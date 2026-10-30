package api

// Exclusion groups as a whole: the flags in each and their slices, the
// group's holdout, and a report comparing the holdout with everyone else.

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/abtests"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/flags"
)

func (s *Server) flagGroupRoutes(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAnalyticsManage
	mux.HandleFunc("GET /v1/workspaces/{ws}/flag-groups", s.inWorkspace(read, s.listFlagGroups))
	mux.HandleFunc("PUT /v1/workspaces/{ws}/flag-groups/{group}", s.inWorkspace(manage, s.setFlagGroup))
	mux.HandleFunc("GET /v1/workspaces/{ws}/flag-groups/{group}/holdout", s.inWorkspace(read, s.flagGroupHoldout))
}

type flagGroupMember struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Share  int    `json:"share"`
	Start  int    `json:"start"`
}

type flagGroupView struct {
	Name    string            `json:"name"`
	Holdout int               `json:"holdout"`
	Flags   []flagGroupMember `json:"flags"`
	// Free is the percent of the group no flag or holdout holds.
	Free int `json:"free"`
}

func (s *Server) loadFlagGroups(ctx context.Context, ws, only string) ([]flagGroupView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.name, coalesce(fg.holdout, 0), f.key, f.name, f.status, f.group_from, f.group_to
		FROM (SELECT DISTINCT exclusion_group AS name FROM flags WHERE workspace_id = $1 AND exclusion_group <> '' AND status <> 'archived'
			UNION SELECT name FROM flag_groups WHERE workspace_id = $1) g
		LEFT JOIN flag_groups fg ON fg.workspace_id = $1 AND fg.name = g.name
		LEFT JOIN flags f ON f.workspace_id = $1 AND f.exclusion_group = g.name AND f.status <> 'archived'
		WHERE $2 = '' OR g.name = $2
		ORDER BY g.name, f.group_from`, ws, only)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []flagGroupView{}
	for rows.Next() {
		var name string
		var holdout int
		var key, fname, status *string
		var from, to *int
		if err := rows.Scan(&name, &holdout, &key, &fname, &status, &from, &to); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != name {
			out = append(out, flagGroupView{Name: name, Holdout: holdout, Flags: []flagGroupMember{}, Free: 100 - holdout})
		}
		g := &out[len(out)-1]
		if key != nil {
			m := flagGroupMember{Key: *key, Name: *fname, Status: *status, Share: (*to - *from) / 100, Start: *from / 100}
			g.Flags = append(g.Flags, m)
			g.Free -= m.Share
		}
	}
	return out, rows.Err()
}

func (s *Server) listFlagGroups(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	list, err := s.loadFlagGroups(r.Context(), a.WorkspaceID, "")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// setFlagGroup sets a group's holdout. It can't reach into a flag's slice:
// shrink or move that flag first.
func (s *Server) setFlagGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Holdout *int `json:"holdout"`
	}
	if !decode(w, r, &in) {
		return
	}
	a := accessFrom(r.Context())
	name := r.PathValue("group")
	if !flags.ValidKey(name) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "a group's name is lowercase letters, digits, - and _, such as homepage")
		return
	}
	if in.Holdout == nil || *in.Holdout < 0 || *in.Holdout > 50 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "holdout is the percent of the group's visitors kept out of every flag in it, 0 to 50")
		return
	}
	if !a.writes(nil) {
		writeError(w, r, http.StatusForbidden, "forbidden", "a group spans brands: only someone with every brand can change its holdout")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		limit := flags.Buckets - *in.Holdout*100
		var clash string
		err := tx.QueryRow(ctx, `SELECT key FROM flags WHERE workspace_id = $1 AND exclusion_group = $2 AND status <> 'archived' AND group_to > $3
			ORDER BY group_to DESC LIMIT 1 FOR UPDATE`, a.WorkspaceID, name, limit).Scan(&clash)
		switch {
		case err == nil:
			return errABConflict(fmt.Sprintf("flag %s's slice reaches into a %d%% holdout: give it a smaller group_share, or take it out of the group, first", clash, *in.Holdout))
		case err != pgx.ErrNoRows:
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO flag_groups (workspace_id, name, holdout) VALUES ($1, $2, $3)
			ON CONFLICT (workspace_id, name) DO UPDATE SET holdout = EXCLUDED.holdout, updated_at = now()`, a.WorkspaceID, name, *in.Holdout); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "flag_group.updated", "flag_group", "", map[string]any{"holdout": *in.Holdout})
	})
	if s.abWriteError(w, r, err) {
		return
	}
	list, err := s.loadFlagGroups(ctx, a.WorkspaceID, name)
	if err != nil || len(list) != 1 {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list[0])
}

// flagGroupHoldout compares a group's holdout with everyone else on a
// goal: ?event=<name> or ?page=<path>, over the last ?days (default 28).
func (s *Server) flagGroupHoldout(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	name := r.PathValue("group")
	q := r.URL.Query()
	var g abtests.Goal
	switch {
	case q.Get("event") != "":
		g = abtests.Goal{Kind: "event", Name: q.Get("event")}
	case q.Get("page") != "":
		g = abtests.Goal{Kind: "page", Path: q.Get("page")}
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give the goal: event=<event name> or page=<path>")
		return
	}
	if msg := abtests.CheckGoals([]abtests.Goal{g}); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	days := abtests.HoldoutDaysDefault
	if d := q.Get("days"); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil || n < 1 || n > abtests.HoldoutDaysMax {
			writeError(w, r, http.StatusBadRequest, "invalid_request", fmt.Sprintf("days is 1 to %d", abtests.HoldoutDaysMax))
			return
		}
		days = n
	}
	ctx, cancel := context.WithTimeout(r.Context(), abResultsTimeout)
	defer cancel()
	list, err := s.loadFlagGroups(ctx, a.WorkspaceID, name)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if len(list) != 1 {
		writeError(w, r, http.StatusNotFound, "not_found", "no exclusion group "+strings.TrimSpace(name))
		return
	}
	if list[0].Holdout == 0 {
		writeError(w, r, http.StatusConflict, "conflict", "this group has no holdout: set one (PUT .../flag-groups/{group}) and let it run before comparing")
		return
	}
	to := time.Now()
	res, err := abtests.Holdout(ctx, s.pool, a.WorkspaceID, name, list[0].Holdout, g, to.AddDate(0, 0, -days), to)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
