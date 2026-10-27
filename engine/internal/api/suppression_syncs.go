package api

import (
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Suppression syncs keep the opt-out list in step with each connected
// sequencer's blocklist (platforms.syncBlocklist), both ways by default.

type suppressionSyncView struct {
	IntegrationID string `json:"integration_id"`
	Connector     string `json:"connector"`
	Name          string `json:"name"`
	// Pull is false for a platform whose blocklist cannot be read.
	CanPull     bool       `json:"can_pull"`
	Pull        bool       `json:"pull"`
	Push        bool       `json:"push"`
	PulledAt    *time.Time `json:"pulled_at"`
	PushedAt    *time.Time `json:"pushed_at"`
	PulledCount int        `json:"pulled_count"`
	PushedCount int        `json:"pushed_count"`
	LastError   *string    `json:"last_error"`
	ErrorCode   *string    `json:"error_code"`
}

// blocklistConnectors are the connector ids that keep a blocklist.
func (s *Server) blocklistConnectors() []string {
	var ids []string
	for _, d := range s.integrations.Registry.All() {
		if slices.Contains(d.Capabilities, connectors.CapBlocklist) {
			ids = append(ids, d.ID)
		}
	}
	return ids
}

const suppressionSyncSelect = `SELECT i.id::text, i.connector, i.name, COALESCE(y.pull, true), COALESCE(y.push, true), y.pulled_at, y.pushed_at,
	COALESCE(y.pulled_count, 0), COALESCE(y.pushed_count, 0), y.last_error, y.error_code
	FROM integrations i LEFT JOIN suppression_syncs y ON y.integration_id = i.id
	WHERE i.workspace_id = $1 AND i.connector = ANY($2)`

func (s *Server) scanSuppressionSync(row pgx.CollectableRow) (suppressionSyncView, error) {
	var v suppressionSyncView
	err := row.Scan(&v.IntegrationID, &v.Connector, &v.Name, &v.Pull, &v.Push, &v.PulledAt, &v.PushedAt, &v.PulledCount, &v.PushedCount, &v.LastError, &v.ErrorCode)
	if c, ok := s.integrations.Registry.Get(v.Connector); ok {
		_, v.CanPull = c.(connectors.BlocklistReader)
	}
	if !v.CanPull {
		v.Pull = false
	}
	return v, err
}

func (s *Server) listSuppressionSyncs(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := s.pool.Query(r.Context(), suppressionSyncSelect+` ORDER BY i.name, i.id`, a.WorkspaceID, s.blocklistConnectors())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	list, err := pgx.CollectRows(rows, s.scanSuppressionSync)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) updateSuppressionSync(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("integration")
	var req struct {
		Pull *bool `json:"pull"`
		Push *bool `json:"push"`
	}
	if !decode(w, r, &req) {
		return
	}
	if req.Pull == nil && req.Push == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "set pull, push or both: true keeps the opt-out list in step that way, false stops it")
		return
	}
	ctx := r.Context()
	var v suppressionSyncView
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if !validUUID(id) {
			return errNotFound
		}
		rows, err := tx.Query(ctx, suppressionSyncSelect+` AND i.id = $3`, a.WorkspaceID, s.blocklistConnectors(), id)
		if err != nil {
			return err
		}
		if v, err = pgx.CollectExactlyOneRow(rows, s.scanSuppressionSync); errors.Is(err, pgx.ErrNoRows) {
			return errNotFound
		} else if err != nil {
			return err
		}
		if req.Pull != nil && *req.Pull && !v.CanPull {
			return errInvalid(v.Name + "'s blocklist cannot be read, so only push can be on")
		}
		if req.Pull != nil {
			v.Pull = *req.Pull
		}
		if req.Push != nil {
			v.Push = *req.Push
		}
		if _, err := tx.Exec(ctx, `INSERT INTO suppression_syncs (integration_id, workspace_id, pull, push) VALUES ($1, $2, $3, $4)
			ON CONFLICT (integration_id) DO UPDATE SET pull = $3, push = $4, updated_at = now()`, id, a.WorkspaceID, v.Pull, v.Push); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "suppression_sync.updated", "integration", id,
			map[string]any{"connector": v.Connector, "pull": v.Pull, "push": v.Push})
	})
	if errors.Is(err, errNotFound) {
		writeError(w, r, http.StatusNotFound, "not_found", "no connected sequencer with a blocklist has that id")
		return
	}
	if s.recordError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, v)
}
