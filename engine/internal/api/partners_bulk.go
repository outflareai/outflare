package api

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

// maxBulkRewards is how many rewards one bulk call changes; more says
// there are others to call again for.
const maxBulkRewards = 1000

type bulkRewardsInput struct {
	Action    string   `json:"action"`
	IDs       []string `json:"ids"`
	Program   string   `json:"program"`
	PartnerID string   `json:"partner_id"`
	Kind      string   `json:"kind"`
	Note      string   `json:"note"`
}

type bulkRewardSkip struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
}

// bulkPartnerRewards approves or rejects many held rewards at once: by id,
// or every pending one of a program or partner. Each is audited as if
// done alone, so webhooks and the partner's notices follow.
func (s *Server) bulkPartnerRewards(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in bulkRewardsInput
	if !decode(w, r, &in) {
		return
	}
	in.Program = strings.ToLower(strings.TrimSpace(in.Program))
	note := strings.TrimSpace(in.Note)
	switch {
	case in.Action != "approve" && in.Action != "reject":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "action is approve or reject")
		return
	case len(in.IDs) == 0 && in.Program == "" && in.PartnerID == "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "say which rewards: ids, or a program or partner_id for all their pending ones")
		return
	case len(in.IDs) > maxBulkRewards:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "at most 1,000 ids at a time")
		return
	case in.PartnerID != "" && !validUUID(in.PartnerID):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "partner_id is a partner's id")
		return
	case in.Kind != "" && !slices.Contains([]string{"cash", "fee", "action", "adjustment"}, in.Kind):
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is cash, fee, action or adjustment")
		return
	case in.Action == "approve" && note != "":
		writeError(w, r, http.StatusBadRequest, "invalid_request", "note is the reason for a rejection")
		return
	}
	ids, skipped := []string{}, []bulkRewardSkip{}
	for _, id := range in.IDs {
		if validUUID(id) {
			ids = append(ids, id)
		} else {
			skipped = append(skipped, bulkRewardSkip{ID: id, Reason: "not_found"})
		}
	}
	if len(in.IDs) > 0 && len(ids) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"action": in.Action, "done": 0, "skipped": skipped, "more": false})
		return
	}
	ctx := r.Context()
	done, more := 0, false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// The rewards the ids or the filter name, in programs the caller
		// sees: another brand's look missing, and shared ones (which a
		// brand-limited caller only reads) are listed by id as skipped.
		rows, err := tx.Query(ctx, `SELECT w.id::text, w.status, $7::uuid[] IS NULL OR COALESCE(p.brand_id = ANY($7), false)
			FROM partner_rewards w JOIN partner_programs p ON p.id = w.program_id
			WHERE w.workspace_id = $1 AND (cardinality($2::uuid[]) = 0 OR w.id = ANY($2::uuid[]))
			  AND ($3 = '' OR p.id::text = $3 OR p.slug = $3) AND ($4 = '' OR w.membership_id::text = $4) AND ($5 = '' OR w.kind = $5)
			  AND (cardinality($2::uuid[]) > 0 OR w.status = 'pending')
			  AND ($7::uuid[] IS NULL OR p.brand_id = ANY($7) OR (p.brand_id IS NULL AND cardinality($2::uuid[]) > 0))
			ORDER BY w.created_at, w.id LIMIT $6 FOR UPDATE OF w`,
			a.WorkspaceID, ids, in.Program, in.PartnerID, in.Kind, maxBulkRewards+1, a.Brands)
		if err != nil {
			return err
		}
		type found struct {
			id, status string
			writable   bool
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (found, error) {
			var f found
			return f, row.Scan(&f.id, &f.status, &f.writable)
		})
		if err != nil {
			return err
		}
		if len(list) > maxBulkRewards {
			list, more = list[:maxBulkRewards], true
		}
		seen := map[string]bool{}
		for _, f := range list {
			seen[f.id] = true
			switch {
			case !f.writable:
				skipped = append(skipped, bulkRewardSkip{ID: f.id, Reason: "shared_program"})
				continue
			case f.status != "pending":
				skipped = append(skipped, bulkRewardSkip{ID: f.id, Reason: f.status})
				continue
			}
			if err := s.bulkRewardOne(ctx, tx, a, in.Action, f.id, note); err != nil {
				return err
			}
			done++
		}
		for _, id := range ids {
			if !seen[id] {
				skipped = append(skipped, bulkRewardSkip{ID: id, Reason: "not_found"})
			}
		}
		return nil
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"action": in.Action, "done": done, "skipped": skipped, "more": more})
}

func (s *Server) bulkRewardOne(ctx context.Context, tx pgx.Tx, a *access, action, id, note string) error {
	if action == "approve" {
		return approvePartnerReward(ctx, tx, &a.principal, a.WorkspaceID, id, "manual")
	}
	v, err := loadReward(ctx, tx, a.WorkspaceID, id)
	if err != nil {
		return err
	}
	return rejectReward(ctx, tx, &a.principal, a.WorkspaceID, v, note)
}
