package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/emails"
)

// Email guardrails cap how much marketing email one person gets and hold
// it during quiet hours (emails.Guardrails). On by default; each
// workspace changes them here.

type guardrailsPatch struct {
	CapEnabled   *bool   `json:"cap_enabled"`
	PerDay       *int    `json:"per_day"`
	PerWeek      *int    `json:"per_week"`
	QuietEnabled *bool   `json:"quiet_hours_enabled"`
	QuietStart   *int    `json:"quiet_start"`
	QuietEnd     *int    `json:"quiet_end"`
	TimeZone     *string `json:"time_zone"`
}

func (s *Server) getEmailGuardrails(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	g, err := emails.LoadGuardrails(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

func (s *Server) updateEmailGuardrails(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req guardrailsPatch
	if !decode(w, r, &req) {
		return
	}
	ctx := r.Context()
	var g emails.Guardrails
	var bad string
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT 1 FROM workspaces WHERE id = $1 FOR UPDATE`, a.WorkspaceID); err != nil {
			return err
		}
		old, err := emails.LoadGuardrails(ctx, tx, a.WorkspaceID)
		if err != nil {
			return err
		}
		g = old
		set(&g.CapEnabled, req.CapEnabled)
		set(&g.PerDay, req.PerDay)
		set(&g.PerWeek, req.PerWeek)
		set(&g.QuietEnabled, req.QuietEnabled)
		set(&g.QuietStart, req.QuietStart)
		set(&g.QuietEnd, req.QuietEnd)
		set(&g.TimeZone, req.TimeZone)
		if bad = g.Check(); bad != "" || g == old {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET email_cap_enabled = $2, email_cap_per_day = $3, email_cap_per_week = $4,
			email_quiet_enabled = $5, email_quiet_start = $6, email_quiet_end = $7, email_time_zone = $8 WHERE id = $1`,
			a.WorkspaceID, g.CapEnabled, g.PerDay, g.PerWeek, g.QuietEnabled, g.QuietStart, g.QuietEnd, g.TimeZone); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "email_guardrails.updated", "workspace", a.WorkspaceID,
			map[string]any{"cap_enabled": g.CapEnabled, "per_day": g.PerDay, "per_week": g.PerWeek, "quiet_hours_enabled": g.QuietEnabled,
				"quiet_start": g.QuietStart, "quiet_end": g.QuietEnd})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if bad != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", bad)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// set copies a patched value over its current one.
func set[T any](to *T, v *T) {
	if v != nil {
		*to = *v
	}
}
