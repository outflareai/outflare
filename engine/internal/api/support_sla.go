package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/support"
)

// Support SLAs: how fast the team promises to reply and to resolve, for
// the whole workspace ("default") or one brand. Saving one recomputes when
// every conversation not closed is due.

type supportSLAView struct {
	ReplyDueAt        *time.Time `json:"reply_due_at"`
	ReplyBreachedAt   *time.Time `json:"reply_breached_at"`
	ResolveDueAt      *time.Time `json:"resolve_due_at"`
	ResolveBreachedAt *time.Time `json:"resolve_breached_at"`
}

type supportSLAPolicyView struct {
	ID                string          `json:"id"`
	BrandID           *string         `json:"brand_id"`
	Enabled           bool            `json:"enabled"`
	FirstReplyMinutes *int            `json:"first_reply_minutes"`
	NextReplyMinutes  *int            `json:"next_reply_minutes"`
	ResolutionMinutes *int            `json:"resolution_minutes"`
	Timezone          string          `json:"timezone"`
	Hours             json.RawMessage `json:"hours"`
	UpdatedAt         time.Time       `json:"updated_at"`
}

const supportSLAPolicySelect = `SELECT id::text, brand_id::text, enabled, first_reply_minutes, next_reply_minutes, resolution_minutes,
	timezone, COALESCE(hours, 'null'::jsonb), updated_at FROM support_sla_policies`

// supportSLARecompute is how many conversations not closed a policy change
// recomputes at once; the rest pick it up on their next change.
const supportSLARecompute = 5000

func (s *Server) listSupportSLAPolicies(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	rows, err := s.pool.Query(r.Context(), supportSLAPolicySelect+` WHERE workspace_id = $1`+brand.sql("brand_id", &args)+
		` ORDER BY brand_id IS NOT NULL, created_at`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, pgx.RowToStructByPos[supportSLAPolicyView])
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// slaScope is the brand a policy is for. Brands share none, so "default"
// is the workspace's first brand (nil only when it has no brand).
func (s *Server) slaScope(r *http.Request, a *access) (*string, error) {
	scope := r.PathValue("scope")
	if scope == "default" {
		b, err := s.rowBrand(r.Context(), a)
		if err != nil || b == "" {
			return nil, err
		}
		return &b, nil
	}
	id, err := s.brandIDFor(r.Context(), a.WorkspaceID, scope)
	if errors.Is(err, errNotFound) {
		return nil, errNotFound
	}
	return &id, err
}

func (s *Server) putSupportSLAPolicy(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		Enabled           *bool           `json:"enabled"`
		FirstReplyMinutes *int            `json:"first_reply_minutes"`
		NextReplyMinutes  *int            `json:"next_reply_minutes"`
		ResolutionMinutes *int            `json:"resolution_minutes"`
		Timezone          string          `json:"timezone"`
		Hours             json.RawMessage `json:"hours"`
	}
	if !decode(w, r, &req) {
		return
	}
	brand, err := s.slaScope(r, a)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, m := range []*int{req.FirstReplyMinutes, req.NextReplyMinutes, req.ResolutionMinutes} {
		if m != nil && (*m < 1 || *m > 525600) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "targets are minutes from 1 to 525600 (a year), or null for none")
			return
		}
	}
	if req.Timezone == "" {
		req.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(req.Timezone); err != nil || len(req.Timezone) > 64 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "timezone is an IANA time zone, such as Europe/London")
		return
	}
	week, err := support.ParseWeek(req.Hours)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var hours []byte
	if week != nil {
		hours, _ = json.Marshal(week)
	}
	enabled := req.Enabled == nil || *req.Enabled
	ctx := r.Context()
	var v supportSLAPolicyView
	var n int
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `
			INSERT INTO support_sla_policies (workspace_id, brand_id, enabled, first_reply_minutes, next_reply_minutes, resolution_minutes, timezone, hours)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid)) DO UPDATE SET
				enabled = EXCLUDED.enabled, first_reply_minutes = EXCLUDED.first_reply_minutes, next_reply_minutes = EXCLUDED.next_reply_minutes,
				resolution_minutes = EXCLUDED.resolution_minutes, timezone = EXCLUDED.timezone, hours = EXCLUDED.hours, updated_at = now()
			RETURNING id::text`,
			a.WorkspaceID, brand, enabled, req.FirstReplyMinutes, req.NextReplyMinutes, req.ResolutionMinutes, req.Timezone, hours).Scan(&id)
		if err != nil {
			return err
		}
		if v, err = s.supportSLAPolicy(ctx, tx, id); err != nil {
			return err
		}
		if n, err = support.ClockAll(ctx, tx, a.WorkspaceID, supportSLARecompute); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.sla_updated", "support_sla_policy", id, map[string]any{
			"brand": brand != nil, "enabled": enabled, "business_hours": week != nil, "first_reply_minutes": req.FirstReplyMinutes,
			"next_reply_minutes": req.NextReplyMinutes, "resolution_minutes": req.ResolutionMinutes, "recomputed": n})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) supportSLAPolicy(ctx context.Context, q pgx.Tx, id string) (supportSLAPolicyView, error) {
	rows, err := q.Query(ctx, supportSLAPolicySelect+` WHERE id = $1`, id)
	if err != nil {
		return supportSLAPolicyView{}, err
	}
	return pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[supportSLAPolicyView])
}

func (s *Server) deleteSupportSLAPolicy(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, err := s.slaScope(r, a)
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no such brand")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	ctx := r.Context()
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `DELETE FROM support_sla_policies WHERE workspace_id = $1 AND brand_id IS NOT DISTINCT FROM $2::uuid
			RETURNING id::text`, a.WorkspaceID, brand).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		n, err := support.ClockAll(ctx, tx, a.WorkspaceID, supportSLARecompute)
		if err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "support.sla_removed", "support_sla_policy", id, map[string]any{"brand": brand != nil, "recomputed": n})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no SLA for that scope")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
