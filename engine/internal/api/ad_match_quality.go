package api

import (
	"net/http"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// adMatchQuality is what the ad platforms say about how well the brand's
// conversions match their accounts: per pixel and event, the latest score
// out of 10, how it moved, the share of events that carried each
// identifier and what the platform says to fix. It reads stored rows only;
// a daily job fills them (adconv/match_quality.go).
func (s *Server) adMatchQuality(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	rows, err := adconv.LoadMatchQuality(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rows, "low_under": adconv.MatchQualityLow, "fell_by": adconv.MatchQualityDrop})
}
