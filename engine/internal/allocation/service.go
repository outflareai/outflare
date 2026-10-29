package allocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobPlan runs the allocator for one workspace.
const JobPlan = "allocation.plan"

// Modes.
const (
	ModeSuggest   = "suggest"
	ModeAutomatic = "automatic"
)

// Service runs the allocator against Postgres.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
	// Now is overridable in tests.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) Register(q *jobs.Queue) { q.Handle(JobPlan, s.planJob) }

type planPayload struct {
	WorkspaceID string `json:"workspace_id"`
}

// Request asks for the workspace to be re-planned shortly. Calls within a
// few seconds of each other share one run, so a bulk change plans once.
func Request(ctx context.Context, q jobs.Querier, workspaceID string) error {
	const window = 3 * time.Second
	at := time.Now().Truncate(window).Add(window)
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Kind: JobPlan, WorkspaceID: &workspaceID, Payload: planPayload{workspaceID}, RunAt: at, MaxAttempts: 3,
		IdempotencyKey: workspaceID + ":" + strconv.FormatInt(at.Unix(), 10),
	})
	return err
}

func (s *Service) planJob(ctx context.Context, job jobs.Job) error {
	var p planPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	_, err := s.Run(ctx, p.WorkspaceID)
	return err
}

// Loop re-plans every workspace that uses sequences each interval, which
// catches what no event announces: mailboxes finishing their follow-ups,
// locks expiring and lifecycle stages ending.
func (s *Service) Loop(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		rows, err := s.Pool.Query(ctx, `SELECT DISTINCT workspace_id::text FROM sequences WHERE status IN ('active', 'paused')
			UNION SELECT DISTINCT workspace_id::text FROM mailbox_assignments`)
		if err == nil {
			var ids []string
			ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
			for _, ws := range ids {
				if _, err := s.Run(ctx, ws); err != nil && ctx.Err() == nil {
					s.Log.Error("allocation run", "workspace_id", ws, "err", err)
				}
			}
		}
		if err != nil && ctx.Err() == nil {
			s.Log.Error("allocation loop", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Result is what one run did.
type Result struct {
	Applied   []Change `json:"applied"`
	Suggested []Change `json:"suggested"`
}

// Run plans the workspace and applies what its modes allow, in one
// transaction holding the workspace's allocation lock.
func (s *Service) Run(ctx context.Context, workspaceID string) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('allocation:' || $1::text, 0))`, workspaceID); err != nil {
			return err
		}
		st, err := Load(ctx, tx, workspaceID, s.now())
		if err != nil {
			return err
		}
		plan := Make(st.Input)
		res, err = st.apply(ctx, tx, plan, s.now())
		return err
	})
	return res, err
}

// Preview plans the workspace without changing anything: a dry run.
func Preview(ctx context.Context, tx pgx.Tx, workspaceID string, now time.Time) (*State, Plan, error) {
	st, err := Load(ctx, tx, workspaceID, now)
	if err != nil {
		return nil, Plan{}, err
	}
	return st, Make(st.Input), nil
}

// State is a workspace's allocation input plus what applying needs.
type State struct {
	Input       Input
	WorkspaceID string
	// Modes holds each sequence's effective mode.
	Modes map[string]string
}

// Load reads the workspace's sequences and mailboxes.
func Load(ctx context.Context, tx pgx.Tx, workspaceID string, now time.Time) (*State, error) {
	st := &State{WorkspaceID: workspaceID, Modes: map[string]string{}, Input: Input{Declined: map[string]bool{}}}
	drows, err := tx.Query(ctx, `SELECT mailbox_id::text || '/' || sequence_id::text FROM allocation_declines WHERE workspace_id = $1`, workspaceID)
	if err != nil {
		return nil, err
	}
	declined, err := pgx.CollectRows(drows, pgx.RowTo[string])
	if err != nil {
		return nil, err
	}
	for _, k := range declined {
		st.Input.Declined[k] = true
	}
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE workspace_id = $1)`, workspaceID).Scan(&st.Input.HasTeams); err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT s.id::text, s.name, s.status, COALESCE(s.team_id::text, ''), s.priority,
		       s.target_daily, s.daily_new_limit, (SELECT count(*) FROM sequence_steps WHERE sequence_id = s.id)::int,
		       COALESCE(s.min_daily, 0), COALESCE(s.pool_id::text, ''), COALESCE(s.brand_id::text, ''), s.created_at,
		       COALESCE(s.allocation_mode, t.allocation_mode, w.allocation_mode),
		       tok.id IS NOT NULL, COALESCE(tok.may_take_mailboxes, true), tok.daily_send_budget,
		       COALESCE((SELECT array_agg(sender_id::text) FROM sequence_senders WHERE sequence_id = s.id), '{}')
		FROM sequences s
		JOIN workspaces w ON w.id = s.workspace_id
		LEFT JOIN teams t ON t.id = s.team_id
		LEFT JOIN api_tokens tok ON tok.actor_id = s.created_by
		WHERE s.workspace_id = $1 AND (s.status <> 'archived' OR EXISTS (SELECT 1 FROM mailbox_assignments a WHERE a.sequence_id = s.id))`,
		workspaceID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var q Sequence
		var target, newLimit, budget *int
		var steps int
		var mode string
		var byToken bool
		var preferred []string
		if err := rows.Scan(&q.ID, &q.Name, &q.Status, &q.TeamID, &q.Priority, &target, &newLimit, &steps,
			&q.MinDaily, &q.PoolID, &q.BrandID, &q.CreatedAt, &mode, &byToken, &q.MayTake, &budget, &preferred); err != nil {
			rows.Close()
			return nil, err
		}
		q.Demand = Demand(target, newLimit, steps, budget)
		q.Preferred = map[string]bool{}
		for _, id := range preferred {
			q.Preferred[id] = true
		}
		st.Input.Sequences = append(st.Input.Sequences, q)
		st.Modes[q.ID] = mode
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rows, err = tx.Query(ctx, `
		SELECT m.id::text, m.email, m.domain, COALESCE(m.pool_id::text, ''), COALESCE(m.brand_id::text, ''), m.daily_limit, m.status,
		       COALESCE(d.status, 'active'), COALESCE(d.spf, 'pending'),
		       COALESCE(d.blocklists @> '[{"status": "listed"}]'::jsonb, false),
		       m.lifecycle_stage, m.stage_until,
		       COALESCE(m.sender_id::text, ''), COALESCE(sn.name, ''), m.sender_confirmed_at IS NOT NULL,
		       COALESCE(sn.kind = 'shared', false), COALESCE(sn.status = 'retired', false),
		       COALESCE((SELECT array_agg(team_id::text) FROM sender_teams WHERE sender_id = m.sender_id), '{}'),
		       l.kind, COALESCE(l.sequence_id::text, ''), COALESCE(l.team_id::text, ''),
		       a.sequence_id::text, a.state, COALESCE(a.next_sequence_id::text, ''), a.source, a.version,
		       CASE WHEN a.link_id IS NULL
		            THEN (SELECT count(*) FROM enrollments e WHERE e.mailbox_id = m.id AND e.sequence_id = a.sequence_id AND e.status = 'active')::int
		            WHEN a.state = 'draining' THEN (a.drain_until > $2)::int
		            ELSE (a.confirmed_at IS NOT NULL)::int END,
		       COALESCE(a.link_id::text, ''), COALESCE(cl.sequence_days, $3), NOT m.has_login,
		       COALESCE(srv.status = 'paused', false)
		FROM mailboxes m
		LEFT JOIN sending_domains d ON d.workspace_id = m.workspace_id AND d.domain = m.domain
		LEFT JOIN sending_servers srv ON srv.id = m.server_id
		LEFT JOIN senders sn ON sn.id = m.sender_id
		LEFT JOIN mailbox_locks l ON l.mailbox_id = m.id AND (l.until IS NULL OR l.until > $2)
		LEFT JOIN mailbox_assignments a ON a.mailbox_id = m.id
		LEFT JOIN campaign_links cl ON cl.id = a.link_id
		WHERE m.workspace_id = $1`, workspaceID, now, DefaultSequenceDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Mailbox
		var status, domainStatus, spf string
		var listed bool
		var stage string
		var stageUntil *time.Time
		var lockKind, assignSeq, assignState, assignSource *string
		var lockSeq, lockTeam, assignNext string
		var version *int
		var inFlight, drainDays int
		var linkID string
		var serverPaused bool
		if err := rows.Scan(&m.ID, &m.Email, &m.Domain, &m.PoolID, &m.BrandID, &m.DailyLimit, &status,
			&domainStatus, &spf, &listed, &stage, &stageUntil,
			&m.SenderID, &m.SenderName, &m.SenderConfirmed, &m.SenderShared, &m.SenderRetired, &m.SenderTeams,
			&lockKind, &lockSeq, &lockTeam, &assignSeq, &assignState, &assignNext, &assignSource, &version, &inFlight,
			&linkID, &drainDays, &m.NoLogin, &serverPaused); err != nil {
			return nil, err
		}
		m.NotReady = NotReady(Readiness{
			Status: status, Stage: stage, StageUntil: stageUntil, DomainPaused: domainStatus == "paused", ServerPaused: serverPaused, SPF: spf, Blocklisted: listed,
		}, now)
		if lockKind != nil {
			m.Lock = &Lock{Kind: *lockKind, SequenceID: lockSeq, TeamID: lockTeam}
		}
		if assignSeq != nil {
			m.Assignment = &Assignment{SequenceID: *assignSeq, State: *assignState, NextSequenceID: assignNext,
				Source: *assignSource, Version: *version, InFlight: inFlight, LinkID: linkID, DrainDays: drainDays}
		}
		if m.NoLogin {
			// Without a login Outflare cannot check the mailbox or warm it
			// up; its domain checks still apply, and the platform's own
			// status and warm-up health stand in for the rest (below).
			m.NotReady = NotReady(Readiness{Status: "active", DomainPaused: domainStatus == "paused", ServerPaused: serverPaused, SPF: spf, Blocklisted: listed}, now)
		}
		st.Input.Mailboxes = append(st.Input.Mailboxes, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err := st.loadPlatforms(ctx, tx, now); err != nil {
		return nil, err
	}
	return st, nil
}

// Demand is the sends a day a sequence should have capacity for: its
// target, else its daily new-lead limit times its steps (each lead gets
// every step), capped by its token's daily budget. 0 means none is set.
func Demand(target, dailyNewLimit *int, steps int, budget *int) int {
	d := 0
	switch {
	case target != nil:
		d = *target
	case dailyNewLimit != nil && steps > 0:
		d = *dailyNewLimit * steps
	}
	if budget != nil && d > *budget {
		d = *budget
	}
	return d
}

// Readiness is what decides whether a mailbox may be given new work.
type Readiness struct {
	Status string
	// Lifecycle stage (warming, ready, resting, retired), and when the
	// warm-up or rest ends.
	Stage        string
	StageUntil   *time.Time
	DomainPaused bool
	// ServerPaused is the mailbox's sending server (its SMTP host) being
	// paused, by a person or a blocklist listing, the same as a domain.
	ServerPaused bool
	SPF          string
	Blocklisted  bool
}

// NotReady returns why a mailbox cannot be given new work, or "". A
// mailbox still warming up is in the warming stage; a sequencer's
// warm-up score is checked with its platform (platforms.go).
func NotReady(r Readiness, now time.Time) string {
	switch {
	case r.Stage == "retired":
		return "mailbox is retired"
	case r.Stage != "" && r.Stage != "ready" && r.StageUntil != nil && r.StageUntil.After(now):
		return "mailbox is " + r.Stage + " until " + r.StageUntil.UTC().Format("Mon 2 Jan")
	case r.Stage != "" && r.Stage != "ready":
		return "mailbox is " + r.Stage
	case r.Status != "active":
		return "mailbox is " + r.Status
	case r.DomainPaused:
		return "domain is paused"
	case r.ServerPaused:
		return "sending server is paused"
	case r.SPF == "missing" || r.SPF == "invalid":
		return "domain SPF record is " + r.SPF
	case r.Blocklisted:
		return "domain is on a blocklist"
	}
	return ""
}

// mode is the mode that decides change c.
func (st *State) mode(c Change) string {
	if c.Action == FinishDrain {
		return ModeAutomatic // carries out a decision already made
	}
	id := c.To
	if c.Action == Release || c.Action == Drain && c.To == "" {
		id = c.From
	}
	if m := st.Modes[id]; m != "" {
		return m
	}
	return ModeAutomatic
}

func (st *State) apply(ctx context.Context, tx pgx.Tx, plan Plan, now time.Time) (Result, error) {
	var res Result
	if _, err := tx.Exec(ctx, `DELETE FROM allocation_suggestions WHERE workspace_id = $1`, st.WorkspaceID); err != nil {
		return res, err
	}
	for _, c := range plan.Changes {
		if c.Needs != "" || st.mode(c) == ModeSuggest {
			needs := c.Needs
			if needs == "" {
				needs = "review"
			}
			if err := suggest(ctx, tx, st.WorkspaceID, c, needs); err != nil {
				return res, err
			}
			res.Suggested = append(res.Suggested, c)
			continue
		}
		done, err := ApplyChange(ctx, tx, st.WorkspaceID, c, "automatic", "", nil)
		if err != nil {
			return res, err
		}
		if done != nil {
			res.Applied = append(res.Applied, *done)
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM allocation_status WHERE workspace_id = $1`, st.WorkspaceID); err != nil {
		return res, err
	}
	for _, s := range plan.Status {
		blocking, _ := json.Marshal(s.Blocking)
		if s.Blocking == nil {
			blocking = []byte("[]")
		}
		var needed *int
		if s.Needed > 0 {
			needed = &s.Needed
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO allocation_status (sequence_id, workspace_id, needed_daily, active_daily, blocking, note, planned_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, s.SequenceID, st.WorkspaceID, needed, s.Active, blocking, s.Note, now); err != nil {
			return res, err
		}
	}
	return res, nil
}

func suggest(ctx context.Context, tx pgx.Tx, ws string, c Change, needs string) error {
	seqID := c.To
	if seqID == "" {
		seqID = c.From
	}
	action := c.Action
	if action == Move {
		action = Assign // approving a move assigns the mailbox to To
	}
	if action == FinishDrain {
		return nil
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO allocation_suggestions (workspace_id, sequence_id, mailbox_id, action, from_sequence_id, needs, approver_team_id, reason, base_version)
		VALUES ($1, $2, $3, $4, NULLIF($5, '')::uuid, $6, NULLIF($7, '')::uuid, $8, $9)`,
		ws, seqID, c.MailboxID, action, c.From, needs, c.ApproverTeam, c.Reason, c.BaseVersion)
	return err
}

// ErrConflict means the mailbox changed since the change was planned.
var ErrConflict = errors.New("the mailbox changed since this was planned")

// ApplyChange carries out one change under the hard rules, re-checked
// with the mailbox row locked: a mailbox that gained leads in flight
// drains instead of moving, and one that changed since planning is left
// alone (nil result). source is automatic, manual, approval or undo.
func ApplyChange(ctx context.Context, tx pgx.Tx, ws string, c Change, source, actorID string, actorType *string) (*Change, error) {
	var lockKind *string
	if err := tx.QueryRow(ctx, `
		SELECT l.kind FROM mailboxes m LEFT JOIN mailbox_locks l ON l.mailbox_id = m.id AND (l.until IS NULL OR l.until > now())
		WHERE m.id = $1 AND m.workspace_id = $2 FOR UPDATE OF m`, c.MailboxID, ws).Scan(&lockKind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if source == "automatic" && lockKind != nil && *lockKind == LockManualOnly && c.Action != FinishDrain {
		return nil, nil
	}
	var cur struct {
		seq, state, link string
		version          int
		confirmed        bool
		days             int
	}
	err := tx.QueryRow(ctx, `
		SELECT a.sequence_id::text, a.state, a.version, COALESCE(a.link_id::text, ''), a.confirmed_at IS NOT NULL, COALESCE(l.sequence_days, $2)
		FROM mailbox_assignments a LEFT JOIN campaign_links l ON l.id = a.link_id
		WHERE a.mailbox_id = $1`, c.MailboxID, DefaultSequenceDays).
		Scan(&cur.seq, &cur.state, &cur.version, &cur.link, &cur.confirmed, &cur.days)
	free := errors.Is(err, pgx.ErrNoRows)
	if err != nil && !free {
		return nil, err
	}
	if c.Action != Assign && (free || cur.seq != c.From || (c.BaseVersion != 0 && cur.version != c.BaseVersion)) {
		return nil, conflict(source)
	}
	if c.Action == Assign && !free {
		if source != "automatic" && cur.seq == c.To && cur.state == "active" {
			return nil, nil // already there
		}
		return nil, conflict(source)
	}

	// A mailbox attached to a platform campaign may have leads waiting on
	// it there, and platforms do not say, so it stays for the campaign's
	// length before it leaves.
	if (c.Action == Release || c.Action == Move) && cur.link != "" && cur.confirmed {
		var seqStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM sequences WHERE id = $1`, c.From).Scan(&seqStatus); err != nil {
			return nil, err
		}
		if seqStatus != "archived" && c.Action != Drain {
			c.Action = Drain
			c.Reason += drainText(&Assignment{LinkID: cur.link, DrainDays: cur.days}, 1, "", "")
		}
	} else if c.Action == Release || c.Action == Move {
		// Leads in flight turn a release or move into a drain.
		var inFlight int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM enrollments WHERE mailbox_id = $1 AND sequence_id = $2 AND status = 'active'`,
			c.MailboxID, c.From).Scan(&inFlight); err != nil {
			return nil, err
		}
		var seqStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM sequences WHERE id = $1`, c.From).Scan(&seqStatus); err != nil {
			return nil, err
		}
		if inFlight > 0 && seqStatus != "archived" {
			c.Action = Drain
			c.Reason += fmt.Sprintf(" It still has %d follow-up%s to send, so it finishes them first.", inFlight, plural(inFlight))
		}
	}

	var version int
	switch c.Action {
	case Assign:
		err = tx.QueryRow(ctx, `
			INSERT INTO mailbox_assignments (mailbox_id, workspace_id, sequence_id, source, reason, link_id)
			VALUES ($1, $2, $3, $4, $5, NULLIF($6, '')::uuid) RETURNING version`, c.MailboxID, ws, c.To, assignSource(source), c.Reason, c.LinkID).Scan(&version)
	case Release:
		_, err = tx.Exec(ctx, `DELETE FROM mailbox_assignments WHERE mailbox_id = $1`, c.MailboxID)
	case Move:
		err = tx.QueryRow(ctx, `
			UPDATE mailbox_assignments SET sequence_id = $2, state = 'active', next_sequence_id = NULL, source = $3, reason = $4,
				link_id = NULLIF($5, '')::uuid, confirmed_at = NULL, drain_until = NULL, version = version + 1, updated_at = now()
			WHERE mailbox_id = $1 RETURNING version`, c.MailboxID, c.To, assignSource(source), c.Reason, c.LinkID).Scan(&version)
	case Drain:
		err = tx.QueryRow(ctx, `
			UPDATE mailbox_assignments SET state = 'draining', next_sequence_id = NULLIF($2, '')::uuid, reason = $3,
				drain_until = CASE WHEN link_id IS NULL THEN NULL ELSE now() + make_interval(days => $4) END,
				version = version + 1, updated_at = now()
			WHERE mailbox_id = $1 RETURNING version`, c.MailboxID, c.To, c.Reason, cur.days).Scan(&version)
	case FinishDrain:
		if c.To == "" {
			_, err = tx.Exec(ctx, `DELETE FROM mailbox_assignments WHERE mailbox_id = $1`, c.MailboxID)
		} else {
			err = tx.QueryRow(ctx, `
				UPDATE mailbox_assignments SET sequence_id = $2, state = 'active', next_sequence_id = NULL, reason = $3,
					link_id = NULLIF($4, '')::uuid, confirmed_at = NULL, drain_until = NULL, version = version + 1, updated_at = now()
				WHERE mailbox_id = $1 RETURNING version`, c.MailboxID, c.To, c.Reason, c.LinkID).Scan(&version)
		}
	default:
		return nil, fmt.Errorf("unknown action %q", c.Action)
	}
	if err != nil {
		return nil, err
	}
	// Platform calls run as jobs after this commits; the platform sync
	// confirms them by reading the campaign back.
	leaves := c.Action == Release || c.Action == Move || c.Action == FinishDrain
	if leaves && cur.link != "" && cur.link != c.LinkID {
		if err := enqueuePlatform(ctx, tx, ws, OpDetach, c.MailboxID, cur.link); err != nil {
			return nil, err
		}
	}
	joins := c.Action == Assign || c.Action == Move || (c.Action == FinishDrain && c.To != "")
	if joins && c.LinkID != "" {
		if err := enqueuePlatform(ctx, tx, ws, OpAttach, c.MailboxID, c.LinkID); err != nil {
			return nil, err
		}
	}
	var v *int
	if version > 0 {
		v = &version
	}
	var actor *string
	if actorID != "" {
		actor = &actorID
	}
	var changeID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO allocation_changes (workspace_id, mailbox_id, action, from_sequence_id, to_sequence_id, source, reason, actor_id, version, link_id)
		VALUES ($1, $2, $3, NULLIF($4, '')::uuid, NULLIF($5, '')::uuid, $6, $7, $8, $9, NULLIF($10, '')::uuid) RETURNING id`,
		ws, c.MailboxID, c.Action, c.From, c.To, source, c.Reason, actor, v, c.LinkID).Scan(&changeID); err != nil {
		return nil, err
	}
	ev := audit.Event{WorkspaceID: ws, ActorType: "system", Action: "mailbox.allocation." + c.Action, TargetType: "mailbox", TargetID: c.MailboxID,
		RequestID: requestID(ctx),
		Data:      map[string]any{"from_sequence_id": c.From, "to_sequence_id": c.To, "source": source, "reason": c.Reason, "change_id": changeID}}
	if actorID != "" && actorType != nil {
		ev.ActorID, ev.ActorType = actorID, *actorType
	}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return nil, err
	}
	return &c, nil
}

func conflict(source string) error {
	if source == "automatic" {
		return nil // planned against a state a person has since changed: their change stands
	}
	return ErrConflict
}

func assignSource(source string) string {
	if source == "automatic" {
		return "automatic"
	}
	return "manual"
}

type ctxKey struct{}

// WithRequestID tags changes made in ctx with the HTTP request id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func requestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}
