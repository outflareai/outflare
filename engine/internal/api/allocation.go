package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/allocation"
	"github.com/outflareai/outflare/engine/internal/auth"
)

// Mailbox allocation: which mailboxes send for which sequence. People and
// agents assign, release and lock mailboxes here; the allocator does the
// routine work and explains every change. See the allocation package.

func (s *Server) routesAllocation(mux *http.ServeMux) {
	read, manage := auth.ScopeSequencesRead, auth.ScopeSequencesManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/allocation/settings", s.inWorkspace(read, s.allocationSettings))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/allocation/settings", s.inWorkspace(auth.ScopeMembersManage, s.updateAllocationSettings))
	mux.HandleFunc("GET /v1/workspaces/{ws}/allocation/plan", s.inWorkspace(read, s.previewAllocation))
	mux.HandleFunc("POST /v1/workspaces/{ws}/allocation/run", s.inWorkspace(manage, s.runAllocation))
	mux.HandleFunc("GET /v1/workspaces/{ws}/allocation/suggestions", s.inWorkspace(read, s.listSuggestions))
	mux.HandleFunc("POST /v1/workspaces/{ws}/allocation/suggestions/{id}/apply", s.inWorkspace(manage, s.applySuggestion))
	mux.HandleFunc("POST /v1/workspaces/{ws}/allocation/suggestions/{id}/decline", s.inWorkspace(manage, s.declineSuggestion))
	mux.HandleFunc("GET /v1/workspaces/{ws}/allocation/changes", s.inWorkspace(read, s.listChanges))
	mux.HandleFunc("POST /v1/workspaces/{ws}/allocation/changes/{id}/undo", s.inWorkspace(manage, s.undoChange))
	mux.HandleFunc("GET /v1/workspaces/{ws}/allocation/attention", s.inWorkspace(read, s.allocationAttention))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/assign", s.inWorkspace(manage, s.assignMailboxes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/release", s.inWorkspace(manage, s.releaseMailboxes))
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/lock", s.inWorkspace(auth.ScopeMailboxesManage, s.lockMailboxes))
	mux.HandleFunc("GET /v1/workspaces/{ws}/sequences/{id}/allocation", s.inWorkspace(read, s.sequenceAllocation))
	mux.HandleFunc("POST /v1/workspaces/{ws}/sequences/{id}/orphaned-follow-ups", s.inWorkspace(manage, s.resolveOrphans))
}

// replans asks the allocator to plan again after a successful change to
// something it plans from, such as a mailbox's status or limit.
func (s *Server) replans(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		h(rec, r)
		if rec.status < 300 {
			if err := allocation.Request(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID); err != nil {
				s.log.Error("allocation request", "err", err)
			}
		}
	}
}

// ---- Settings ----

func (s *Server) allocationSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var mode string
	if err := s.pool.QueryRow(r.Context(), `SELECT allocation_mode FROM workspaces WHERE id = $1`, a.WorkspaceID).Scan(&mode); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": mode})
}

func (s *Server) updateAllocationSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Mode != allocation.ModeSuggest && req.Mode != allocation.ModeAutomatic {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "mode is suggest or automatic")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET allocation_mode = $2 WHERE id = $1`, a.WorkspaceID, req.Mode); err != nil {
			return err
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "allocation.settings_updated", "workspace", a.WorkspaceID, map[string]any{"mode": req.Mode})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": req.Mode})
}

// ---- Plan and run ----

type changeView struct {
	MailboxID    string `json:"mailbox_id"`
	Action       string `json:"action"`
	From         string `json:"from_sequence_id,omitempty"`
	To           string `json:"to_sequence_id,omitempty"`
	Reason       string `json:"reason"`
	Needs        string `json:"needs,omitempty"`
	ApproverTeam string `json:"approver_team_id,omitempty"`
}

func viewChanges(list []allocation.Change) []changeView {
	out := make([]changeView, 0, len(list))
	for _, c := range list {
		out = append(out, changeView{MailboxID: c.MailboxID, Action: c.Action, From: c.From, To: c.To, Reason: c.Reason, Needs: c.Needs, ApproverTeam: c.ApproverTeam})
	}
	return out
}

type capacityView struct {
	SequenceID string                `json:"sequence_id"`
	Needed     *int                  `json:"needed_daily"`
	Active     int                   `json:"active_daily"`
	Incoming   int                   `json:"incoming_daily"`
	Blocking   []allocation.Blocking `json:"blocking"`
	Note       string                `json:"note"`
}

// previewAllocation is a dry run: what the allocator would do now.
func (s *Server) previewAllocation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	st, plan, err := allocation.Preview(ctx, tx, a.WorkspaceID, time.Now())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	caps := []capacityView{}
	for _, q := range st.Input.Sequences {
		c := plan.Status[q.ID]
		if q.Status != "active" {
			continue
		}
		v := capacityView{SequenceID: q.ID, Active: c.Active, Incoming: c.Incoming, Blocking: c.Blocking, Note: c.Note}
		if c.Needed > 0 {
			v.Needed = &c.Needed
		}
		if v.Blocking == nil {
			v.Blocking = []allocation.Blocking{}
		}
		caps = append(caps, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"changes": viewChanges(plan.Changes), "capacity": caps})
}

func (s *Server) runAllocation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	res, err := s.alloc.Run(allocation.WithRequestID(r.Context(), RequestID(r.Context())), a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": viewChanges(res.Applied), "suggested": viewChanges(res.Suggested)})
}

// ---- Suggestions ----

type suggestionView struct {
	ID             string    `json:"id"`
	SequenceID     string    `json:"sequence_id"`
	SequenceName   string    `json:"sequence_name"`
	MailboxID      string    `json:"mailbox_id"`
	MailboxEmail   string    `json:"mailbox_email"`
	Action         string    `json:"action"`
	FromSequenceID *string   `json:"from_sequence_id"`
	Needs          string    `json:"needs"`
	ApproverTeamID *string   `json:"approver_team_id"`
	ApproverTeam   *string   `json:"approver_team_name"`
	Reason         string    `json:"reason"`
	CreatedAt      time.Time `json:"created_at"`
}

const suggestionSelect = `
	SELECT g.id, g.sequence_id, q.name, g.mailbox_id, m.email, g.action, g.from_sequence_id, g.needs,
	       g.approver_team_id, t.name, g.reason, g.created_at
	FROM allocation_suggestions g JOIN sequences q ON q.id = g.sequence_id JOIN mailboxes m ON m.id = g.mailbox_id
	LEFT JOIN teams t ON t.id = g.approver_team_id`

func (s *Server) loadSuggestions(ctx context.Context, ws string, seq *string) ([]suggestionView, error) {
	rows, err := s.pool.Query(ctx, suggestionSelect+` WHERE g.workspace_id = $1 AND ($2::uuid IS NULL OR g.sequence_id = $2)
		ORDER BY g.needs DESC, q.name, m.email`, ws, seq)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[suggestionView])
	if list == nil {
		list = []suggestionView{}
	}
	return list, err
}

func (s *Server) listSuggestions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var seq *string
	if v := r.URL.Query().Get("sequence_id"); v != "" {
		if !validUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "sequence_id must be a UUID")
			return
		}
		seq = &v
	}
	list, err := s.loadSuggestions(r.Context(), a.WorkspaceID, seq)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type suggestionRow struct {
	seq, mailbox, action, needs string
	from, team                  *string
	reason                      string
	base                        int
}

// takeSuggestion locks a suggestion and checks a may decide it: another
// team's mailbox needs a member of that team (or a workspace admin), and
// lending decisions are always a person's.
func (s *Server) takeSuggestion(ctx context.Context, tx pgx.Tx, a *access, id string) (*suggestionRow, error) {
	var g suggestionRow
	err := tx.QueryRow(ctx, `
		SELECT sequence_id::text, mailbox_id::text, action, needs, from_sequence_id::text, approver_team_id::text, reason, base_version
		FROM allocation_suggestions WHERE id = $1 AND workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).
		Scan(&g.seq, &g.mailbox, &g.action, &g.needs, &g.from, &g.team, &g.reason, &g.base)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	if g.needs == "review" {
		return &g, nil
	}
	if a.UserID == "" {
		return nil, errDenied("this needs a person's approval; an API token cannot give it")
	}
	if g.needs == allocation.NeedsTeamApproval && a.Role != auth.RoleOwner && a.Role != auth.RoleAdmin {
		var member bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM team_members WHERE team_id = $1 AND user_id = $2)`, g.team, a.UserID).Scan(&member); err != nil {
			return nil, err
		}
		if !member {
			return nil, errDenied("only a member of the mailbox's team, or a workspace admin, can lend it")
		}
	}
	return &g, nil
}

func (s *Server) applySuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := allocation.WithRequestID(r.Context(), RequestID(r.Context()))
	var done *allocation.Change
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		g, err := s.takeSuggestion(ctx, tx, a, id)
		if err != nil {
			return err
		}
		c := allocation.Change{MailboxID: g.mailbox, Action: g.action, Reason: g.reason, BaseVersion: g.base}
		switch g.action {
		case allocation.Assign:
			c.To = g.seq
			if g.from != nil {
				c.Action, c.From = allocation.Move, *g.from
			}
		case allocation.Release:
			c.From = g.seq
		case allocation.Drain:
			c.From = g.seq
			if g.from != nil {
				c.From, c.To = *g.from, g.seq
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM allocation_suggestions WHERE id = $1`, id); err != nil {
			return err
		}
		if done, err = allocation.ApplyChange(ctx, tx, a.WorkspaceID, c, "approval", a.ActorID, &a.ActorType); err != nil {
			return err
		}
		return allocation.Request(ctx, tx, a.WorkspaceID)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": viewChanges([]allocation.Change{*done})[0]})
}

func (s *Server) declineSuggestion(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		g, err := s.takeSuggestion(ctx, tx, a, id)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM allocation_suggestions WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO allocation_declines (workspace_id, mailbox_id, sequence_id, declined_by) VALUES ($1, $2, $3, $4)
			ON CONFLICT DO NOTHING`, a.WorkspaceID, g.mailbox, g.seq, a.ActorID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.allocation.declined", "mailbox", g.mailbox,
			map[string]any{"sequence_id": g.seq, "action": g.action, "reason": g.reason})
	})
	if s.allocError(w, r, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Change history and undo ----

type allocChangeView struct {
	ID             string     `json:"id"`
	MailboxID      string     `json:"mailbox_id"`
	MailboxEmail   string     `json:"mailbox_email"`
	Action         string     `json:"action"`
	FromSequenceID *string    `json:"from_sequence_id"`
	FromSequence   *string    `json:"from_sequence_name"`
	ToSequenceID   *string    `json:"to_sequence_id"`
	ToSequence     *string    `json:"to_sequence_name"`
	Source         string     `json:"source"`
	Reason         string     `json:"reason"`
	ActorName      *string    `json:"actor_name"`
	UndoneAt       *time.Time `json:"undone_at"`
	CreatedAt      time.Time  `json:"created_at"`
}

func (s *Server) loadChanges(ctx context.Context, ws string, seq, mailbox *string, limit, offset int) ([]allocChangeView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.mailbox_id, m.email, c.action, c.from_sequence_id, f.name, c.to_sequence_id, t.name,
		       c.source, c.reason, ac.display_name, c.undone_at, c.created_at
		FROM allocation_changes c JOIN mailboxes m ON m.id = c.mailbox_id
		LEFT JOIN sequences f ON f.id = c.from_sequence_id LEFT JOIN sequences t ON t.id = c.to_sequence_id
		LEFT JOIN actors ac ON ac.id = c.actor_id
		WHERE c.workspace_id = $1 AND ($2::uuid IS NULL OR c.from_sequence_id = $2 OR c.to_sequence_id = $2)
		  AND ($3::uuid IS NULL OR c.mailbox_id = $3)
		ORDER BY c.created_at DESC, c.id DESC LIMIT $4 OFFSET $5`, ws, seq, mailbox, limit, offset)
	if err != nil {
		return nil, err
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[allocChangeView])
	if list == nil {
		list = []allocChangeView{}
	}
	return list, err
}

func (s *Server) listChanges(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	var seq, mailbox *string
	for key, dst := range map[string]**string{"sequence_id": &seq, "mailbox_id": &mailbox} {
		if v := q.Get(key); v != "" {
			if !validUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", key+" must be a UUID")
				return
			}
			*dst = &v
		}
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 500 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit must be 1 to 500")
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "offset must be 0 or more")
			return
		}
		offset = n
	}
	// One more than asked for says whether there is a next page.
	list, err := s.loadChanges(r.Context(), a.WorkspaceID, seq, mailbox, limit+1, offset)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *int
	if len(list) > limit {
		list = list[:limit]
		n := offset + limit
		next = &n
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_offset": next})
}

// undoChange reverses one change, through the same checks as any other.
// It refuses when the mailbox has changed since, or when undoing would
// move a mailbox away from leads it has started writing to.
func (s *Server) undoChange(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := allocation.WithRequestID(r.Context(), RequestID(r.Context()))
	var done *allocation.Change
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ch struct {
			mailbox, action, source string
			from, to                *string
			version                 *int
			undone                  *time.Time
			latest                  bool
		}
		err := tx.QueryRow(ctx, `
			SELECT c.mailbox_id::text, c.action, c.source, c.from_sequence_id::text, c.to_sequence_id::text, c.version, c.undone_at,
			       NOT EXISTS (SELECT 1 FROM allocation_changes l WHERE l.mailbox_id = c.mailbox_id AND l.created_at > c.created_at)
			FROM allocation_changes c WHERE c.id = $1 AND c.workspace_id = $2 FOR UPDATE`, id, a.WorkspaceID).
			Scan(&ch.mailbox, &ch.action, &ch.source, &ch.from, &ch.to, &ch.version, &ch.undone, &ch.latest)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		switch {
		case ch.undone != nil:
			return errInvalid("this change was already undone")
		case !ch.latest:
			return errInvalid("the mailbox has changed since; undo the later changes first")
		case ch.action == allocation.FinishDrain:
			return errInvalid("a finished drain cannot be undone: its follow-ups were sent")
		}
		var curSeq, curState *string
		var curVersion *int
		if err := tx.QueryRow(ctx, `
			SELECT (SELECT sequence_id::text FROM mailbox_assignments WHERE mailbox_id = m.id),
			       (SELECT state FROM mailbox_assignments WHERE mailbox_id = m.id),
			       (SELECT version FROM mailbox_assignments WHERE mailbox_id = m.id)
			FROM mailboxes m WHERE m.id = $1 FOR UPDATE`, ch.mailbox).Scan(&curSeq, &curState, &curVersion); err != nil {
			return err
		}
		inFlight := func(seq string) (int, error) {
			var n int
			err := tx.QueryRow(ctx, `SELECT count(*) FROM enrollments WHERE mailbox_id = $1 AND sequence_id = $2 AND status = 'active'`, ch.mailbox, seq).Scan(&n)
			return n, err
		}
		c := allocation.Change{MailboxID: ch.mailbox, Reason: "Undone by request."}
		switch ch.action {
		case allocation.Assign, allocation.Move:
			if curSeq == nil || *curSeq != deref(ch.to) {
				return allocation.ErrConflict
			}
			n, err := inFlight(*curSeq)
			if err != nil {
				return err
			}
			if n > 0 {
				return errInvalid(fmt.Sprintf("it has already started writing to %d lead%s in this sequence, and their follow-ups must come from it", n, plural(n)))
			}
			c.From = *curSeq
			if curVersion != nil {
				c.BaseVersion = *curVersion
			}
			c.Action = allocation.Release
			if ch.action == allocation.Move {
				c.Action, c.To = allocation.Move, deref(ch.from)
			}
		case allocation.Release:
			if curSeq != nil {
				return allocation.ErrConflict
			}
			c.Action, c.To = allocation.Assign, deref(ch.from)
		case allocation.Drain:
			if curSeq == nil || *curSeq != deref(ch.from) || deref(curState) != "draining" {
				return allocation.ErrConflict
			}
			// Back to active where it was: no new rules apply, so update directly.
			if _, err := tx.Exec(ctx, `UPDATE mailbox_assignments SET state = 'active', next_sequence_id = NULL, version = version + 1, updated_at = now() WHERE mailbox_id = $1`, ch.mailbox); err != nil {
				return err
			}
			done = &allocation.Change{MailboxID: ch.mailbox, Action: "undrain", From: *curSeq, To: *curSeq, Reason: c.Reason}
			if _, err := tx.Exec(ctx, `INSERT INTO allocation_changes (workspace_id, mailbox_id, action, from_sequence_id, to_sequence_id, source, reason, actor_id)
				VALUES ($1, $2, 'undrain', $3, $3, 'undo', $4, $5)`, a.WorkspaceID, ch.mailbox, *curSeq, c.Reason, a.ActorID); err != nil {
				return err
			}
		default:
			return errInvalid("this change cannot be undone")
		}
		if done == nil {
			if done, err = allocation.ApplyChange(ctx, tx, a.WorkspaceID, c, "undo", a.ActorID, &a.ActorType); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE allocation_changes SET undone_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		// Undoing the allocator's choice tells it not to make it again.
		if ch.source == "automatic" && ch.to != nil && (ch.action == allocation.Assign || ch.action == allocation.Move) {
			if _, err := tx.Exec(ctx, `INSERT INTO allocation_declines (workspace_id, mailbox_id, sequence_id, declined_by) VALUES ($1, $2, $3, $4) ON CONFLICT DO NOTHING`,
				a.WorkspaceID, ch.mailbox, *ch.to, a.ActorID); err != nil {
				return err
			}
		}
		if err := record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.allocation.undone", "mailbox", ch.mailbox, map[string]any{"change_id": id}); err != nil {
			return err
		}
		return allocation.Request(ctx, tx, a.WorkspaceID)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": viewChanges([]allocation.Change{*done})[0]})
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// ---- Manual assign, release and lock ----

type mailboxResult struct {
	MailboxID string `json:"mailbox_id"`
	Result    string `json:"result"` // assigned, moved, draining, released, unchanged, refused
	Message   string `json:"message"`
}

func (s *Server) assignMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		MailboxIDs []string `json:"mailbox_ids"`
		SequenceID string   `json:"sequence_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.MailboxIDs) == 0 || len(req.MailboxIDs) > 5000 || !allUUIDs(req.MailboxIDs) || !validUUID(req.SequenceID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give 1 to 5000 mailbox_ids and a sequence_id")
		return
	}
	ctx := allocation.WithRequestID(r.Context(), RequestID(r.Context()))
	var results []mailboxResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var name, status string
		var team *string
		err := tx.QueryRow(ctx, `SELECT name, status, team_id::text FROM sequences WHERE id = $1 AND workspace_id = $2`, req.SequenceID, a.WorkspaceID).Scan(&name, &status, &team)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("sequence not found")
		}
		if err != nil {
			return err
		}
		if status == "archived" {
			return errInvalid("the sequence is archived")
		}
		for _, id := range req.MailboxIDs {
			res, err := s.assignOne(ctx, tx, a, id, req.SequenceID, name, deref(team))
			if err != nil {
				return err
			}
			results = append(results, res)
		}
		return allocation.Request(ctx, tx, a.WorkspaceID)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": results})
}

// assignOne puts one mailbox on a sequence by hand. Soft preferences do
// not apply to people, but locks and in-flight follow-ups do.
func (s *Server) assignOne(ctx context.Context, tx pgx.Tx, a *access, id, seq, seqName, seqTeam string) (mailboxResult, error) {
	res := mailboxResult{MailboxID: id}
	var lockKind, lockSeq, lockTeam, curSeq, curState, notReady, otherProject, otherBrand *string
	var version *int
	err := tx.QueryRow(ctx, `
		SELECT l.kind, l.sequence_id::text, l.team_id::text, x.sequence_id::text, x.state, x.version,
		       CASE WHEN m.status <> 'active' THEN 'the mailbox is ' || m.status END,
		       -- A project's mailbox serves only that project's sequences.
		       (SELECT p.name FROM projects p WHERE p.id = m.project_id
		        AND m.project_id IS DISTINCT FROM (SELECT project_id FROM sequences WHERE id = $3)),
		       -- And a brand's mailbox only that brand's.
		       (SELECT b.name FROM brands b WHERE b.id = m.brand_id
		        AND m.brand_id IS DISTINCT FROM (SELECT brand_id FROM sequences WHERE id = $3))
		FROM mailboxes m
		LEFT JOIN mailbox_locks l ON l.mailbox_id = m.id AND (l.until IS NULL OR l.until > now())
		LEFT JOIN mailbox_assignments x ON x.mailbox_id = m.id
		WHERE m.id = $1 AND m.workspace_id = $2 FOR UPDATE OF m`, id, a.WorkspaceID, seq).
		Scan(&lockKind, &lockSeq, &lockTeam, &curSeq, &curState, &version, &notReady, &otherProject, &otherBrand)
	if errors.Is(err, pgx.ErrNoRows) {
		res.Result, res.Message = "refused", "mailbox not found"
		return res, nil
	}
	if err != nil {
		return res, err
	}
	switch {
	case otherProject != nil:
		res.Result, res.Message = "refused", "It belongs to the project "+*otherProject+". Move it to this sequence's project, or make it shared, first."
		return res, nil
	case otherBrand != nil:
		res.Result, res.Message = "refused", "It belongs to the brand "+*otherBrand+". Move it to this sequence's brand, or make it shared, first."
		return res, nil
	case lockKind != nil && *lockKind == allocation.LockSequence && *lockSeq != seq:
		res.Result, res.Message = "refused", "It is pinned to another sequence. Remove the lock first."
		return res, nil
	case lockKind != nil && *lockKind == allocation.LockTeam && *lockTeam != seqTeam:
		res.Result, res.Message = "refused", "It is reserved for another team. Remove the lock first."
		return res, nil
	case curSeq != nil && *curSeq == seq && *curState == "active":
		res.Result, res.Message = "unchanged", "Already sending for "+seqName+"."
		return res, nil
	case curSeq != nil && *curSeq == seq:
		// Draining away from this sequence: keep it here after all.
		if _, err := tx.Exec(ctx, `UPDATE mailbox_assignments SET state = 'active', next_sequence_id = NULL, source = 'manual', version = version + 1, updated_at = now() WHERE mailbox_id = $1`, id); err != nil {
			return res, err
		}
		_, err := tx.Exec(ctx, `INSERT INTO allocation_changes (workspace_id, mailbox_id, action, from_sequence_id, to_sequence_id, source, reason, actor_id)
			VALUES ($1, $2, 'undrain', $3, $3, 'manual', 'Kept on the sequence by hand.', $4)`, a.WorkspaceID, id, seq, a.ActorID)
		res.Result, res.Message = "assigned", "Kept on "+seqName+"."
		return res, err
	}
	link, refuse, err := route(ctx, tx, id, seq)
	if err != nil {
		return res, err
	}
	if refuse != "" {
		res.Result, res.Message = "refused", refuse
		return res, nil
	}
	c := allocation.Change{MailboxID: id, Action: allocation.Assign, To: seq, LinkID: link, Reason: "Assigned by hand."}
	if curSeq != nil {
		c.Action, c.From, c.BaseVersion = allocation.Move, *curSeq, *version
		if *curState == "draining" {
			// Already finishing elsewhere: change where it goes next.
			_, err := tx.Exec(ctx, `UPDATE mailbox_assignments SET next_sequence_id = $2, version = version + 1, updated_at = now() WHERE mailbox_id = $1`, id, seq)
			res.Result, res.Message = "draining", "Joins "+seqName+" once it finishes its follow-ups."
			return res, err
		}
	}
	done, err := allocation.ApplyChange(ctx, tx, a.WorkspaceID, c, "manual", a.ActorID, &a.ActorType)
	if err != nil {
		return res, err
	}
	switch done.Action {
	case allocation.Assign:
		res.Result, res.Message = "assigned", "Now sending for "+seqName+"."
		if link != "" {
			res.Message = "Added to " + seqName + "; waiting for the platform to confirm."
		}
	case allocation.Move:
		res.Result, res.Message = "moved", "Moved to "+seqName+"."
	case allocation.Drain:
		res.Result, res.Message = "draining", done.Reason+" Then it joins "+seqName+"."
	}
	if notReady != nil {
		res.Message += " It won't send until it is ready: " + *notReady + "."
	}
	return res, nil
}

func (s *Server) releaseMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		MailboxIDs []string `json:"mailbox_ids"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.MailboxIDs) == 0 || len(req.MailboxIDs) > 5000 || !allUUIDs(req.MailboxIDs) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give 1 to 5000 mailbox_ids")
		return
	}
	ctx := allocation.WithRequestID(r.Context(), RequestID(r.Context()))
	var results []mailboxResult
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for _, id := range req.MailboxIDs {
			res := mailboxResult{MailboxID: id}
			var lockKind, curSeq, curState *string
			var version *int
			err := tx.QueryRow(ctx, `
				SELECT l.kind, x.sequence_id::text, x.state, x.version FROM mailboxes m
				LEFT JOIN mailbox_locks l ON l.mailbox_id = m.id AND (l.until IS NULL OR l.until > now())
				LEFT JOIN mailbox_assignments x ON x.mailbox_id = m.id
				WHERE m.id = $1 AND m.workspace_id = $2 FOR UPDATE OF m`, id, a.WorkspaceID).Scan(&lockKind, &curSeq, &curState, &version)
			switch {
			case errors.Is(err, pgx.ErrNoRows):
				res.Result, res.Message = "refused", "mailbox not found"
			case err != nil:
				return err
			case curSeq == nil:
				res.Result, res.Message = "unchanged", "Not allocated to a sequence."
			case lockKind != nil && *lockKind == allocation.LockSequence:
				res.Result, res.Message = "refused", "It is pinned to its sequence. Remove the lock first."
			case *curState == "draining":
				if _, err := tx.Exec(ctx, `UPDATE mailbox_assignments SET next_sequence_id = NULL, version = version + 1 WHERE mailbox_id = $1`, id); err != nil {
					return err
				}
				res.Result, res.Message = "draining", "Already finishing its follow-ups; it will be free afterwards."
			default:
				done, err := allocation.ApplyChange(ctx, tx, a.WorkspaceID,
					allocation.Change{MailboxID: id, Action: allocation.Release, From: *curSeq, BaseVersion: *version, Reason: "Released by hand."},
					"manual", a.ActorID, &a.ActorType)
				if err != nil {
					return err
				}
				if done.Action == allocation.Drain {
					res.Result, res.Message = "draining", done.Reason
				} else {
					res.Result, res.Message = "released", "Free for other sequences."
				}
			}
			results = append(results, res)
		}
		return allocation.Request(ctx, tx, a.WorkspaceID)
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": results})
}

// lockMailboxes sets or clears one lock on each mailbox. Pinning a
// mailbox to a sequence also puts it there, so "always use this inbox
// here" takes effect at once.
func (s *Server) lockMailboxes(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		MailboxIDs []string   `json:"mailbox_ids"`
		Kind       *string    `json:"kind"` // null clears the lock
		SequenceID *string    `json:"sequence_id"`
		TeamID     *string    `json:"team_id"`
		Until      *time.Time `json:"until"`
		Note       string     `json:"note"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.MailboxIDs) == 0 || len(req.MailboxIDs) > 5000 || !allUUIDs(req.MailboxIDs) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give 1 to 5000 mailbox_ids")
		return
	}
	if req.Kind != nil {
		msg := ""
		switch *req.Kind {
		case allocation.LockSequence:
			if req.SequenceID == nil || !validUUID(*req.SequenceID) || req.TeamID != nil {
				msg = "a sequence lock needs sequence_id"
			}
		case allocation.LockTeam:
			if req.TeamID == nil || !validUUID(*req.TeamID) || req.SequenceID != nil {
				msg = "a team lock needs team_id"
			}
		case allocation.LockManualOnly:
			if req.TeamID != nil || req.SequenceID != nil {
				msg = "a manual_only lock takes no sequence_id or team_id"
			}
		default:
			msg = "kind is sequence, team, manual_only, or null to remove the lock"
		}
		if req.Until != nil && !req.Until.After(time.Now()) {
			msg = "until must be in the future"
		}
		if len(req.Note) > 500 {
			msg = "note must be at most 500 characters"
		}
		if msg != "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
			return
		}
	}
	ctx := allocation.WithRequestID(r.Context(), RequestID(r.Context()))
	var results []mailboxResult
	var updated int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var seqName, seqTeam string
		if req.SequenceID != nil {
			var team *string
			err := tx.QueryRow(ctx, `SELECT name, team_id::text FROM sequences WHERE id = $1 AND workspace_id = $2 AND status <> 'archived'`, *req.SequenceID, a.WorkspaceID).Scan(&seqName, &team)
			if errors.Is(err, pgx.ErrNoRows) {
				return errInvalid("sequence not found")
			}
			if err != nil {
				return err
			}
			seqTeam = deref(team)
		}
		if req.TeamID != nil {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM teams WHERE id = $1 AND workspace_id = $2)`, *req.TeamID, a.WorkspaceID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("team not found")
			}
		}
		if req.Kind == nil {
			tag, err := tx.Exec(ctx, `DELETE FROM mailbox_locks WHERE workspace_id = $1 AND mailbox_id = ANY($2::uuid[])`, a.WorkspaceID, req.MailboxIDs)
			if err != nil {
				return err
			}
			updated = tag.RowsAffected()
		} else {
			tag, err := tx.Exec(ctx, `
				INSERT INTO mailbox_locks (mailbox_id, workspace_id, kind, sequence_id, team_id, until, note, created_by)
				SELECT m.id, m.workspace_id, $3, $4, $5, $6, $7, $8 FROM mailboxes m WHERE m.workspace_id = $1 AND m.id = ANY($2::uuid[])
				ON CONFLICT (mailbox_id) DO UPDATE SET kind = EXCLUDED.kind, sequence_id = EXCLUDED.sequence_id, team_id = EXCLUDED.team_id,
					until = EXCLUDED.until, note = EXCLUDED.note, created_by = EXCLUDED.created_by, created_at = now()`,
				a.WorkspaceID, req.MailboxIDs, *req.Kind, req.SequenceID, req.TeamID, req.Until, req.Note, a.ActorID)
			if err != nil {
				return err
			}
			updated = tag.RowsAffected()
			if *req.Kind == allocation.LockSequence {
				for _, id := range req.MailboxIDs {
					res, err := s.assignOne(ctx, tx, a, id, *req.SequenceID, seqName, seqTeam)
					if err != nil {
						return err
					}
					results = append(results, res)
				}
			}
		}
		if err := allocation.Request(ctx, tx, a.WorkspaceID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.lock_set", "workspace", a.WorkspaceID, map[string]any{
			"mailboxes": updated, "kind": req.Kind, "sequence_id": req.SequenceID, "team_id": req.TeamID, "until": req.Until})
	})
	if s.allocError(w, r, err) {
		return
	}
	if results == nil {
		results = []mailboxResult{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "assignments": results})
}

// ---- One sequence's allocation ----

type assignedView struct {
	MailboxID   string    `json:"mailbox_id"`
	Email       string    `json:"email"`
	SenderName  *string   `json:"sender_name"`
	State       string    `json:"state"`
	NextSeqID   *string   `json:"next_sequence_id"`
	Source      string    `json:"source"`
	Reason      string    `json:"reason"`
	DailyLimit  int       `json:"daily_limit"`
	InFlight    int       `json:"in_flight"`
	LockKind    *string   `json:"lock_kind"`
	AssignedAt  time.Time `json:"assigned_at"`
	MailboxStat string    `json:"mailbox_status"`
	// The platform it sends through for this sequence, or null for Outflare.
	Platform *string `json:"platform"`
	// False while a platform has not yet shown the mailbox on the campaign.
	Confirmed  bool       `json:"confirmed"`
	DrainUntil *time.Time `json:"drain_until"`
}

func (s *Server) sequenceAllocation(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	var out struct {
		SequenceID string              `json:"sequence_id"`
		Mode       string              `json:"mode"`
		Needed     *int                `json:"needed_daily"`
		Active     int                 `json:"active_daily"`
		Blocking   json.RawMessage     `json:"blocking"`
		Note       string              `json:"note"`
		PlannedAt  *time.Time          `json:"planned_at"`
		Mailboxes  []assignedView      `json:"mailboxes"`
		Incoming   []assignedView      `json:"incoming"`
		Suggested  []suggestionView    `json:"suggestions"`
		Changes    []allocChangeView   `json:"changes"`
		Orphaned   int                 `json:"orphaned_follow_ups"`
		Links      []linkView          `json:"links"`
		_          allocation.Blocking `json:"-"`
	}
	out.SequenceID = id
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(q.allocation_mode, t.allocation_mode, w.allocation_mode), st.needed_daily, COALESCE(st.active_daily, 0),
		       COALESCE(st.blocking, '[]'), COALESCE(st.note, ''), st.planned_at,
		       (SELECT count(*) FROM enrollments e WHERE e.sequence_id = q.id AND e.status = 'active' AND (e.mailbox_id IS NULL OR e.mailbox_id IN (SELECT id FROM mailboxes WHERE lifecycle_stage = 'retired')) AND e.next_step > 0 AND NOT e.handoff)::int
		FROM sequences q JOIN workspaces w ON w.id = q.workspace_id LEFT JOIN teams t ON t.id = q.team_id
		LEFT JOIN allocation_status st ON st.sequence_id = q.id
		WHERE q.id = $1 AND q.workspace_id = $2`, id, a.WorkspaceID).
		Scan(&out.Mode, &out.Needed, &out.Active, &out.Blocking, &out.Note, &out.PlannedAt, &out.Orphaned)
	if errors.Is(err, pgx.ErrNoRows) {
		s.allocError(w, r, errNotFound)
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	load := func(where string) ([]assignedView, error) {
		rows, err := s.pool.Query(ctx, `
			SELECT m.id, m.email, sn.name, x.state, x.next_sequence_id, x.source, x.reason, m.daily_limit,
			       (SELECT count(*) FROM enrollments e WHERE e.mailbox_id = m.id AND e.sequence_id = x.sequence_id AND e.status = 'active')::int,
			       l.kind, x.created_at, m.status, i.name, x.link_id IS NULL OR x.confirmed_at IS NOT NULL, x.drain_until
			FROM mailbox_assignments x JOIN mailboxes m ON m.id = x.mailbox_id
			LEFT JOIN campaign_links cl ON cl.id = x.link_id LEFT JOIN integrations i ON i.id = cl.integration_id
			LEFT JOIN senders sn ON sn.id = m.sender_id AND m.sender_confirmed_at IS NOT NULL
			LEFT JOIN mailbox_locks l ON l.mailbox_id = m.id AND (l.until IS NULL OR l.until > now())
			WHERE x.workspace_id = $1 AND `+where+` ORDER BY x.state, m.email`, a.WorkspaceID, id)
		if err != nil {
			return nil, err
		}
		list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[assignedView])
		if list == nil {
			list = []assignedView{}
		}
		return list, err
	}
	if out.Mailboxes, err = load(`x.sequence_id = $2`); err == nil {
		if out.Incoming, err = load(`x.next_sequence_id = $2`); err == nil {
			if out.Suggested, err = s.loadSuggestions(ctx, a.WorkspaceID, &id); err == nil {
				if out.Changes, err = s.loadChanges(ctx, a.WorkspaceID, &id, nil, 50, 0); err == nil {
					var links map[string][]linkView
					links, err = s.loadLinks(ctx, a.WorkspaceID, []string{id})
					out.Links = links[id]
					if out.Links == nil {
						out.Links = []linkView{}
					}
				}
			}
		}
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// resolveOrphans decides what happens to follow-ups whose mailbox is gone:
// cancel them, or send them from another of the sequence's mailboxes as a
// fresh email (not a reply, since the new sender never wrote before).
func (s *Server) resolveOrphans(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Action string `json:"action"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Action != "cancel" && req.Action != "handoff" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "action is cancel or handoff")
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	var n int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sequences WHERE id = $1 AND workspace_id = $2)`, id, a.WorkspaceID).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return errNotFound
		}
		sql := `UPDATE enrollments SET status = 'stopped', status_reason = 'sender_mailbox_gone', next_send_at = NULL, updated_at = now()`
		if req.Action == "handoff" {
			sql = `UPDATE enrollments SET handoff = true, mailbox_id = NULL, last_message_id = NULL, references_ids = '', status_reason = NULL, next_send_at = now(), updated_at = now()`
		}
		tag, err := tx.Exec(ctx, sql+` WHERE sequence_id = $1 AND status = 'active' AND (mailbox_id IS NULL OR mailbox_id IN (SELECT id FROM mailboxes WHERE lifecycle_stage = 'retired')) AND next_step > 0 AND NOT handoff`, id)
		if err != nil {
			return err
		}
		n = tag.RowsAffected()
		return record(ctx, tx, &a.principal, a.WorkspaceID, "sequence.orphaned_follow_ups_"+map[string]string{"cancel": "cancelled", "handoff": "handed_off"}[req.Action],
			"sequence", id, map[string]any{"leads": n})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": n})
}

// ---- Needs attention ----

// allocationAttention is everything about allocation that needs a person:
// approvals, shortages, follow-ups without a mailbox, unconfirmed senders
// and platform accounts that are out of date, unreviewed or in conflict.
func (s *Server) allocationAttention(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	suggestions, err := s.loadSuggestions(ctx, a.WorkspaceID, nil)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	approvals := []suggestionView{}
	reviews := 0
	for _, g := range suggestions {
		if g.Needs == "review" {
			reviews++
		} else {
			approvals = append(approvals, g)
		}
	}
	type shortage struct {
		SequenceID   string          `json:"sequence_id"`
		SequenceName string          `json:"sequence_name"`
		Needed       int             `json:"needed_daily"`
		Active       int             `json:"active_daily"`
		Blocking     json.RawMessage `json:"blocking"`
		Note         string          `json:"note"`
	}
	rows, err := s.pool.Query(ctx, `
		SELECT q.id, q.name, st.needed_daily, st.active_daily, st.blocking, st.note
		FROM allocation_status st JOIN sequences q ON q.id = st.sequence_id
		WHERE st.workspace_id = $1 AND q.status = 'active' AND st.needed_daily > st.active_daily
		ORDER BY q.priority DESC, q.name`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	shortages, err := pgx.CollectRows(rows, pgx.RowToStructByPos[shortage])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	type orphan struct {
		SequenceID   string `json:"sequence_id"`
		SequenceName string `json:"sequence_name"`
		Leads        int    `json:"leads"`
	}
	rows, err = s.pool.Query(ctx, `
		SELECT q.id, q.name, count(*)::int FROM enrollments e JOIN sequences q ON q.id = e.sequence_id
		WHERE e.workspace_id = $1 AND e.status = 'active' AND (e.mailbox_id IS NULL OR e.mailbox_id IN (SELECT id FROM mailboxes WHERE lifecycle_stage = 'retired')) AND e.next_step > 0 AND NOT e.handoff
		GROUP BY q.id, q.name ORDER BY q.name`, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	orphans, err := pgx.CollectRows(rows, pgx.RowToStructByPos[orphan])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var unconfirmed int
	if err := s.pool.QueryRow(ctx, `
		SELECT CASE WHEN EXISTS (SELECT 1 FROM teams WHERE workspace_id = $1)
		       THEN (SELECT count(*) FROM mailboxes WHERE workspace_id = $1 AND sender_confirmed_at IS NULL) ELSE 0 END`,
		a.WorkspaceID).Scan(&unconfirmed); err != nil {
		s.internal(w, r, err)
		return
	}
	accounts, err := s.loadPlatforms(ctx, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	platforms := []platformView{}
	for _, p := range accounts {
		if p.Stale || p.AwaitingReview || p.Conflicts > 0 {
			platforms = append(platforms, p)
		}
	}
	if shortages == nil {
		shortages = []shortage{}
	}
	if orphans == nil {
		orphans = []orphan{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"approvals": approvals, "suggestions_to_review": reviews, "shortages": shortages,
		"orphaned_follow_ups": orphans, "unconfirmed_senders": unconfirmed, "platforms": platforms,
	})
}
