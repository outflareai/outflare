package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/rules"
)

// Rules: the rules engine of the customer data platform. A rule turns what
// happens to records (an event, a change to their fields, something that
// didn't happen in time, joining or leaving a smart list) into a business
// event of its own on their timeline, which journeys and webhooks act on.
// internal/rules evaluates rules; these handlers make and change them.

func (s *Server) routesRules(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/rules", s.inBrand(read, s.listRules))
	mux.HandleFunc("POST /v1/workspaces/{ws}/rules", s.inBrand(manage, s.createRule))
	mux.HandleFunc("GET /v1/workspaces/{ws}/rules/{id}", s.inBrand(read, s.getRule))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/rules/{id}", s.inBrand(manage, s.updateRule))
	mux.HandleFunc("POST /v1/workspaces/{ws}/rules/{id}/activate", s.inBrand(manage, s.activateRule))
	mux.HandleFunc("POST /v1/workspaces/{ws}/rules/{id}/pause", s.inBrand(manage, s.pauseRule))
	mux.HandleFunc("POST /v1/workspaces/{ws}/rules/{id}/archive", s.inBrand(manage, s.archiveRule))
	mux.HandleFunc("POST /v1/workspaces/{ws}/rules/{id}/simulate", s.inBrand(read, s.simulateRule))
	mux.HandleFunc("GET /v1/workspaces/{ws}/rules/{id}/firings", s.inBrand(read, s.listRuleFirings))
}

type ruleStats struct {
	Firings     int        `json:"firings"`
	Records     int        `json:"records"`
	LastFiredAt *time.Time `json:"last_fired_at"`
}

type ruleView struct {
	ID              string            `json:"id"`
	Name            string            `json:"name"`
	Description     string            `json:"description"`
	Status          string            `json:"status"`
	BrandID         *string           `json:"brand_id"`
	RecordType      string            `json:"record_type"`
	Trigger         rules.Trigger     `json:"trigger"`
	Conditions      []rules.Condition `json:"conditions"`
	Match           string            `json:"match"`
	EmitEvent       string            `json:"emit_event"`
	Fire            string            `json:"fire"`
	CooldownSeconds int64             `json:"cooldown_seconds"`
	ManualEdits     bool              `json:"manual_edits"`
	Version         int               `json:"version"`
	ActivatedAt     *time.Time        `json:"activated_at"`
	// How often it fired: only on a single rule.
	Stats     *ruleStats `json:"stats,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// engine is the rule as internal/rules reads it.
func (v ruleView) engine(ws string) rules.Rule {
	return rules.Rule{ID: v.ID, WorkspaceID: ws, BrandID: v.BrandID, Status: v.Status, RecordType: v.RecordType, Trigger: v.Trigger,
		Conditions: v.Conditions, Match: v.Match, Emit: v.EmitEvent, Fire: v.Fire, Cooldown: v.CooldownSeconds, ManualEdits: v.ManualEdits,
		Version: v.Version, ActivatedAt: v.ActivatedAt}
}

const ruleSelect = `SELECT id, name, description, status, brand_id, record_type, trigger, conditions, match, emit_event, fire,
	cooldown_seconds, manual_edits, version, activated_at, created_at, updated_at FROM rules WHERE workspace_id = $1`

func scanRuleView(row pgx.Row) (ruleView, error) {
	var v ruleView
	var trigger, conds []byte
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.Status, &v.BrandID, &v.RecordType, &trigger, &conds, &v.Match, &v.EmitEvent, &v.Fire,
		&v.CooldownSeconds, &v.ManualEdits, &v.Version, &v.ActivatedAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Conditions = []rules.Condition{}
	_ = json.Unmarshal(trigger, &v.Trigger)
	_ = json.Unmarshal(conds, &v.Conditions)
	return v, nil
}

var ruleStatuses = []string{"draft", "active", "paused", "archived"}

func (s *Server) listRules(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	status := r.URL.Query().Get("status")
	if status != "" && !slices.Contains(ruleStatuses, status) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "status is one of "+strings.Join(ruleStatuses, ", "))
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	// Archived rules only when asked for.
	args := []any{a.WorkspaceID, status}
	rows, err := s.pool.Query(r.Context(), ruleSelect+` AND (($2 = '' AND status <> 'archived') OR status = $2)`+sel.sql("brand_id", &args)+`
		ORDER BY created_at DESC LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ruleView, error) { return scanRuleView(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []ruleView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findRule loads one rule the caller may see, or writes a 404.
func (s *Server) findRule(w http.ResponseWriter, r *http.Request, a *access, id string) (*ruleView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "rule not found")
		return nil, false
	}
	v, err := scanRuleView(s.pool.QueryRow(r.Context(), ruleSelect+` AND id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(v.BrandID)) {
		writeError(w, r, http.StatusNotFound, "not_found", "rule not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) getRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var st ruleStats
	if err := s.pool.QueryRow(r.Context(), `SELECT count(*)::int, count(DISTINCT record_id)::int, max(fired_at) FROM rule_firings
		WHERE workspace_id = $1 AND rule_id = $2`, a.WorkspaceID, v.ID).Scan(&st.Firings, &st.Records, &st.LastFiredAt); err != nil {
		s.internal(w, r, err)
		return
	}
	v.Stats = &st
	writeJSON(w, http.StatusOK, v)
}

type ruleFields struct {
	Name            *string            `json:"name"`
	Description     *string            `json:"description"`
	BrandID         optional[string]   `json:"brand_id"`
	RecordType      *string            `json:"record_type"`
	Trigger         *rules.Trigger     `json:"trigger"`
	Conditions      *[]rules.Condition `json:"conditions"`
	Match           *string            `json:"match"`
	EmitEvent       *string            `json:"emit_event"`
	Fire            *string            `json:"fire"`
	CooldownSeconds *int64             `json:"cooldown_seconds"`
	ManualEdits     *bool              `json:"manual_edits"`
}

// onlyLabels reports whether f changes nothing but the name and
// description, which can change while a rule is active.
func (f ruleFields) onlyLabels() bool {
	return !f.BrandID.Set && f.RecordType == nil && f.Trigger == nil && f.Conditions == nil && f.Match == nil && f.EmitEvent == nil &&
		f.Fire == nil && f.CooldownSeconds == nil && f.ManualEdits == nil
}

// applyRule checks f and applies it to v.
func (s *Server) applyRule(r *http.Request, a *access, f ruleFields, v *ruleView) error {
	ctx := r.Context()
	if f.Name != nil {
		n := strings.TrimSpace(*f.Name)
		if n == "" || len(n) > 200 {
			return errInvalid("name must be 1 to 200 characters")
		}
		v.Name = n
	}
	if f.Description != nil {
		if len(*f.Description) > 2000 {
			return errInvalid("description must be at most 2,000 characters")
		}
		v.Description = *f.Description
	}
	if f.BrandID.Set {
		v.BrandID = nil
		if f.BrandID.Value != nil {
			id, err := s.brandIDFor(ctx, a.WorkspaceID, *f.BrandID.Value)
			if err != nil {
				return errInvalid("brand_id must be one of the workspace's brands, or null for every brand")
			}
			v.BrandID = &id
		}
	}
	if f.RecordType != nil {
		v.RecordType = *f.RecordType
	}
	if f.Trigger != nil {
		v.Trigger = *f.Trigger
	}
	if f.Conditions != nil {
		v.Conditions = *f.Conditions
	}
	if f.Match != nil {
		v.Match = *f.Match
	}
	if f.EmitEvent != nil {
		v.EmitEvent = *f.EmitEvent
	}
	if f.Fire != nil {
		v.Fire = *f.Fire
	}
	if f.CooldownSeconds != nil {
		v.CooldownSeconds = *f.CooldownSeconds
	}
	if f.ManualEdits != nil {
		v.ManualEdits = *f.ManualEdits
	}
	if v.Conditions == nil {
		v.Conditions = []rules.Condition{}
	}
	if msg := rules.Check(v.engine(a.WorkspaceID)); msg != "" {
		return errInvalid(msg)
	}
	c, err := s.readCatalog(ctx, a.WorkspaceID, false)
	if err != nil {
		return err
	}
	if c.recordType(v.RecordType) == nil {
		return errInvalid("record_type " + truncate(v.RecordType, 63) + " is not a record type in the catalog")
	}
	if v.Trigger.Kind == rules.KindAudience {
		var listBrand *string
		err := s.pool.QueryRow(ctx, `SELECT brand_id::text FROM smart_lists WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.Trigger.ListID).Scan(&listBrand)
		if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(listBrand)) {
			return errInvalid("list_id is not a smart list in this workspace")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) ruleApplied(w http.ResponseWriter, r *http.Request, err error) bool {
	var inv errInvalid
	switch {
	case err == nil:
		return true
	case errors.As(err, &inv):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(inv))
	default:
		s.internal(w, r, err)
	}
	return false
}

func (s *Server) createRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f ruleFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil || f.Trigger == nil || f.EmitEvent == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name, trigger and emit_event are required")
		return
	}
	v := ruleView{RecordType: "person", Conditions: []rules.Condition{}, Match: "all", Fire: "every_time"}
	if !s.ruleApplied(w, r, s.applyRule(r, a, f, &v)) {
		return
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only make rules for your brands: set brand_id")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO rules (workspace_id, brand_id, name, description, record_type, trigger_kind, trigger, conditions, match,
			emit_event, fire, cooldown_seconds, manual_edits, watch_events, watch_fields, list_id, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17) RETURNING id`,
			append([]any{a.WorkspaceID, v.BrandID, v.Name, v.Description}, append(ruleColumns(v), createdBy)...)...).Scan(&id); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "rule.created", "rule", id, ruleData(v))
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "a rule with this name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findRule(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

// ruleColumns are the rule's definition columns from record_type to
// list_id, in the order the INSERT and UPDATE use.
func ruleColumns(v ruleView) []any {
	trigger, _ := json.Marshal(v.Trigger)
	conds, _ := json.Marshal(v.Conditions)
	var list *string
	if v.Trigger.Kind == rules.KindAudience {
		list = &v.Trigger.ListID
	}
	return []any{v.RecordType, v.Trigger.Kind, trigger, conds, v.Match, v.EmitEvent, v.Fire, v.CooldownSeconds, v.ManualEdits,
		rules.WatchEvents(v.Trigger), rules.WatchFields(v.Trigger, v.Conditions), list}
}

// ruleData is what the audit log keeps about a rule's definition: kinds
// and counts, never names or values.
func ruleData(v ruleView) map[string]any {
	return map[string]any{"trigger": v.Trigger.Kind, "conditions": len(v.Conditions), "fire": v.Fire, "record_type": v.RecordType,
		"branded": v.BrandID != nil, "emit_event": v.EmitEvent}
}

// updateRule changes a draft or paused rule. While it is active only its
// name and description change: pause it first, so no firing is midway
// through a definition that is being rewritten. A change to what it does
// bumps its version, which each firing records.
func (s *Server) updateRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var f ruleFields
	if !decode(w, r, &f) {
		return
	}
	switch {
	case cur.Status == "archived":
		writeError(w, r, http.StatusConflict, "conflict", "the rule is archived")
		return
	case cur.Status == "active" && !f.onlyLabels():
		writeError(w, r, http.StatusConflict, "conflict", "the rule is active; pause it to change more than its name and description")
		return
	case !a.writes(cur.BrandID):
		writeError(w, r, http.StatusForbidden, "forbidden", "this rule covers every brand; you are limited to some brands")
		return
	}
	v := *cur
	if !s.ruleApplied(w, r, s.applyRule(r, a, f, &v)) {
		return
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only make rules for your brands: set brand_id")
		return
	}
	ctx := r.Context()
	bump := 0
	if !f.onlyLabels() {
		bump = 1
	}
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE rules SET name = $3, description = $4, brand_id = $5, record_type = $6, trigger_kind = $7, trigger = $8,
			conditions = $9, match = $10, emit_event = $11, fire = $12, cooldown_seconds = $13, manual_edits = $14, watch_events = $15,
			watch_fields = $16, list_id = $17, version = version + $18, updated_at = now() WHERE workspace_id = $1 AND id = $2`,
			append([]any{a.WorkspaceID, cur.ID, v.Name, v.Description, v.BrandID}, append(ruleColumns(v), bump)...)...); err != nil {
			return err
		}
		// Where records stood (timers, whether the condition held) belongs
		// to the old definition; the new one starts afresh. Firings stay,
		// so fire: once still holds.
		if bump == 1 {
			if _, err := tx.Exec(ctx, `DELETE FROM rule_states WHERE workspace_id = $1 AND rule_id = $2`, a.WorkspaceID, cur.ID); err != nil {
				return err
			}
		}
		data := ruleData(v)
		data["definition_changed"] = bump == 1
		return record(ctx, tx, &a.principal, a.WorkspaceID, "rule.updated", "rule", cur.ID, data)
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "a rule with this name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findRule(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// activateRule makes a draft or paused rule live. It only acts on what
// happens from now on: events from before, and timers that ran out while
// it was paused, never fire it. Activation is refused when the rule's
// event would set off a chain of active rules that ends up setting it off
// again.
func (s *Server) activateRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.Status != "draft" && cur.Status != "paused" {
		writeError(w, r, http.StatusConflict, "conflict", "the rule is "+cur.Status+"; only a draft or paused rule can be activated")
		return
	}
	if !s.ruleApplied(w, r, s.applyRule(r, a, ruleFields{}, cur)) {
		return
	}
	active, err := rules.Load(r.Context(), s.pool, a.WorkspaceID, `status = 'active'`)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if rules.Loops(cur.engine(a.WorkspaceID), active) {
		writeError(w, r, http.StatusConflict, "conflict", "activating this rule would make a loop: its event "+cur.EmitEvent+
			" sets off active rules that, in the end, set it off again")
		return
	}
	s.setRuleStatus(w, r, a, cur, "active", []string{"draft", "paused"})
}

func (s *Server) pauseRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.Status != "active" {
		writeError(w, r, http.StatusConflict, "conflict", "the rule is "+cur.Status+"; only an active rule can be paused")
		return
	}
	s.setRuleStatus(w, r, a, cur, "paused", []string{"active"})
}

// archiveRule stops a rule for good. Its firings stay.
func (s *Server) archiveRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if cur.Status == "archived" {
		writeError(w, r, http.StatusConflict, "conflict", "the rule is already archived")
		return
	}
	s.setRuleStatus(w, r, a, cur, "archived", []string{"draft", "active", "paused"})
}

func (s *Server) setRuleStatus(w http.ResponseWriter, r *http.Request, a *access, cur *ruleView, to string, from []string) {
	if !a.writes(cur.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this rule covers every brand; you are limited to some brands")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE rules SET status = $3, activated_at = CASE WHEN $3 = 'active' THEN now() ELSE activated_at END,
			updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = ANY ($4)`, a.WorkspaceID, cur.ID, to, from)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errConflict
		}
		switch to {
		case "active":
			err = rules.Paused(ctx, tx, a.WorkspaceID, cur.ID)
		case "archived":
			err = rules.Archived(ctx, tx, a.WorkspaceID, cur.ID)
		}
		if err != nil {
			return err
		}
		action := map[string]string{"active": "rule.activated", "paused": "rule.paused", "archived": "rule.archived"}[to]
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "rule", cur.ID, map[string]any{"from": cur.Status, "trigger": cur.Trigger.Kind})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the rule changed meanwhile; fetch it and try again")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findRule(w, r, a, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// simulateRule is a dry run: what the rule would have done over the last
// days (default 30, at most 90). It writes and sends nothing.
func (s *Server) simulateRule(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Days *int `json:"days"`
	}
	if r.ContentLength != 0 && !decode(w, r, &in) {
		return
	}
	days := 30
	if in.Days != nil {
		if *in.Days < 1 || *in.Days > rules.MaxSimulateDays {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to "+strconv.Itoa(rules.MaxSimulateDays))
			return
		}
		days = *in.Days
	}
	sim, err := rules.Simulate(r.Context(), s.pool, cur.engine(a.WorkspaceID), days)
	if isStatementTimeout(err) {
		writeError(w, r, http.StatusBadRequest, "query_timeout", "the simulation took over 15 seconds; try fewer days")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, sim)
}

type ruleFiringView struct {
	ID           string    `json:"id"`
	RecordID     string    `json:"record_id"`
	RuleVersion  int       `json:"rule_version"`
	EpisodeKey   string    `json:"episode_key"`
	EventID      *string   `json:"event_id"`
	CauseEventID *string   `json:"cause_event_id"`
	Depth        int       `json:"depth"`
	FiredAt      time.Time `json:"fired_at"`
}

// listRuleFirings lists when the rule fired and for whom, newest first.
func (s *Server) listRuleFirings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findRule(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, cur.ID, limit}
	where := ``
	if b := r.URL.Query().Get("before"); b != "" {
		at, id, found := strings.Cut(b, "|")
		t, err := time.Parse(time.RFC3339Nano, at)
		if !found || err != nil || !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "before is not one this API gave out: pass next_before from the previous page unchanged")
			return
		}
		args = append(args, t, id)
		where = ` AND (fired_at, id) < ($4, $5)`
	}
	if rec := r.URL.Query().Get("record_id"); rec != "" {
		if !validUUID(rec) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "record_id must be a record id")
			return
		}
		args = append(args, rec)
		where += ` AND record_id = $` + strconv.Itoa(len(args))
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, record_id::text, rule_version, episode_key, event_id::text, cause_event_id::text, depth, fired_at
		FROM rule_firings WHERE workspace_id = $1 AND rule_id = $2`+where+` ORDER BY fired_at DESC, id DESC LIMIT $3`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ruleFiringView, error) {
		var f ruleFiringView
		return f, row.Scan(&f.ID, &f.RecordID, &f.RuleVersion, &f.EpisodeKey, &f.EventID, &f.CauseEventID, &f.Depth, &f.FiredAt)
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []ruleFiringView{}
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		c := last.FiredAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}

// isStatementTimeout reports whether err is Postgres stopping a statement
// at its statement_timeout.
func isStatementTimeout(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "57014"
}
