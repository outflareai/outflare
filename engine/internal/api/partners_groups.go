package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// maxGroupAssign is how many partners one call moves into a group.
const maxGroupAssign = 1000

// updatePartnerGroup renames a group or makes it the one new partners
// join (default: true), or stops new partners joining it (default: false).
func (s *Server) updatePartnerGroup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		Name    *string `json:"name"`
		Default *bool   `json:"default"`
	}
	if !decode(w, r, &in) {
		return
	}
	group := r.PathValue("group")
	var g *partnerGroupView
	for i := range cur.Groups {
		if cur.Groups[i].ID == group {
			g = &cur.Groups[i]
		}
	}
	if g == nil {
		writeError(w, r, http.StatusNotFound, "not_found", "group not found")
		return
	}
	if in.Name != nil {
		if n := strings.TrimSpace(*in.Name); n == "" || len(n) > 80 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "name must be 1 to 80 characters, such as Gold")
			return
		}
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if in.Name != nil {
			_, err := tx.Exec(ctx, `UPDATE partner_groups SET name = $3 WHERE workspace_id = $1 AND id = $2`, a.WorkspaceID, g.ID, strings.TrimSpace(*in.Name))
			if pe := (*pgconn.PgError)(nil); errors.As(err, &pe) && pe.Code == "23505" {
				return errConflict
			}
			if err != nil {
				return err
			}
		}
		if in.Default != nil {
			if _, err := tx.Exec(ctx, `UPDATE partner_programs SET default_group_id = CASE WHEN $3 THEN $2::uuid
				WHEN default_group_id = $2::uuid THEN NULL ELSE default_group_id END, updated_at = now() WHERE id = $1`, cur.ID, g.ID, *in.Default); err != nil {
				return err
			}
		}
		data := map[string]any{"group_id": g.ID, "renamed": in.Name != nil}
		if in.Default != nil {
			data["default"] = *in.Default
		}
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.group_updated", "partner_program", cur.ID, data)
	})
	if errors.Is(err, errConflict) {
		writeError(w, r, http.StatusConflict, "conflict", "the program already has a group called "+strings.TrimSpace(*in.Name))
		return
	}
	if s.allocError(w, r, err) {
		return
	}
	if v, ok := s.findPartnerProgram(w, r, a.WorkspaceID, cur.ID); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// assignPartnerGroup moves partners of the program into a group, or out
// of every group when the group is "none". A partner is in one group at a
// time, and the group decides which group rules apply to them.
func (s *Server) assignPartnerGroup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	cur, ok := s.findPartnerProgram(w, r, a.WorkspaceID, r.PathValue("id"))
	if !ok {
		return
	}
	var in struct {
		PartnerIDs []string `json:"partner_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.PartnerIDs) == 0 || len(in.PartnerIDs) > maxGroupAssign {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_ids lists 1 to 1,000 partners of this program")
		return
	}
	for _, id := range in.PartnerIDs {
		if !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_ids must be partners' ids")
			return
		}
	}
	var group *string
	if g := r.PathValue("group"); g != "none" {
		found := false
		for _, have := range cur.Groups {
			found = found || have.ID == g
		}
		if !found {
			writeError(w, r, http.StatusNotFound, "not_found", "group not found; none takes partners out of every group")
			return
		}
		group = &g
	}
	ctx := r.Context()
	var moved int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE partner_memberships SET group_id = $4, updated_at = now()
			WHERE workspace_id = $1 AND program_id = $2 AND id = ANY($3::uuid[]) AND group_id IS DISTINCT FROM $4::uuid`,
			a.WorkspaceID, cur.ID, in.PartnerIDs, group)
		if err != nil {
			return err
		}
		moved = tag.RowsAffected()
		return partnerRecord(ctx, tx, &a.principal, a.WorkspaceID, "partner_program.group_assigned", "partner_program", cur.ID,
			map[string]any{"group_id": group, "partners": moved})
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"moved": moved})
}
