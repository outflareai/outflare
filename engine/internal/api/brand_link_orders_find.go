package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// findLinkOrderDonorEmails queues OutflareBot's search of each donor's
// site for who to write to (linkorders.Finder). It answers at once; each
// row's email_search says when its search finished.
func (s *Server) findLinkOrderDonorEmails(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var in struct {
		IDs []uuid.UUID `json:"ids"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send JSON with ids: the catalog rows to search")
		return
	}
	var queued int
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if queued, err = linkorders.QueueFindEmails(r.Context(), tx, ws, brand, in.IDs, time.Now()); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "requested": len(in.IDs), "queued": queued}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.emails_search_queued", "brand", a.Brand, props, props)
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "searches": queued})
}
