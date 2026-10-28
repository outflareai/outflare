package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/health"
)

func (s *Server) routesHealth(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/workspaces/{ws}/mailbox-health", s.inWorkspace(auth.ScopeMailboxesRead, s.mailboxHealth))
	mux.HandleFunc("GET /v1/workspaces/{ws}/mailbox-health/settings", s.inWorkspace(auth.ScopeMailboxesRead, s.getHealthSettings))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/mailbox-health/settings", s.inWorkspace(auth.ScopeMailboxesManage, s.putHealthSettings))
}

// mailboxHealth grades every mailbox, or the ones asked for, worst first.
func (s *Server) mailboxHealth(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	ids := []string{}
	for _, v := range q["mailbox_id"] {
		for _, id := range strings.Split(v, ",") {
			if id = strings.TrimSpace(id); id != "" {
				if !validUUID(id) {
					writeError(w, r, http.StatusBadRequest, "invalid_request", "mailbox_id must be a mailbox's id")
					return
				}
				ids = append(ids, id)
			}
		}
	}
	level := q.Get("level")
	if level != "" && level != health.OK && level != health.Warning && level != health.Critical && level != health.Paused {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "level must be ok, warning, critical or paused")
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	if !sel.all() {
		// One brand's mailboxes, or the ones no brand has.
		args := []any{a.WorkspaceID}
		where := sel.sql("brand_id", &args)
		if len(ids) > 0 {
			args = append(args, ids)
			where += fmt.Sprintf(` AND id = ANY($%d::uuid[])`, len(args))
		}
		rows, err := s.pool.Query(r.Context(), `SELECT id::text FROM mailboxes WHERE workspace_id = $1`+where, args...)
		if err == nil {
			ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		}
		if err != nil {
			s.internal(w, r, err)
			return
		}
		if len(ids) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"summary": health.Summarise(nil), "data": []health.Mailbox{}})
			return
		}
	}
	list, err := health.Check(r.Context(), s.pool, a.WorkspaceID, ids)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	sum := health.Summarise(list)
	out := []health.Mailbox{}
	for _, m := range list {
		if level == "" || m.Level == level {
			out = append(out, m)
		}
	}
	order := map[string]int{health.Critical: 0, health.Warning: 1, health.Paused: 2, health.OK: 3}
	sort.SliceStable(out, func(i, j int) bool { return order[out[i].Level] < order[out[j].Level] })
	writeJSON(w, http.StatusOK, map[string]any{"summary": sum, "data": out})
}

func (s *Server) getHealthSettings(w http.ResponseWriter, r *http.Request) {
	set, err := health.LoadSettings(r.Context(), s.pool, accessFrom(r.Context()).WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}

func (s *Server) putHealthSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	set, err := health.LoadSettings(r.Context(), s.pool, a.WorkspaceID)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var req struct {
		AutoPause       *bool    `json:"auto_pause"`
		PauseBounceRate *float64 `json:"pause_bounce_rate"`
		MinSends        *int     `json:"min_sends"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.AutoPause != nil {
		set.AutoPause = *req.AutoPause
	}
	if req.PauseBounceRate != nil {
		if *req.PauseBounceRate <= 0 || *req.PauseBounceRate > 100 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "pause_bounce_rate is a percent above 0 and at most 100")
			return
		}
		set.PauseBounceRate = *req.PauseBounceRate
	}
	if req.MinSends != nil {
		if *req.MinSends < 1 || *req.MinSends > 10000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "min_sends must be between 1 and 10000")
			return
		}
		set.MinSends = *req.MinSends
	}
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		if err := health.SaveSettings(r.Context(), tx, a.WorkspaceID, set); err != nil {
			return err
		}
		return record(r.Context(), tx, &a.principal, a.WorkspaceID, "mailbox_health.settings_updated", "workspace", a.WorkspaceID,
			map[string]any{"auto_pause": set.AutoPause, "pause_bounce_rate": set.PauseBounceRate, "min_sends": set.MinSends})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, set)
}
