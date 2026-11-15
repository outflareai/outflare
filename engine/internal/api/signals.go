package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/journeys"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/signals"
)

// Signals (docs/signals.md): what someone should act on about a person
// or a company, tracked from raised to closed with an owner, a due date
// and an outcome. The open ones are the brand's "Needs attention" queue.
// internal/signals raises them from events and escalates late ones; these
// handlers list them, raise them by hand and move them along.

func (s *Server) routesSignals(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	base := "/v1/brands/{brand}/signals"
	mux.HandleFunc("GET "+base, s.onBrand(read, s.listSignals))
	mux.HandleFunc("POST "+base, s.onBrand(manage, s.raiseSignal))
	mux.HandleFunc("PATCH "+base+"/{id}", s.onBrand(manage, s.updateSignal))
	types := "/v1/brands/{brand}/signal-types"
	mux.HandleFunc("GET "+types, s.onBrand(read, s.listSignalTypes))
	mux.HandleFunc("PUT "+types+"/{key}", s.onBrand(manage, s.putSignalType))
	mux.HandleFunc("DELETE "+types+"/{key}", s.onBrand(manage, s.archiveSignalType))
}

type signalView struct {
	ID           string         `json:"id"`
	RecordID     string         `json:"record_id"`
	RecordType   string         `json:"record_type"`
	RecordName   string         `json:"record_name"`
	Type         string         `json:"type"`
	TypeLabel    string         `json:"type_label"`
	Title        string         `json:"title"`
	Detail       string         `json:"detail"`
	Status       string         `json:"status"`
	Outcome      *string        `json:"outcome"`
	OwnerID      *string        `json:"owner_id"`
	OwnerName    *string        `json:"owner_name"`
	DueAt        *time.Time     `json:"due_at"`
	SnoozedUntil *time.Time     `json:"snoozed_until"`
	Source       string         `json:"source"`
	EventID      *string        `json:"event_id"`
	Data         map[string]any `json:"data"`
	EscalatedAt  *time.Time     `json:"escalated_at"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	ClosedAt     *time.Time     `json:"closed_at"`
	// Late says an open signal is past its due date.
	Late bool `json:"late"`
}

const signalSelect = `SELECT g.id::text, g.record_id::text, r.record_type, r.name, g.type, COALESCE(t.label, g.type), g.title, g.detail, g.status, g.outcome,
		g.owner_id::text, u.name, g.due_at, g.snoozed_until, g.source, g.event_id::text, g.data, g.escalated_at, g.created_at, g.updated_at, g.closed_at,
		g.status IN ('open', 'accepted') AND g.due_at < now()
	FROM signals g JOIN records r ON r.id = g.record_id
	LEFT JOIN signal_types t ON t.brand_id = g.brand_id AND t.key = g.type
	LEFT JOIN users u ON u.id = g.owner_id`

func scanSignal(row pgx.Row) (signalView, error) {
	var v signalView
	err := row.Scan(&v.ID, &v.RecordID, &v.RecordType, &v.RecordName, &v.Type, &v.TypeLabel, &v.Title, &v.Detail, &v.Status, &v.Outcome,
		&v.OwnerID, &v.OwnerName, &v.DueAt, &v.SnoozedUntil, &v.Source, &v.EventID, &v.Data, &v.EscalatedAt, &v.CreatedAt, &v.UpdatedAt, &v.ClosedAt, &v.Late)
	if v.Data == nil {
		v.Data = map[string]any{}
	}
	return v, err
}

// listSignals is the queue: open signals first by due date (status=open,
// the default, means open and accepted, without the snoozed ones), or the
// closed ones newest first. owner=me, record and type narrow it.
func (s *Server) listSignals(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	where := []string{"g.workspace_id = $1", "g.brand_id = $2"}
	args := []any{a.WorkspaceID, a.Brand}
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, strings.ReplaceAll(cond, "?", "$"+strconv.Itoa(len(args))))
	}
	order := "g.due_at NULLS LAST, g.created_at"
	switch q.Get("status") {
	case "", "open":
		where = append(where, "g.status IN ('open', 'accepted')", "(g.snoozed_until IS NULL OR g.snoozed_until < now())")
	case "snoozed":
		where = append(where, "g.status IN ('open', 'accepted')", "g.snoozed_until >= now()")
	case "closed":
		where = append(where, "g.status IN ('done', 'dismissed')")
		order = "g.closed_at DESC"
	case "all":
		order = "g.created_at DESC"
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is open, snoozed, closed or all")
		return
	}
	switch owner := q.Get("owner"); {
	case owner == "":
	case owner == "me":
		add("g.owner_id::text = ?", a.personID())
	case owner == "none":
		where = append(where, "g.owner_id IS NULL")
	case validUUID(owner):
		add("g.owner_id = ?", owner)
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "owner is me, none or a user's id")
		return
	}
	if rec := q.Get("record"); rec != "" {
		if !validUUID(rec) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record is a record's id")
			return
		}
		add("g.record_id = ?", rec)
	}
	if t := q.Get("type"); t != "" {
		add("g.type = ?", t)
	}
	// The queue is short by design (five open signals a record, and
	// closed ones newest first), so it is one page.
	const limit = 500
	ctx := r.Context()
	out := struct {
		Data []signalView `json:"data"`
		// Counts are the brand's open signals, whatever the filters: all
		// of them, the caller's, the late ones and the ones with no owner.
		Counts struct {
			Open    int `json:"open"`
			Mine    int `json:"mine"`
			Late    int `json:"late"`
			NoOwner int `json:"no_owner"`
		} `json:"counts"`
	}{Data: []signalView{}}
	err := s.readTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, signalSelect+` WHERE `+strings.Join(where, " AND ")+` ORDER BY `+order+`, g.id LIMIT `+strconv.Itoa(limit), args...)
		if err != nil {
			return err
		}
		if out.Data, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (signalView, error) { return scanSignal(row) }); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE owner_id::text = $3), count(*) FILTER (WHERE due_at < now()),
				count(*) FILTER (WHERE owner_id IS NULL)
			FROM signals WHERE workspace_id = $1 AND brand_id = $2 AND status IN ('open', 'accepted') AND (snoozed_until IS NULL OR snoozed_until < now())`,
			a.WorkspaceID, a.Brand, a.personID()).Scan(&out.Counts.Open, &out.Counts.Mine, &out.Counts.Late, &out.Counts.NoOwner)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) findSignal(w http.ResponseWriter, r *http.Request, a *access, id string) (signalView, bool) {
	v, err := scanSignal(s.pool.QueryRow(r.Context(), signalSelect+` WHERE g.workspace_id = $1 AND g.brand_id = $2 AND g.id = $3`, a.WorkspaceID, a.Brand, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such signal")
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	return v, true
}

// raiseSignal raises a signal by hand, or from an agent: "flag this
// account for sales".
func (s *Server) raiseSignal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f struct {
		RecordID string     `json:"record_id"`
		Type     string     `json:"type"`
		Title    string     `json:"title"`
		Detail   string     `json:"detail"`
		OwnerID  *string    `json:"owner_id"`
		DueAt    *time.Time `json:"due_at"`
		// The conversation it was flagged from ("Flag for sales").
		Conversation *struct {
			Channel string `json:"channel"`
			Ref     string `json:"ref"`
		} `json:"conversation"`
	}
	if !decode(w, r, &f) {
		return
	}
	var data map[string]any
	if c := f.Conversation; c != nil {
		if !slices.Contains(signals.Channels, c.Channel) || c.Ref == "" || len(c.Ref) > 200 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "conversation is a channel ("+strings.Join(signals.Channels, ", ")+") and the conversation's id as ref")
			return
		}
		data = map[string]any{"channel": c.Channel, "ref": c.Ref}
	}
	f.Title, f.Detail = strings.TrimSpace(f.Title), strings.TrimSpace(f.Detail)
	switch {
	case !validUUID(f.RecordID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id is the person or company the signal is about")
		return
	case f.Type == "":
		f.Type = "other"
	}
	if len(f.Title) > 300 || len(f.Detail) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "title is at most 300 characters and detail at most 2,000")
		return
	}
	if f.OwnerID != nil && !validUUID(*f.OwnerID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "owner_id is a member's user id")
		return
	}
	source := "manual"
	if a.ActorType == "api_token" {
		source = "api"
	}
	ctx := r.Context()
	var id, outcome string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := signals.EnsureTypes(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var known bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM signal_types WHERE brand_id = $1 AND key = $2 AND archived_at IS NULL)`, a.Brand, f.Type).Scan(&known); err != nil {
			return err
		}
		if !known {
			return errInvalid("type is one of the brand's signal types, such as churn_risk or upsell (GET signal-types lists them)")
		}
		if f.OwnerID != nil {
			var member bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, a.WorkspaceID, *f.OwnerID).Scan(&member); err != nil {
				return err
			}
			if !member {
				return errInvalid("owner_id is not a member of this workspace")
			}
		}
		var err error
		id, outcome, err = signals.Raise(ctx, tx, signals.Signal{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, RecordID: f.RecordID, Type: f.Type,
			Title: f.Title, Detail: f.Detail, Source: source, OwnerID: f.OwnerID, DueAt: f.DueAt, CreatedBy: a.personID(), Data: data,
			ActorID: a.ActorID, ActorType: a.ActorType, UserID: a.personID(), RequestID: RequestID(ctx)})
		return err
	})
	var inv errInvalid
	switch {
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
		return
	case err != nil:
		s.internal(w, r, err)
		return
	}
	metrics.SignalRaised(source, outcome)
	switch outcome {
	case signals.NoRecord:
		writeError(w, r, http.StatusNotFound, "not_found", "no such person or company in this brand")
	case signals.Duplicate:
		writeError(w, r, http.StatusConflict, "conflict", "this record already has an open signal of this type")
	case signals.Capped:
		writeError(w, r, http.StatusConflict, "conflict", "this record already has "+strconv.Itoa(signals.MaxOpen)+" open signals: close one first")
	default:
		if v, ok := s.findSignal(w, r, a, id); ok {
			writeJSON(w, http.StatusCreated, v)
		}
	}
}

var signalOutcomes = []string{"won", "no_change", "not_useful", "duplicate"}

// updateSignal moves a signal along: accept it, hand it to someone else,
// change its due date, snooze it, close it as done or dismissed with an
// outcome, or reopen it.
func (s *Server) updateSignal(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such signal")
		return
	}
	var f struct {
		Status       *string             `json:"status"`
		Outcome      *string             `json:"outcome"`
		OwnerID      optional[string]    `json:"owner_id"`
		DueAt        *time.Time          `json:"due_at"`
		SnoozedUntil optional[time.Time] `json:"snoozed_until"`
		Title        *string             `json:"title"`
		Detail       *string             `json:"detail"`
	}
	if !decode(w, r, &f) {
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var status, typ, record string
		var outcome, owner *string
		err := tx.QueryRow(ctx, `SELECT status, outcome, owner_id::text, type, record_id::text FROM signals WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 FOR UPDATE`,
			a.WorkspaceID, a.Brand, id).Scan(&status, &outcome, &owner, &typ, &record)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		was, action := status, "signal.updated"
		wasOpen := status == "open" || status == "accepted"
		if f.Status != nil {
			if !slices.Contains([]string{"open", "accepted", "done", "dismissed"}, *f.Status) {
				return errInvalid("status is open, accepted, done or dismissed")
			}
			status = *f.Status
		}
		closed := status == "done" || status == "dismissed"
		if f.Outcome != nil {
			if !slices.Contains(signalOutcomes, *f.Outcome) {
				return errInvalid("outcome is " + strings.Join(signalOutcomes, ", "))
			}
			if !closed {
				return errInvalid("an outcome goes with status done or dismissed")
			}
			outcome = f.Outcome
		}
		switch {
		case closed && outcome == nil && status == "done":
			o := "no_change"
			outcome = &o
		case closed && outcome == nil:
			o := "not_useful"
			outcome = &o
		case !closed:
			outcome = nil
		}
		// Reopening is refused when another signal of the type took its
		// place meanwhile.
		if !closed && !wasOpen {
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM signals WHERE record_id = $1 AND type = $2 AND status IN ('open', 'accepted') AND id <> $3)`,
				record, typ, id).Scan(&taken); err != nil {
				return err
			}
			if taken {
				return errSignalConflict("this record has another open signal of this type")
			}
		}
		if f.OwnerID.Set {
			owner = f.OwnerID.Value
			if owner != nil {
				var member bool
				if !validUUID(*owner) {
					return errInvalid("owner_id is a member's user id, or null")
				}
				if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM memberships WHERE workspace_id = $1 AND user_id = $2)`, a.WorkspaceID, *owner).Scan(&member); err != nil {
					return err
				}
				if !member {
					return errInvalid("owner_id is not a member of this workspace")
				}
			}
		}
		// Accepting a signal nobody owns makes it the caller's.
		if status == "accepted" && was == "open" && owner == nil && a.personID() != "" {
			me := a.personID()
			owner = &me
		}
		if f.Title != nil && len(strings.TrimSpace(*f.Title)) > 300 || f.Detail != nil && len(strings.TrimSpace(*f.Detail)) > 2000 {
			return errInvalid("title is at most 300 characters and detail at most 2,000")
		}
		if _, err := tx.Exec(ctx, `UPDATE signals SET status = $2, outcome = $3, owner_id = $4, due_at = COALESCE($5, due_at),
				snoozed_until = CASE WHEN $6 THEN $7 ELSE snoozed_until END, title = COALESCE($8, title), detail = COALESCE($9, detail),
				closed_at = CASE WHEN $10 THEN COALESCE(closed_at, now()) END, closed_by = CASE WHEN $10 THEN COALESCE(closed_by, NULLIF($11, '')::uuid) END,
				updated_at = now()
			WHERE id = $1`, id, status, outcome, owner, f.DueAt, f.SnoozedUntil.Set, f.SnoozedUntil.Value, trimmedText(f.Title), trimmedText(f.Detail),
			closed, a.personID()); err != nil {
			return err
		}
		switch {
		case closed && wasOpen:
			action = "signal.closed"
		case status == "accepted" && was == "open":
			action = "signal.accepted"
		case !closed && !wasOpen:
			action = "signal.reopened"
		}
		data := map[string]any{"signal_id": id, "record_id": record, "type": typ, "status": status, "outcome": outcome, "owner_id": owner}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "signal", id, data, map[string]any{"status": status, "outcome": outcome})
	})
	if !s.signalApplied(w, r, err) {
		return
	}
	if v, ok := s.findSignal(w, r, a, id); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func trimmedText(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

// errSignalConflict is a refusal the caller can fix by changing something else
// first.
type errSignalConflict string

func (e errSignalConflict) Error() string { return string(e) }

func (s *Server) signalApplied(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	var con errSignalConflict
	switch {
	case err == nil:
		return true
	case errors.Is(err, errNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "no such signal")
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	case errors.As(err, &con):
		writeError(w, r, http.StatusConflict, "conflict", string(con))
	default:
		s.internal(w, r, err)
	}
	return false
}

type signalTypeView struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	Description string   `json:"description"`
	Events      []string `json:"events"`
	DueDays     int      `json:"due_days"`
	// How the type's signals ended in the last 90 days, so a brand can see
	// which kinds are worth raising.
	Open      int `json:"open"`
	Closed    int `json:"closed_90d"`
	Won       int `json:"won_90d"`
	NotUseful int `json:"not_useful_90d"`
}

func (s *Server) signalTypes(r *http.Request, a *access) ([]signalTypeView, error) {
	ctx := r.Context()
	var out []signalTypeView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := signals.EnsureTypes(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT t.key, t.label, t.description, t.events, t.due_days,
				count(g.id) FILTER (WHERE g.status IN ('open', 'accepted')),
				count(g.id) FILTER (WHERE g.closed_at > now() - interval '90 days'),
				count(g.id) FILTER (WHERE g.closed_at > now() - interval '90 days' AND g.outcome = 'won'),
				count(g.id) FILTER (WHERE g.closed_at > now() - interval '90 days' AND g.outcome = 'not_useful')
			FROM signal_types t LEFT JOIN signals g ON g.brand_id = t.brand_id AND g.type = t.key
			WHERE t.workspace_id = $1 AND t.brand_id = $2 AND t.archived_at IS NULL GROUP BY t.id ORDER BY t.key = 'other', t.label, t.key`, a.WorkspaceID, a.Brand)
		if err != nil {
			return err
		}
		out, err = pgx.CollectRows(rows, pgx.RowToStructByPos[signalTypeView])
		return err
	})
	if out == nil {
		out = []signalTypeView{}
	}
	return out, err
}

func (s *Server) listSignalTypes(w http.ResponseWriter, r *http.Request) {
	list, err := s.signalTypes(r, accessFrom(r.Context()))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// putSignalType adds a signal type or changes one: its label, the events
// that raise it and how long its owner has.
func (s *Server) putSignalType(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	key := r.PathValue("key")
	var f struct {
		Label       *string   `json:"label"`
		Description *string   `json:"description"`
		Events      *[]string `json:"events"`
		DueDays     *int      `json:"due_days"`
	}
	if !decode(w, r, &f) {
		return
	}
	switch {
	case !signals.ValidKey(key):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a signal type's key is lowercase letters, digits and underscores, such as churn_risk")
		return
	case f.Label != nil && (strings.TrimSpace(*f.Label) == "" || len(*f.Label) > 80):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "label is 1 to 80 characters")
		return
	case f.Description != nil && len(*f.Description) > 500:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "description is at most 500 characters")
		return
	case f.DueDays != nil && (*f.DueDays < 0 || *f.DueDays > 365):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "due_days is 0 to 365")
		return
	}
	if f.Events != nil {
		if len(*f.Events) > 20 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a signal type listens to at most 20 events")
			return
		}
		for _, e := range *f.Events {
			if !journeys.ValidEventName(e) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "events are event names such as account.churn_risk")
				return
			}
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := signals.EnsureTypes(ctx, tx, a.WorkspaceID, a.Brand); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM signal_types WHERE brand_id = $1 AND key = $2)`, a.Brand, key).Scan(&exists); err != nil {
			return err
		}
		if !exists && f.Label == nil {
			return errInvalid("a new signal type needs a label")
		}
		sql := `INSERT INTO signal_types (workspace_id, brand_id, key, label, description, events, due_days)
			VALUES ($1, $2, $3, $4, COALESCE($5, ''), COALESCE($6, '{}'::text[]), COALESCE($7, 2))`
		if exists {
			sql = `UPDATE signal_types SET label = COALESCE($4, label), description = COALESCE($5, description), events = COALESCE($6, events),
				due_days = COALESCE($7, due_days), archived_at = NULL, updated_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND key = $3`
		}
		if _, err := tx.Exec(ctx, sql, a.WorkspaceID, a.Brand, key, trimmedText(f.Label), f.Description, f.Events, f.DueDays); err != nil {
			return err
		}
		action := "signal_type.updated"
		if !exists {
			action = "signal_type.created"
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "signal_type", "", map[string]any{"key": key, "brand_id": a.Brand})
	})
	if !s.signalApplied(w, r, err) {
		return
	}
	s.listSignalTypes(w, r)
}

// archiveSignalType takes a type off the list. Its signals stay.
func (s *Server) archiveSignalType(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE signal_types SET archived_at = now(), updated_at = now() WHERE workspace_id = $1 AND brand_id = $2 AND key = $3 AND archived_at IS NULL`,
			a.WorkspaceID, a.Brand, r.PathValue("key"))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "signal_type.archived", "signal_type", "", map[string]any{"key": r.PathValue("key"), "brand_id": a.Brand})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such signal type")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.listSignalTypes(w, r)
}
