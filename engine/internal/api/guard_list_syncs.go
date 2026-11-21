package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/guardsync"
)

// Guard list syncs keep a copy of each contact guard's whole list (an
// account feed, a blocklist's exports) in the opt-out list
// (guardsync), so a person can see how fresh each copy is and change how
// often it is read.

type guardListSyncView struct {
	IntegrationID string `json:"integration_id"`
	Connector     string `json:"connector"`
	Name          string `json:"name"`
	// Mode is feed (only what is new is read) or snapshot (read whole).
	Mode string `json:"mode"`
	// Reason is the opt-out reason its entries carry: customer or blocklist.
	Reason                 string     `json:"reason"`
	Source                 string     `json:"source"`
	Enabled                bool       `json:"enabled"`
	IntervalMinutes        int        `json:"interval_minutes"`
	DefaultIntervalMinutes int        `json:"default_interval_minutes"`
	Fresh                  bool       `json:"fresh"`
	Running                bool       `json:"running"`
	BackfilledAt           *time.Time `json:"backfilled_at"`
	AttemptedAt            *time.Time `json:"attempted_at"`
	SucceededAt            *time.Time `json:"succeeded_at"`
	RetryAt                *time.Time `json:"retry_at"`
	LastAdded              int        `json:"last_added"`
	LastRemoved            int        `json:"last_removed"`
	LastDurationMs         int        `json:"last_duration_ms"`
	AddedCount             int64      `json:"added_count"`
	RemovedCount           int64      `json:"removed_count"`
	Entries                int        `json:"entries"`
	LastError              *string    `json:"last_error"`
	ErrorCode              *string    `json:"error_code"`
}

const guardListSyncSelect = `SELECT i.id::text, i.connector, i.name, COALESCE(g.enabled, true), g.interval_seconds, g.backfilled_at, g.attempted_at,
	g.succeeded_at, g.retry_at, COALESCE(g.running_until > now(), false), COALESCE(g.last_added, 0), COALESCE(g.last_removed, 0),
	COALESCE(g.last_duration_ms, 0), COALESCE(g.added_count, 0), COALESCE(g.removed_count, 0), COALESCE(g.entries, 0), g.last_error, g.error_code
	FROM integrations i LEFT JOIN guard_list_syncs g ON g.integration_id = i.id
	WHERE i.workspace_id = $1 AND i.connector = ANY($2)`

func (s *Server) scanGuardListSync(row pgx.CollectableRow) (guardListSyncView, error) {
	var v guardListSyncView
	var every *int
	err := row.Scan(&v.IntegrationID, &v.Connector, &v.Name, &v.Enabled, &every, &v.BackfilledAt, &v.AttemptedAt, &v.SucceededAt, &v.RetryAt,
		&v.Running, &v.LastAdded, &v.LastRemoved, &v.LastDurationMs, &v.AddedCount, &v.RemovedCount, &v.Entries, &v.LastError, &v.ErrorCode)
	if err != nil {
		return v, err
	}
	info, _ := guardsync.Info(s.integrations.Registry, v.Connector)
	v.Mode, v.Reason, v.Source = info.Mode, guardsync.SuppressionReason(info.Reason), guardsync.Source(v.Connector)
	interval := guardsync.Interval(info, every)
	v.IntervalMinutes = int(interval / time.Minute)
	v.DefaultIntervalMinutes = int(guardsync.Interval(info, nil) / time.Minute)
	v.Fresh = v.Enabled && v.BackfilledAt != nil && v.SucceededAt != nil &&
		time.Since(*v.SucceededAt) <= time.Duration(guardsync.FreshFactor)*interval
	return v, nil
}

func (s *Server) listGuardListSyncs(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), guardListSyncSelect+` ORDER BY i.name, i.id`, a.WorkspaceID, guardsync.Listers(s.integrations.Registry))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, s.scanGuardListSync)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) updateGuardListSync(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("integration")
	var req struct {
		Enabled         *bool           `json:"enabled"`
		IntervalMinutes json.RawMessage `json:"interval_minutes"`
	}
	if !decode(w, r, &req) {
		return
	}
	// interval_minutes: a number of minutes, or null for the default.
	var minutes *int
	setInterval := len(req.IntervalMinutes) > 0
	if setInterval && string(req.IntervalMinutes) != "null" {
		var n int
		if json.Unmarshal(req.IntervalMinutes, &n) != nil || n < 1 || n > 1440 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "interval_minutes is a whole number of minutes from 1 to 1440, or null for the default")
			return
		}
		minutes = &n
	}
	if req.Enabled == nil && !setInterval {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "set enabled, interval_minutes or both")
		return
	}
	ctx := r.Context()
	var v guardListSyncView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO guard_list_syncs (integration_id, workspace_id)
			SELECT i.id, i.workspace_id FROM integrations i WHERE i.workspace_id = $1 AND i.id = $2 AND i.connector = ANY($3)
			ON CONFLICT (integration_id) DO NOTHING`, a.WorkspaceID, id, guardsync.Listers(s.integrations.Registry)); err != nil {
			return err
		}
		var seconds *int
		if minutes != nil {
			n := *minutes * 60
			seconds = &n
		}
		tag, err := tx.Exec(ctx, `UPDATE guard_list_syncs SET enabled = COALESCE($3, enabled),
			interval_seconds = CASE WHEN $4 THEN $5 ELSE interval_seconds END, updated_at = now()
			WHERE integration_id = $2 AND workspace_id = $1`, a.WorkspaceID, id, req.Enabled, setInterval, seconds)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errNotFound
		}
		rows, err := tx.Query(ctx, guardListSyncSelect+` AND i.id = $3`, a.WorkspaceID, guardsync.Listers(s.integrations.Registry), id)
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, s.scanGuardListSync); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "guard_list_sync.updated", "integration", id,
			map[string]any{"connector": v.Connector, "enabled": v.Enabled, "interval_minutes": v.IntervalMinutes})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no connected contact guard with a list has that id")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}
