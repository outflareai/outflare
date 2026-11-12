package api

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/outflareai/outflare/engine/internal/paidads"
)

// adsPerformance is the brand's campaigns, ad groups or ads with what each
// spent and what Outflare's own ledger says it brought: sign-ups, new
// paying customers and what each cost, against the brand's target.
func (s *Server) adsPerformance(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	// Ad accounts report by their own time zone: the default end is the
	// latest date anywhere (UTC+14), as the campaigns list does.
	to := time.Now().UTC().Add(14 * time.Hour).Truncate(24 * time.Hour)
	if v := q.Get("to"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "to should be a date like 2026-10-24")
			return
		}
		to = t
	}
	from := to.AddDate(0, 0, -27)
	if v := q.Get("from"); v != "" {
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "from should be a date like 2026-09-27")
			return
		}
		from = t
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to, and at most a year before it")
		return
	}
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	p := paidads.PerformanceQuery{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, Level: paidads.LevelCampaign, From: from, To: to, Location: loc, Customers: "all"}
	if v := q.Get("level"); v != "" {
		if !slices.Contains([]string{paidads.LevelCampaign, paidads.LevelAdGroup, paidads.LevelAd}, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "level is campaign, ad_group or ad")
			return
		}
		p.Level = v
	}
	if v := q.Get("customers"); v != "" {
		if !slices.Contains([]string{"all", "new", "returning"}, v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "customers is all, new or returning")
			return
		}
		p.Customers = v
	}
	p.Connector = q.Get("connector")
	for _, f := range []struct {
		name string
		dst  *string
	}{{"account_id", &p.AccountID}, {"campaign_id", &p.CampaignID}} {
		if v := q.Get(f.name); v != "" {
			if !isUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f.name+" is an id from GET .../ads/accounts or .../ads/campaigns")
				return
			}
			*f.dst = v
		}
	}
	if v := q.Get("window_days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > paidads.MaxReadWindowDays {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "window_days is 1 to "+strconv.Itoa(paidads.MaxReadWindowDays))
			return
		}
		p.WindowDays = n
	}
	econ, err := paidads.Load(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if errors.Is(err, paidads.ErrNoBrand) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	p.Econ = econ
	rows, err := paidads.Performance(r.Context(), s.pool, p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	window := econ.AcquisitionWindowDays
	if p.WindowDays > 0 {
		window = p.WindowDays
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": p.From.Format(time.DateOnly), "to": p.To.Format(time.DateOnly), "level": p.Level, "customers": p.Customers,
		"window_days": window, "target_cost_per_payer_micros": econ.TargetCostPerPayerMicros, "target_cost_per_signup_micros": econ.TargetCostPerSignupMicros,
		"currency": econ.Currency, "data": rows,
		"notes": []string{"Spend and costs are in micros of each account's currency; revenue is in minor units per currency. Nothing is converted or added across currencies.",
			"signups and new_payers are Outflare's own count: the last paid click within the acquisition window before the sign-up or first payment. platform_conversions is the network's own claim, shown for comparison and never added in."}})
}
