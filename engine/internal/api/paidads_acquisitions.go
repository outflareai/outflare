package api

import (
	"net/http"
	"slices"
	"time"

	"github.com/outflareai/outflare/engine/internal/paidads"
)

// listAdAcquisitions lists the people the brand's ads acquired: who,
// through which campaign, ad group, ad and keyword, when they clicked,
// signed up and first paid, and what they have paid since. It names
// people, so it is customers.read.
func (s *Server) listAdAcquisitions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	p := paidads.AcquisitionsQuery{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, Location: loc, Customers: "all"}
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"from", &p.From}, {"to", &p.To}} {
		if v := q.Get(f.name); v != "" {
			t, err := time.Parse(time.DateOnly, v)
			if err != nil {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f.name+" should be a date like 2026-10-24")
				return
			}
			*f.dst = t
		}
	}
	if !p.From.IsZero() && !p.To.IsZero() && p.To.Before(p.From) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to")
		return
	}
	if v := q.Get("campaign_id"); v != "" {
		if !isUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "campaign_id is a campaign's id, from GET .../ads/campaigns")
			return
		}
		p.CampaignID = v
	}
	if v := q.Get("customers"); v != "" {
		if !slices.Contains([]string{"all", "new", "returning"}, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "customers is all, new (people who paid) or returning (people who paid more than once)")
			return
		}
		p.Customers = v
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	p.Before = before
	limit, ok := pageLimit(w, r, 50, 500)
	if !ok {
		return
	}
	p.Limit = limit
	list, err := paidads.Acquisitions(r.Context(), s.pool, p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var next *string
	if len(list) == limit {
		t := list[len(list)-1].ClickedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "next_cursor": nextCursor(pageBefore, next)})
}
