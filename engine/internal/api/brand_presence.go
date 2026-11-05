package api

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// coveragePartyDelete removes a CRM company or person from a placement. The
// placement's history keeps that they were there.
func (s *Server) coveragePartyDelete(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		writeError(w, r, 404, "not_found", "not found")
		return
	}
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		ctx := context.Context(r.Context())
		var placement, record, role string
		if err := tx.QueryRow(ctx, `DELETE FROM coverage_parties WHERE workspace_id=$1 AND brand_id=$2 AND id=$3
			RETURNING placement_id::text, record_id::text, role`, a.WorkspaceID, a.Brand, id).Scan(&placement, &record, &role); err != nil {
			return err
		}
		return coverageHistory(ctx, tx, a, "parties", id, "deleted", map[string]any{"placement_id": placement, "record_id": record, "role": role})
	})
	if s.coverageError(w, r, err) {
		return
	}
	writeJSON(w, 200, map[string]any{"id": id, "deleted": true})
}
