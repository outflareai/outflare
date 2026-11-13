package api

// A brand's click filter lists (plans/ad-tracker.md, B7b): addresses,
// ranges and pieces of a user agent its team knows to be bots or fraud. A
// redirect click that matches gets the fraud reason filter_list: it still
// goes on, as every flagged click does, but counts against no cap and
// shows as suspect in the reports. Nothing sends matched clicks elsewhere.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/fraud"
)

// maxClickFilters is how many entries a brand's lists hold.
const maxClickFilters = 1000

func (s *Server) routesTrackerClickFilters(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/click-filters", s.onBrand(auth.ScopeAnalyticsRead, s.listClickFilters))
	mux.HandleFunc("POST /v1/brands/{brand}/click-filters", s.onBrand(auth.ScopeAnalyticsManage, s.createClickFilter))
	mux.HandleFunc("DELETE /v1/brands/{brand}/click-filters/{id}", s.onBrand(auth.ScopeAnalyticsManage, s.deleteClickFilter))
}

type clickFilterView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *Server) listClickFilters(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), `SELECT id::text, kind, value, note, created_at FROM tracker_click_filters
		WHERE workspace_id = $1 AND brand_id = $2 ORDER BY kind, value`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[clickFilterView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if list == nil {
		list = []clickFilterView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

type clickFilterInput struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
	Note  string `json:"note"`
}

// clean checks an entry and writes it the way it is matched: a range in
// its masked form, a user agent in lowercase.
func (in *clickFilterInput) clean() error {
	in.Value, in.Note = strings.TrimSpace(in.Value), strings.TrimSpace(in.Note)
	switch in.Kind {
	case "ip":
		p, ok := fraud.ParseFilterPrefix(in.Value)
		if !ok {
			return errInvalid("an ip filter is an address (203.0.113.7) or a range (203.0.113.0/24)")
		}
		if p.Bits() < map[bool]int{true: 8, false: 32}[p.Addr().Is4()] {
			return errInvalid("an ip range is at most a /8 (IPv4) or a /32 (IPv6), so a typo can't flag everyone")
		}
		in.Value = p.String()
		if p.IsSingleIP() {
			in.Value = p.Addr().String()
		}
	case "user_agent":
		in.Value = strings.ToLower(in.Value)
		if len(in.Value) < 3 || len(in.Value) > 200 {
			return errInvalid("a user_agent filter is 3 to 200 characters of the user agent, such as headlesschrome")
		}
	default:
		return errInvalid("kind is ip or user_agent")
	}
	if len(in.Note) > 200 {
		return errInvalid("note is at most 200 characters")
	}
	return nil
}

func (s *Server) createClickFilter(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var in clickFilterInput
	if !decode(w, r, &in) || s.allocError(w, r, in.clean()) {
		return
	}
	var v clickFilterView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM tracker_click_filters WHERE workspace_id = $1 AND brand_id = $2`, a.WorkspaceID, a.Brand).Scan(&n); err != nil {
			return err
		}
		if n >= maxClickFilters {
			return errInvalid("a brand's filter lists hold at most 1,000 entries; use ranges for neighbouring addresses")
		}
		err := tx.QueryRow(ctx, `INSERT INTO tracker_click_filters (workspace_id, brand_id, kind, value, note, created_by)
			VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (brand_id, kind, value) DO NOTHING
			RETURNING id::text, kind, value, note, created_at`, a.WorkspaceID, a.Brand, in.Kind, in.Value, in.Note, nullIfEmpty(a.ActorID)).
			Scan(&v.ID, &v.Kind, &v.Value, &v.Note, &v.CreatedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errConflict
		}
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "click_filter.created", "click_filter", v.ID, map[string]any{"brand_id": a.Brand, "kind": v.Kind})
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "that "+in.Kind+" filter is already on the list")
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	s.redirects.clear()
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) deleteClickFilter(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such filter")
		return
	}
	var kind string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `DELETE FROM tracker_click_filters WHERE workspace_id = $1 AND brand_id = $2 AND id = $3 RETURNING kind`,
			a.WorkspaceID, a.Brand, id).Scan(&kind)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "click_filter.deleted", "click_filter", id, map[string]any{"brand_id": a.Brand, "kind": kind})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such filter")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	s.redirects.clear()
	w.WriteHeader(http.StatusNoContent)
}

// filterQuerier is a pool or a transaction.
type filterQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// loadClickFilters reads a brand's lists, for the redirect cache and a
// direct click.
func loadClickFilters(ctx context.Context, q filterQuerier, ws, brand string) (fraud.ClickFilters, error) {
	var f fraud.ClickFilters
	rows, err := q.Query(ctx, `SELECT kind, value FROM tracker_click_filters WHERE workspace_id = $1 AND brand_id = $2 LIMIT $3`, ws, brand, maxClickFilters)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, value string
		if err := rows.Scan(&kind, &value); err != nil {
			return f, err
		}
		if kind == "user_agent" {
			f.Agents = append(f.Agents, value)
		} else if p, ok := fraud.ParseFilterPrefix(value); ok {
			f.Prefixes = append(f.Prefixes, p)
		}
	}
	return f, rows.Err()
}
