package api

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

// Partner settings are the few partner rules that hold for every program
// in the workspace. The one today: whether erasing a partner's record also
// clears the note and reference on their payouts (customers.Erase).

type partnerSettings struct {
	ErasePayoutNotes *bool `json:"erase_payout_notes"`
}

func (s *Server) getPartnerSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var v bool
	err := s.pool.QueryRow(r.Context(), `SELECT erase_partner_payout_notes FROM workspaces WHERE id = $1`, a.WorkspaceID).Scan(&v)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, partnerSettings{ErasePayoutNotes: &v})
}

func (s *Server) updatePartnerSettings(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req partnerSettings
	if !decode(w, r, &req) {
		return
	}
	if req.ErasePayoutNotes == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "erase_payout_notes is required: true clears a partner's payout notes and references when their record is erased")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var old bool
		if err := tx.QueryRow(ctx, `SELECT erase_partner_payout_notes FROM workspaces WHERE id = $1 FOR UPDATE`, a.WorkspaceID).Scan(&old); err != nil {
			return err
		}
		if old == *req.ErasePayoutNotes {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE workspaces SET erase_partner_payout_notes = $2 WHERE id = $1`, a.WorkspaceID, *req.ErasePayoutNotes); err != nil {
			return err
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "partner_settings.updated", "workspace", a.WorkspaceID,
			map[string]any{"erase_payout_notes": *req.ErasePayoutNotes})
	})
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "workspace not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, req)
}
