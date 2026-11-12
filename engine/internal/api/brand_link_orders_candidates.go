package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/coverage/linkorders"
	"github.com/outflareai/outflare/engine/internal/customers"
)

func (s *Server) listLinkOrderCandidates(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	siteID, ok := optionalSiteID(w, r)
	if !ok {
		return
	}
	limit, err := limitParam(r, 100, 500)
	if s.allocError(w, r, err) {
		return
	}
	offset, ok := pageOffsetParam(w, r)
	if !ok {
		return
	}
	list, err := linkorders.ListCandidates(r.Context(), s.pool, ws, brand, siteID)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	if list == nil {
		list = []linkorders.Candidate{}
	}
	page, next := page(list, offset, limit)
	writeJSON(w, http.StatusOK, map[string]any{"data": page, "total": len(list), "next_cursor": next})
}

func (s *Server) addLinkOrderCandidateToCatalog(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	raw, err := url.PathUnescape(r.PathValue("domain"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is not a valid website host")
		return
	}
	domain := customers.CompanyDomain(raw)
	if domain == "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "domain is not a valid website host")
		return
	}
	siteID, ok := optionalSiteID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	inGap, err := linkorders.DomainInSavedGap(ctx, s.pool, ws, brand, domain, siteID)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	if !inGap {
		writeError(w, r, http.StatusNotFound, "not_found", "domain is not in the saved link gap")
		return
	}
	// Copy the stored rating onto the catalog row when the gap has one.
	var rating **int
	if list, err := linkorders.ListCandidates(ctx, s.pool, ws, brand, siteID); err == nil {
		for _, c := range list {
			if c.Domain == domain && c.Rating != nil {
				v := int(*c.Rating + 0.5)
				if v < 0 {
					v = 0
				}
				if v > 100 {
					v = 100
				}
				p := &v
				rating = &p
				break
			}
		}
	} else {
		s.writeLinkOrderError(w, r, err)
		return
	}
	var created bool
	var donor *linkorders.Donor
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var e error
		donor, created, e = linkorders.UpsertDonor(ctx, tx, ws, brand, linkorders.DonorWrite{
			Domain:       domain,
			Source:       linkorders.SourceCompetitorGap,
			DomainRating: rating,
		})
		if e != nil {
			return e
		}
		action := "link_orders.candidate_added"
		data := map[string]any{"brand_id": a.Brand, "domain": domain, "created": created}
		return record(ctx, tx, &a.principal, a.WorkspaceID, action, "donor_profile", donor.ID.String(), data)
	})
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, donor)
}

func (s *Server) listLinkOrderEquivalents(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	list, err := linkorders.ListEquivalents(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	if list == nil {
		list = []linkorders.Equivalent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (s *Server) getLinkOrderImpact(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws, brand, _, err := parseLinkOrderIDs(a)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	id, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	v, err := linkorders.LoadImpact(r.Context(), s.pool, ws, brand, id)
	if err != nil {
		s.writeLinkOrderError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func optionalSiteID(w http.ResponseWriter, r *http.Request) (*uuid.UUID, bool) {
	v := strings.TrimSpace(r.URL.Query().Get("site_id"))
	if v == "" {
		return nil, true
	}
	id, err := uuid.Parse(v)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "site_id is an SEO site's id")
		return nil, false
	}
	return &id, true
}
