package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/outflareai/outflare/engine/internal/paidads"
)

// adsMatchCheck is the dry run of joining visits by network and device
// (paidads.RunMatchCheck). It reads stored visits and changes nothing.
func (s *Server) adsMatchCheck(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days := 0
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > paidads.MatchCheckMaxDays {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days must be between 1 and 90")
			return
		}
		days = n
	}
	out, err := paidads.RunMatchCheck(r.Context(), s.pool, a.WorkspaceID, a.Brand, days, time.Now())
	if errors.Is(err, paidads.ErrNoBrand) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// adsCreativeResults is every ad that ran in the range, scored step by
// step with a verdict (paidads.CreativeResults).
func (s *Server) adsCreativeResults(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	to := time.Now().UTC().Add(14 * time.Hour).Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -27)
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"to", &to}, {"from", &from}} {
		v := q.Get(f.name)
		if v == "" {
			if f.name == "to" {
				from = to.AddDate(0, 0, -27)
			}
			continue
		}
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.name+" should be a date like 2026-10-24")
			return
		}
		*f.dst = t
		if f.name == "to" {
			from = to.AddDate(0, 0, -27)
		}
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to, and at most a year before it")
		return
	}
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
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
	p := paidads.PerformanceQuery{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, From: from, To: to, Location: loc, Customers: "all", Connector: q.Get("connector"), Econ: econ}
	if p.Rates, err = paidads.LoadRates(r.Context(), s.pool); err != nil {
		s.internal(w, r, err)
		return
	}
	rows, sum, err := paidads.CreativeResults(r.Context(), s.pool, p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from.Format(time.DateOnly), "to": to.Format(time.DateOnly), "currency": econ.Currency,
		"target_cost_per_payer_micros": econ.TargetCostPerPayerMicros, "summary": sum, "data": rows})
}

// adsBusiness is the whole business over a range: every payment against
// every network's spend, what each network claims against what Outflare
// counted, and whether each month's new customers paid back their cost.
func (s *Server) adsBusiness(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	to := time.Now().UTC().Add(14 * time.Hour).Truncate(24 * time.Hour)
	from := to.AddDate(0, 0, -27)
	for _, f := range []struct {
		name string
		dst  *time.Time
	}{{"to", &to}, {"from", &from}} {
		v := q.Get(f.name)
		if v == "" {
			continue
		}
		t, err := time.Parse(time.DateOnly, v)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "invalid_request", f.name+" should be a date like 2026-10-24")
			return
		}
		*f.dst = t
		if f.name == "to" {
			from = to.AddDate(0, 0, -27)
		}
	}
	if to.Before(from) || to.Sub(from) > 366*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from must be on or before to, and at most a year before it")
		return
	}
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
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
	p := paidads.BusinessQuery{WorkspaceID: a.WorkspaceID, BrandID: a.Brand, From: from, To: to, Location: loc, Econ: econ}
	if p.Rates, err = paidads.LoadRates(r.Context(), s.pool); err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := paidads.WholeBusiness(r.Context(), s.pool, p)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		From       string   `json:"from"`
		To         string   `json:"to"`
		TargetROAS *float64 `json:"target_roas"`
		CohortDays []int    `json:"cohort_days"`
		paidads.Business
	}{from.Format(time.DateOnly), to.Format(time.DateOnly), econ.TargetROAS, paidads.CohortDays, out})
}
