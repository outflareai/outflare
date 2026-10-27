package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Smart lists: groups of people on customer records that keep themselves
// up to date from rules. They sit beside lead lists on the Lists page.
// internal/smartlists works out who matches; these handlers make and
// change lists and show their members.

func (s *Server) routesSmartLists(mux *http.ServeMux) {
	read, manage := auth.ScopeCustomersRead, auth.ScopeCustomersManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/smart-lists", s.inBrand(read, s.listSmartLists))
	mux.HandleFunc("POST /v1/workspaces/{ws}/smart-lists", s.inBrand(manage, s.createSmartList))
	mux.HandleFunc("POST /v1/workspaces/{ws}/smart-lists/preview", s.inBrand(read, s.previewSmartList))
	mux.HandleFunc("GET /v1/workspaces/{ws}/smart-lists/{id}", s.inBrand(read, s.getSmartList))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/smart-lists/{id}", s.inBrand(manage, s.updateSmartList))
	mux.HandleFunc("DELETE /v1/workspaces/{ws}/smart-lists/{id}", s.inBrand(manage, s.deleteSmartList))
	mux.HandleFunc("GET /v1/workspaces/{ws}/smart-lists/{id}/members", s.inBrand(read, s.listSmartListMembers))
	mux.HandleFunc("POST /v1/workspaces/{ws}/smart-lists/{id}/refresh", s.inBrand(manage, s.refreshSmartList))
}

type smartListView struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	BrandID     *string           `json:"brand_id"`
	Rules       []smartlists.Rule `json:"rules"`
	Match       string            `json:"match"`
	Members     int               `json:"members"`
	RefreshedAt *time.Time        `json:"refreshed_at"`
	ErrorCode   *string           `json:"error_code"`
	CreatedAt   time.Time         `json:"created_at"`
	UpdatedAt   time.Time         `json:"updated_at"`
}

const smartListSelect = `SELECT l.id, l.name, l.description, l.brand_id, l.rules, l.match, l.members, l.refreshed_at, l.error_code,
	l.created_at, l.updated_at FROM smart_lists l WHERE l.workspace_id = $1`

func scanSmartList(row pgx.Row) (smartListView, error) {
	var v smartListView
	var rules []byte
	err := row.Scan(&v.ID, &v.Name, &v.Description, &v.BrandID, &rules, &v.Match, &v.Members, &v.RefreshedAt, &v.ErrorCode, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	v.Rules = []smartlists.Rule{}
	_ = json.Unmarshal(rules, &v.Rules)
	return v, nil
}

func (s *Server) listSmartLists(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), smartListSelect+sel.sql("l.brand_id", &args)+` ORDER BY lower(l.name) LIMIT 500`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (smartListView, error) { return scanSmartList(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []smartListView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// findSmartList loads one list the caller may see, or writes a 404.
func (s *Server) findSmartList(w http.ResponseWriter, r *http.Request, a *access, id string) (*smartListView, bool) {
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "smart list not found")
		return nil, false
	}
	v, err := scanSmartList(s.pool.QueryRow(r.Context(), smartListSelect+` AND l.id = $2`, a.WorkspaceID, id))
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(nil, v.BrandID)) {
		writeError(w, r, http.StatusNotFound, "not_found", "smart list not found")
		return nil, false
	}
	if err != nil {
		s.internal(w, r, err)
		return nil, false
	}
	return &v, true
}

func (s *Server) getSmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if v, ok := s.findSmartList(w, r, a, r.PathValue("id")); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

type smartListFields struct {
	Name        *string            `json:"name"`
	Description *string            `json:"description"`
	BrandID     optional[string]   `json:"brand_id"`
	Rules       *[]smartlists.Rule `json:"rules"`
	Match       *string            `json:"match"`
}

// changesMembers reports whether f changes who is in the list.
func (f smartListFields) changesMembers() bool {
	return f.BrandID.Set || f.Rules != nil || f.Match != nil
}

// applySmartList checks f and applies it to v.
func (s *Server) applySmartList(r *http.Request, a *access, f smartListFields, v *smartListView) error {
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
			id, err := s.brandIDFor(r.Context(), a.WorkspaceID, *f.BrandID.Value)
			if errors.Is(err, errNotFound) {
				return errInvalid("brand_id must be one of the workspace's brands, or null for every brand")
			}
			if err != nil {
				return err
			}
			v.BrandID = &id
		}
	}
	if f.Rules != nil {
		v.Rules = *f.Rules
	}
	if f.Match != nil {
		v.Match = *f.Match
	}
	return s.checkSmartRules(r, a, v.Rules, v.Match)
}

// checkSmartRules checks rules and that the lead lists they name are the
// workspace's.
func (s *Server) checkSmartRules(r *http.Request, a *access, rules []smartlists.Rule, match string) error {
	if msg := smartlists.Check(rules, match); msg != "" {
		return errInvalid(msg)
	}
	if ids := smartlists.InLists(rules); len(ids) > 0 {
		var n int
		if err := s.pool.QueryRow(r.Context(), `SELECT count(DISTINCT id)::int FROM lead_lists WHERE workspace_id = $1 AND id = ANY($2::uuid[])`,
			a.WorkspaceID, ids).Scan(&n); err != nil {
			return err
		}
		if n != len(uniq(ids)) {
			return errInvalid("in_list names a lead list that isn't in this workspace")
		}
	}
	return nil
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

// smartListApplied writes the error from applySmartList, if any.
func (s *Server) smartListApplied(w http.ResponseWriter, r *http.Request, err error) bool {
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

func uniqueViolation(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23505"
}

func (s *Server) createSmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var f smartListFields
	if !decode(w, r, &f) {
		return
	}
	if f.Name == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name is required")
		return
	}
	v := smartListView{Rules: []smartlists.Rule{}, Match: "all"}
	if !s.smartListApplied(w, r, s.applySmartList(r, a, f, &v)) {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only make smart lists for your brands: set brand_id")
		return
	}
	ctx := r.Context()
	var id string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rules, _ := json.Marshal(v.Rules)
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO smart_lists (workspace_id, brand_id, name, description, rules, match, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, a.WorkspaceID, v.BrandID, v.Name, v.Description, rules, v.Match, createdBy).Scan(&id); err != nil {
			return err
		}
		if _, err := smartlists.Enqueue(ctx, tx, a.WorkspaceID, id, true); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "smart_list.created", "smart_list", id,
			map[string]any{"rules": len(v.Rules), "match": v.Match, "branded": v.BrandID != nil})
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "a list with this name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSmartList(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, v)
	}
}

func (s *Server) updateSmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findSmartList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var f smartListFields
	if !decode(w, r, &f) {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this list holds people of every brand; you are limited to some brands")
		return
	}
	if !s.smartListApplied(w, r, s.applySmartList(r, a, f, v)) {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only make smart lists for your brands: set brand_id")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		rules, _ := json.Marshal(v.Rules)
		if _, err := tx.Exec(ctx, `UPDATE smart_lists SET name = $3, description = $4, brand_id = $5, rules = $6, match = $7, updated_at = now()
			WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID, v.Name, v.Description, v.BrandID, rules, v.Match); err != nil {
			return err
		}
		if f.changesMembers() {
			if _, err := smartlists.Enqueue(ctx, tx, a.WorkspaceID, v.ID, true); err != nil {
				return err
			}
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "smart_list.updated", "smart_list", v.ID,
			map[string]any{"rules": len(v.Rules), "match": v.Match, "branded": v.BrandID != nil, "members_change": f.changesMembers()})
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "a list with this name already exists")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if v, ok := s.findSmartList(w, r, a, v.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

func (s *Server) deleteSmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findSmartList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if !a.writes(nil, v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this list holds people of every brand; you are limited to some brands")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM smart_lists WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, v.ID); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "smart_list.deleted", "smart_list", v.ID, map[string]any{"members": v.Members})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) refreshSmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findSmartList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	if _, err := smartlists.Enqueue(r.Context(), s.pool, a.WorkspaceID, v.ID, true); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true})
}

type smartPreviewInput struct {
	BrandID *string           `json:"brand_id"`
	Rules   []smartlists.Rule `json:"rules"`
	Match   *string           `json:"match"`
}

// previewSmartList counts who rules pick now and shows a few of them,
// without saving anything, so the app can show the count while someone
// edits the rules.
func (s *Server) previewSmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in smartPreviewInput
	if !decode(w, r, &in) {
		return
	}
	match := "all"
	if in.Match != nil {
		match = *in.Match
	}
	if in.Rules == nil {
		in.Rules = []smartlists.Rule{}
	}
	var brand *string
	if in.BrandID != nil {
		id, err := s.brandIDFor(r.Context(), a.WorkspaceID, *in.BrandID)
		if errors.Is(err, errNotFound) || (err == nil && !a.sees(nil, &id)) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "brand_id must be one of the workspace's brands, or null for every brand")
			return
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		brand = &id
	}
	if !s.smartListApplied(w, r, s.checkSmartRules(r, a, in.Rules, match)) {
		return
	}
	n, sample, err := s.smartPreview(r, a, in.Rules, match, brand)
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "57014" {
		writeError(w, r, http.StatusBadRequest, "query_timeout", "these rules took too long to count: narrow them, for example with a recent time window on an event")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": n, "sample": sample})
}

// smartPreview runs a preview in a read-only transaction with a time
// limit. A brand-limited caller only counts its brands' people.
func (s *Server) smartPreview(r *http.Request, a *access, rules []smartlists.Rule, match string, brand *string) (int, []smartlists.Person, error) {
	ctx := r.Context()
	var n int
	var sample []smartlists.Person
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET LOCAL statement_timeout = '15s'`); err != nil {
			return err
		}
		var err error
		n, sample, err = smartlists.Preview(ctx, tx, a.WorkspaceID, rules, match, brand, a.Brands, 10)
		return err
	})
	return n, sample, err
}

func (s *Server) listSmartListMembers(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.findSmartList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	var after *time.Time
	var afterID string
	if b := r.URL.Query().Get("before"); b != "" {
		at, id, found := strings.Cut(b, "|")
		t, err := time.Parse(time.RFC3339Nano, at)
		if !found || err != nil || !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_cursor", "before is not one this API gave out: pass next_cursor from the previous page unchanged")
			return
		}
		after, afterID = &t, id
	}
	list, err := smartlists.Members(r.Context(), s.pool, a.WorkspaceID, v.ID, a.Brands, after, afterID, limit)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		last := list[len(list)-1]
		c := last.EnteredAt.UTC().Format(time.RFC3339Nano) + "|" + last.ID
		next = &c
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_before": next})
}
