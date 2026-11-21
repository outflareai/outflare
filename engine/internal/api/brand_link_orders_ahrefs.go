package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// checkLinkOrderDonorsInAhrefs queues an Ahrefs check of each catalog
// site (linkorders.Checker) on the brand's own Ahrefs connection. It
// answers at once; each row's ahrefs_check says when its check finished.
func (s *Server) checkLinkOrderDonorsInAhrefs(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, r, http.StatusBadRequest, "invalid_request", "send JSON with ids: the catalog rows to check")
		return
	}
	var queued, fresh int
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if queued, fresh, err = linkorders.QueueAhrefsChecks(r.Context(), tx, s.integrations, ws, brand, in.IDs, a.principal.ActorID, time.Now()); err != nil {
			return err
		}
		props := map[string]any{"brand_id": a.Brand, "requested": len(in.IDs), "queued": queued, "fresh": fresh}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "link_orders.ahrefs_check_queued", "brand", a.Brand, props, props)
	})
	if errors.Is(err, linkorders.ErrNoAhrefs) {
		writeError(w, r, http.StatusConflict, "connector_needed", "Connect the brand's own Ahrefs account under Connectors to check sites in Ahrefs.")
		return
	}
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "checks": queued, "fresh": fresh, "estimated_units": queued * linkorders.AhrefsCheckUnits})
}
