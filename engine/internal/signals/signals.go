// Package signals tracks what someone should act on about a person or a
// company: churn risk, an upsell, a renewal coming up. A signal has a
// type from the brand's own list, an owner, a due date and, once closed,
// an outcome, so every flag is followed to its end and a brand can see
// which kinds of signal are worth raising.
//
// A signal is raised by an event a signal type listens to (a rule emits
// account.churn_risk; the "Churn risk" type names that event), or by a
// person or an agent through the API. Owners are not flooded: a record
// has one open signal per type, at most MaxOpen open at once, and the
// owner hears through alerts, which cap email per day and fill the
// digest. An open signal nobody accepted in time is escalated to the
// brand's owners and admins.
package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/alerts"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	// MaxOpen is the most signals one record has open at once; more are
	// not raised until one closes.
	MaxOpen = 5
	// EscalateAfter is how long past its due date an open signal waits
	// before it goes to the brand's owners and admins.
	EscalateAfter = 48 * time.Hour

	// AlertRaised and AlertEscalated are the alert kinds.
	AlertRaised    = "signal.raised"
	AlertEscalated = "signal.escalated"
)

// Outcomes of raising a signal.
const (
	Raised    = "raised"
	Duplicate = "duplicate" // the record already has an open signal of this type
	Capped    = "capped"    // the record has MaxOpen open signals
	NoRecord  = "no_record"
)

// Starter are the types a brand starts with; it can change the list.
var Starter = []struct {
	Key, Label, Description string
	DueDays                 int
}{
	{"churn_risk", "Churn risk", "Signs the customer may leave: usage falling, payments failing, going quiet.", 2},
	{"upsell", "Upsell", "Room to grow the account: more usage, a bigger plan, a commitment.", 5},
	{"cross_sell", "Cross-sell", "A product they don't use yet fits what they do.", 5},
	{"renewal", "Renewal", "An agreement is coming up for renewal.", 7},
	{"billing", "Billing", "A payment failed or the account is past due.", 2},
	{"feature_request", "Feature request", "The customer asked for something the product doesn't do yet.", 7},
	{"competitor", "Competitor mentioned", "The customer is comparing, trialling or moving to a competitor.", 3},
	{"other", "Other", "Anything else worth someone's attention.", 5},
}

// IsStarter reports whether key is one of the starter types.
func IsStarter(key string) bool {
	for _, t := range Starter {
		if t.Key == key {
			return true
		}
	}
	return false
}

var keyRE = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// ValidKey reports whether key can name a signal type.
func ValidKey(key string) bool { return keyRE.MatchString(key) }

// EnsureTypes gives a brand the starter types when it has none.
func EnsureTypes(ctx context.Context, q jobs.Querier, ws, brand string) error {
	var has bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM signal_types WHERE brand_id = $1)`, brand).Scan(&has); err != nil || has {
		return err
	}
	for _, t := range Starter {
		if _, err := q.Exec(ctx, `INSERT INTO signal_types (workspace_id, brand_id, key, label, description, due_days)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT DO NOTHING`, ws, brand, t.Key, t.Label, t.Description, t.DueDays); err != nil {
			return err
		}
	}
	return nil
}

// Signal is a signal to raise.
type Signal struct {
	WorkspaceID, BrandID, RecordID string
	Type                           string
	Title, Detail                  string
	Source                         string // event, journey, manual, agent, api or feed
	EventID                        string
	OwnerID                        *string // default: the record's owner
	DueAt                          *time.Time
	CreatedBy                      string
	Data                           map[string]any
	// Who raised it, for the audit log; empty for the system.
	ActorID, ActorType, UserID string
	RequestID                  string
}

// Raise keeps a signal and tells its owner, in tx. It returns the
// signal's id and Raised, or "" and why nothing was raised.
func Raise(ctx context.Context, tx pgx.Tx, s Signal) (string, string, error) {
	// The record's lock makes two raises about one record take turns.
	var attrs []byte
	err := tx.QueryRow(ctx, `SELECT attributes FROM records WHERE id = $1 AND workspace_id = $2 AND brand_id = $3
		AND merged_into IS NULL AND archived_at IS NULL AND deleted_at IS NULL FOR NO KEY UPDATE`, s.RecordID, s.WorkspaceID, s.BrandID).Scan(&attrs)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", NoRecord, nil
	}
	if err != nil {
		return "", "", err
	}
	var same, open int
	if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE type = $2), count(*) FROM signals WHERE record_id = $1 AND status IN ('open', 'accepted')`,
		s.RecordID, s.Type).Scan(&same, &open); err != nil {
		return "", "", err
	}
	switch {
	case same > 0:
		return "", Duplicate, nil
	case open >= MaxOpen:
		return "", Capped, nil
	}
	var dueDays int
	var label string
	err = tx.QueryRow(ctx, `SELECT due_days, label FROM signal_types WHERE brand_id = $1 AND key = $2 AND archived_at IS NULL`, s.BrandID, s.Type).Scan(&dueDays, &label)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", fmt.Errorf("signals: unknown type %q", s.Type)
	}
	if err != nil {
		return "", "", err
	}
	if s.Title == "" {
		s.Title = label
	}
	if s.DueAt == nil {
		due := time.Now().AddDate(0, 0, dueDays)
		s.DueAt = &due
	}
	if s.OwnerID == nil {
		a := map[string]any{}
		_ = json.Unmarshal(attrs, &a)
		if owner, _ := a["owner"].(string); owner != "" {
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id::text = $2)`, s.WorkspaceID, owner).Scan(&member); err != nil {
				return "", "", err
			}
			if member {
				s.OwnerID = &owner
			}
		}
	}
	if s.Data == nil {
		s.Data = map[string]any{}
	}
	data, err := json.Marshal(s.Data)
	if err != nil {
		return "", "", err
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO signals (workspace_id, brand_id, record_id, type, title, detail, source, event_id, owner_id, due_at, created_by, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, '')::uuid, $9, $10, NULLIF($11, '')::uuid, $12) RETURNING id::text`,
		s.WorkspaceID, s.BrandID, s.RecordID, s.Type, s.Title, s.Detail, s.Source, s.EventID, s.OwnerID, s.DueAt, s.CreatedBy, data).Scan(&id); err != nil {
		return "", "", err
	}
	ev := audit.Event{ActorType: "system"}
	if s.ActorType != "" {
		ev.ActorID, ev.ActorType, ev.UserID = s.ActorID, s.ActorType, s.UserID
	}
	ev.WorkspaceID, ev.BrandID, ev.Action, ev.TargetType, ev.TargetID, ev.RequestID = s.WorkspaceID, s.BrandID, "signal.raised", "signal", id, s.RequestID
	ev.Data = map[string]any{"signal_id": id, "record_id": s.RecordID, "type": s.Type, "source": s.Source, "owner_id": s.OwnerID, "due_at": s.DueAt}
	ev.Props = map[string]any{"source": s.Source, "has_owner": s.OwnerID != nil}
	if err := audit.Record(ctx, tx, ev); err != nil {
		return "", "", err
	}
	if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: s.WorkspaceID, BrandID: &s.BrandID, Kind: AlertRaised, DedupeKey: id,
		TargetType: "signal", TargetID: id, RecordID: &s.RecordID, OwnerID: s.OwnerID, SkipActorID: s.CreatedBy,
		Data: map[string]any{"type": s.Type, "source": s.Source}, RequestID: s.RequestID}); err != nil {
		return "", "", err
	}
	return id, Raised, nil
}

func init() {
	alerts.Register(alerts.Kind{ID: AlertRaised, Title: "Signal raised",
		Description: "A signal about a person or company you own: churn risk, an upsell, a renewal coming up.",
		Scope:       "customers.read", Describe: describe("New signal")})
	alerts.Register(alerts.Kind{ID: AlertEscalated, Title: "Signal not picked up",
		Description: "A signal nobody accepted two days past its due date.",
		Scope:       "customers.read", Describe: describe("Signal not picked up")})
}

// describe is an alert's view: the signal's type and who it is about,
// read fresh.
func describe(title string) func(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
	return func(ctx context.Context, q jobs.Querier, a alerts.Row) (alerts.View, error) {
		v := alerts.View{Title: title, Path: "signals"}
		var label, name, text string
		err := q.QueryRow(ctx, `SELECT COALESCE(t.label, s.type), r.name, s.title FROM signals s JOIN records r ON r.id = s.record_id
			LEFT JOIN signal_types t ON t.brand_id = s.brand_id AND t.key = s.type WHERE s.id = $1 AND s.workspace_id = $2`, a.TargetID, a.WorkspaceID).
			Scan(&label, &name, &text)
		if errors.Is(err, pgx.ErrNoRows) {
			return v, nil
		}
		if err != nil {
			return v, err
		}
		v.Title = title + ": " + label
		v.Text = strings.TrimSuffix(strings.TrimSpace(name+". "+text), ".") + "."
		return v, nil
	}
}

// JobEvent raises the signals of the types that listen to an event.
const JobEvent = customers.JobSignalEvent

// Service raises signals from events and escalates late ones.
type Service struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

// Register adds the job handler to q.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobEvent, s.eventJob) }

func (s *Service) eventJob(ctx context.Context, job jobs.Job) error {
	var in struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(job.Payload, &in); err != nil || job.WorkspaceID == nil || in.EventID == "" {
		return fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	ws := *job.WorkspaceID
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var rec, brand *string
		var name, source string
		var occurred, received time.Time
		err := tx.QueryRow(ctx, `SELECT record_id::text, brand_id::text, name, source, occurred_at, received_at FROM customer_events WHERE workspace_id = $1 AND id = $2`,
			ws, in.EventID).Scan(&rec, &brand, &name, &source, &occurred, &received)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && (rec == nil || brand == nil)) {
			return nil
		}
		if err != nil {
			return err
		}
		// As with rules: a browser event anyone can send raises nothing,
		// and neither does one that arrived more than a day late.
		if source == "browser" || received.Sub(occurred) > 24*time.Hour {
			metrics.SignalRaised("event", "skipped")
			return nil
		}
		rows, err := tx.Query(ctx, `SELECT key, label FROM signal_types WHERE brand_id = $1 AND $2 = ANY (events) AND archived_at IS NULL ORDER BY key`, *brand, name)
		if err != nil {
			return err
		}
		types, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) ([2]string, error) {
			var t [2]string
			return t, row.Scan(&t[0], &t[1])
		})
		if err != nil {
			return err
		}
		for _, t := range types {
			_, outcome, err := Raise(ctx, tx, Signal{WorkspaceID: ws, BrandID: *brand, RecordID: *rec, Type: t[0], Source: "event", EventID: in.EventID,
				Data: map[string]any{"event": name}})
			if err != nil {
				return err
			}
			metrics.SignalRaised("event", outcome)
		}
		return nil
	})
}

// Run escalates late signals every tick until ctx ends.
func (s *Service) Run(ctx context.Context, tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		if _, err := s.Escalate(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "signal.escalation_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		var open, late int
		if err := s.Pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE due_at < now()) FROM signals WHERE status = 'open'`).Scan(&open, &late); err == nil {
			metrics.SetSignalsOpen(open, late)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Escalate hands open signals nobody accepted EscalateAfter past their
// due date to the brand's owners and admins, up to 200 a round, and
// reports how many.
func (s *Service) Escalate(ctx context.Context) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, workspace_id::text, brand_id::text, record_id::text, type FROM signals
			WHERE status = 'open' AND escalated_at IS NULL AND due_at < now() - make_interval(secs => $1)
			  AND (snoozed_until IS NULL OR snoozed_until < now())
			ORDER BY due_at LIMIT 200 FOR UPDATE SKIP LOCKED`, EscalateAfter.Seconds())
		if err != nil {
			return err
		}
		type late struct{ id, ws, brand, record, typ string }
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (late, error) {
			var l late
			return l, row.Scan(&l.id, &l.ws, &l.brand, &l.record, &l.typ)
		})
		if err != nil {
			return err
		}
		for _, l := range list {
			if _, err := tx.Exec(ctx, `UPDATE signals SET escalated_at = now(), updated_at = now() WHERE id = $1`, l.id); err != nil {
				return err
			}
			if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: l.ws, BrandID: l.brand, ActorType: "system", Action: "signal.escalated",
				TargetType: "signal", TargetID: l.id, Data: map[string]any{"signal_id": l.id, "record_id": l.record, "type": l.typ}}); err != nil {
				return err
			}
			// No owner on the alert: it goes to the owners and admins.
			if _, err := alerts.Raise(ctx, tx, alerts.Alert{WorkspaceID: l.ws, BrandID: &l.brand, Kind: AlertEscalated, DedupeKey: l.id,
				TargetType: "signal", TargetID: l.id, RecordID: &l.record, Data: map[string]any{"type": l.typ}}); err != nil {
				return err
			}
			metrics.SignalEscalated()
			n++
		}
		return nil
	})
	return n, err
}

// Conversations a brand's own AI can read for signals.
var Channels = []string{"support", "telegram", "email", "call"}

// JobConversation reads one conversation for signals (the API server
// handles it: it holds the workspace's AI and the keys to sealed text).
const JobConversation = "signals.read_conversation"

// ReadDelay is how long after a customer's message a conversation is
// read, so a burst of messages is read once.
const ReadDelay = 10 * time.Minute

// EnqueueConversation queues a read of a conversation, in the
// transaction that stored something new in it, when the brand reads that
// channel. A conversation is read at most once per half hour.
func EnqueueConversation(ctx context.Context, q jobs.Querier, ws, brand, channel, ref string) error {
	if brand == "" || ref == "" {
		return nil
	}
	var on bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM conversation_signal_settings WHERE brand_id = $1 AND workspace_id = $2 AND enabled AND $3 = ANY (channels))`,
		brand, ws, channel).Scan(&on); err != nil || !on {
		return err
	}
	now := time.Now().UTC()
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobConversation, WorkspaceID: &ws, MaxAttempts: 3, RunAt: now.Add(ReadDelay),
		IdempotencyKey: fmt.Sprintf("%s:%s:%d", channel, ref, now.Unix()/1800),
		Payload:        map[string]string{"brand_id": brand, "channel": channel, "ref": ref}})
	return err
}
