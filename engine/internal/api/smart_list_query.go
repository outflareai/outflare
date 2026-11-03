package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/smartlists"
)

// Smart lists from a saved query, and copies of a list
// (plans/customer-data-platform, the owner's decision of 2026-11-02).

// SmartListQueryIDs runs a query list's saved query (its current version)
// and returns the records in its record_id column, or its id column. It
// reads customer data only (customers.read), inside the list's brand, as
// any query on the brand's route does. smartlists.Service calls it.
func (s *Server) SmartListQueryIDs(ctx context.Context, ws, listID string) ([]string, error) {
	var brand, sql *string
	err := s.pool.QueryRow(ctx, `SELECT l.brand_id::text, v.sql FROM smart_lists l
		LEFT JOIN saved_queries q ON q.id = l.saved_query_id AND q.archived_at IS NULL
		LEFT JOIN saved_query_versions v ON v.query_id = q.id AND v.version = q.current_version
		WHERE l.workspace_id = $1 AND l.id = $2`, ws, listID).Scan(&brand, &sql)
	if err != nil {
		return nil, err
	}
	if sql == nil {
		return nil, smartlists.QueryFailed{Code: "query_missing", Message: "the saved query was archived or deleted: pick another"}
	}
	a := &access{WorkspaceID: ws, Scopes: []string{auth.ScopeCustomersRead}}
	if brand != nil {
		a.Brand, a.Brands = *brand, []string{*brand}
	}
	res, _, err := s.execQuery(ctx, a, *sql, smartlists.MaxQueryMembers, 64<<20)
	var qe *queryError
	if errors.As(err, &qe) {
		return nil, smartlists.QueryFailed{Code: qe.code, Message: qe.message}
	}
	if err != nil {
		return nil, err
	}
	if res.Truncated {
		return nil, smartlists.QueryFailed{Code: "too_many", Message: fmt.Sprintf("the query returns more than %d rows", smartlists.MaxQueryMembers)}
	}
	col := slices.IndexFunc(res.Columns, func(c queryColumn) bool { return c.Name == "record_id" })
	if col < 0 {
		col = slices.IndexFunc(res.Columns, func(c queryColumn) bool { return c.Name == "id" })
	}
	if col < 0 {
		return nil, smartlists.QueryFailed{Code: "no_record_id", Message: "the query needs a record_id (or id) column"}
	}
	ids := make([]string, 0, len(res.Rows))
	for _, row := range res.Rows {
		if id, ok := row[col].(string); ok && validUUID(id) {
			ids = append(ids, strings.ToLower(id))
		}
	}
	return ids, nil
}

// copySmartList saves who is in a list now as a new static list: a
// snapshot that never changes on its own. Static lists hold people.
func (s *Server) copySmartList(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	v, ok := s.findSmartList(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Name *string `json:"name"`
	}
	if !decodeOptional(w, r, &in) {
		return
	}
	if v.RecordType != smartlists.People {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a copy is a static list, which holds people: a list of companies can't be copied")
		return
	}
	if !a.writes(v.BrandID) {
		writeError(w, r, http.StatusForbidden, "forbidden", "this list holds people of every brand; you are limited to some brands")
		return
	}
	name := v.Name + ", " + time.Now().UTC().Format("2 Jan 2006")
	if in.Name != nil {
		name = strings.TrimSpace(*in.Name)
	}
	if name == "" || len(name) > 200 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 200 characters")
		return
	}
	var id string
	var copied int
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		table := "smart_list_members"
		if v.Kind == smartlists.KindStatic {
			table = "static_list_members"
		}
		var createdBy *string
		if a.ActorID != "" {
			createdBy = &a.ActorID
		}
		if err := tx.QueryRow(ctx, `INSERT INTO smart_lists (workspace_id, brand_id, name, description, kind, rules, match, refreshed_at, created_by, record_type)
			VALUES ($1, COALESCE($2, outflare_first_brand($1)), $3, $4, 'static', $5, 'all', $6, $7, 'person') RETURNING id::text`,
			a.WorkspaceID, v.BrandID, name, "A copy of "+v.Name+" as it was on "+time.Now().UTC().Format("2 Jan 2006 15:04")+" UTC.",
			mustJSON(smartlists.NobodyRules), smartlists.Never, createdBy).Scan(&id); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT record_id::text FROM `+table+` WHERE list_id = $1`, v.ID)
		if err != nil {
			return err
		}
		members, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		res, err := smartlists.AddStatic(ctx, tx, a.WorkspaceID, id, members, "copy", v.ID)
		if err != nil {
			return err
		}
		copied = res.Changed
		return record(ctx, tx, &a.principal, a.WorkspaceID, "smart_list.copied", "smart_list", id,
			map[string]any{"from": v.ID, "members": copied, "brand_id": v.BrandID})
	})
	if uniqueViolation(err) {
		writeError(w, r, http.StatusConflict, "conflict", "a list with this name already exists: pass another name")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out, ok := s.findSmartList(w, r, a, id); ok {
		writeJSON(w, http.StatusCreated, out)
	}
}
