package api

import (
	"net/http"
	"time"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
)

// getLinkROI is cost per published link against stored Search Console,
// ranking and referring-domain changes on each target page, at 30, 60
// and 90 days. It does not call a provider or claim the link caused a change.
func (s *Server) getLinkROI(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	v, err := linkorders.LoadROI(r.Context(), s.pool, ws, brand, time.Now().UTC())
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
