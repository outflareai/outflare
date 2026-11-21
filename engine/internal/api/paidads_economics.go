package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

// Paid ads economics (plans/paid-ads/plan.md): the brand's own numbers
// that every finding, verdict and report is scored in. Reads are
// analytics.read; changing them is analytics.manage, and turning
// shadow_mode off (letting rules' findings go live) is a person's to do.
// Nothing here lets a change apply without a person: only an extension's
// hook can (paidads.AutoApprove, Host.SetAdAutoApprove).

func (s *Server) routesPaidAds(mux *http.ServeMux) {
	read := auth.ScopeAnalyticsRead
	mux.HandleFunc("GET /v1/brands/{brand}/ads/economics", s.onBrand(read, s.getAdEconomics))
	mux.HandleFunc("PUT /v1/brands/{brand}/ads/economics", s.onBrand(auth.ScopeAnalyticsManage, s.setAdEconomics))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/performance", s.onBrand(read, s.adsPerformance))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/acquisitions", s.onBrand(auth.ScopeCustomersRead, s.listAdAcquisitions))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/setup", s.onBrand(read, s.adsSetup))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/match-check", s.onBrand(read, s.adsMatchCheck))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/creatives/results", s.onBrand(read, s.adsCreativeResults))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/business", s.onBrand(read, s.adsBusiness))
}

var economicsCurrency = regexp.MustCompile(`^[A-Z]{3}$`)

func (s *Server) getAdEconomics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	e, err := paidads.Load(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if errors.Is(err, paidads.ErrNoBrand) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// setAdEconomics changes what is sent and keeps the rest. A field sent as
// null clears it where it can be empty (the targets, the margin).
func (s *Server) setAdEconomics(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	// The body is read as a map first, so a field sent as null is told
	// apart from one left out.
	var raw map[string]json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	var in struct {
		Currency                   *string   `json:"currency"`
		TargetCostPerPayerMicros   *int64    `json:"target_cost_per_payer_micros"`
		TargetCostPerSignupMicros  *int64    `json:"target_cost_per_signup_micros"`
		AcquisitionWindowDays      *int      `json:"acquisition_window_days"`
		PaybackMonths              *int      `json:"payback_months"`
		GrossMarginPercent         *int      `json:"gross_margin_percent"`
		ShadowMode                 *bool     `json:"shadow_mode"`
		DailyBudgetCeilingMicros   *int64    `json:"daily_budget_ceiling_micros"`
		TargetNewPayersMonthly     *int      `json:"target_new_payers_monthly"`
		MonthlyBudgetMicros        *int64    `json:"monthly_budget_micros"`
		TargetRevenueMonthlyMicros *int64    `json:"target_revenue_monthly_micros"`
		TargetROAS                 *float64  `json:"target_roas"`
		OverviewNumbers            *[]string `json:"overview_numbers"`
		sent                       map[string]bool
	}
	body, _ := json.Marshal(raw)
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "invalid JSON: "+err.Error())
		return
	}
	in.sent = map[string]bool{}
	for k := range raw {
		in.sent[k] = true
	}
	cur, err := paidads.Load(r.Context(), s.pool, a.WorkspaceID, a.Brand)
	if errors.Is(err, paidads.ErrNoBrand) {
		writeError(w, r, http.StatusNotFound, "not_found", "brand not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	next := cur
	changed := map[string]any{}
	if in.Currency != nil {
		c := strings.ToUpper(strings.TrimSpace(*in.Currency))
		if !economicsCurrency.MatchString(c) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "currency is a three-letter code such as USD")
			return
		}
		next.Currency, changed["currency"] = c, c
	}
	if in.sent["target_cost_per_payer_micros"] {
		if in.TargetCostPerPayerMicros != nil && *in.TargetCostPerPayerMicros <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_cost_per_payer_micros is above zero, in micros of the currency, or null to clear it")
			return
		}
		next.TargetCostPerPayerMicros, changed["target_cost_per_payer_micros"] = in.TargetCostPerPayerMicros, in.TargetCostPerPayerMicros
	}
	if in.sent["target_cost_per_signup_micros"] {
		if in.TargetCostPerSignupMicros != nil && *in.TargetCostPerSignupMicros <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_cost_per_signup_micros is above zero, in micros of the currency, or null to clear it")
			return
		}
		next.TargetCostPerSignupMicros, changed["target_cost_per_signup_micros"] = in.TargetCostPerSignupMicros, in.TargetCostPerSignupMicros
	}
	if in.AcquisitionWindowDays != nil {
		if *in.AcquisitionWindowDays < 1 || *in.AcquisitionWindowDays > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "acquisition_window_days is 1 to 365")
			return
		}
		next.AcquisitionWindowDays, changed["acquisition_window_days"] = *in.AcquisitionWindowDays, *in.AcquisitionWindowDays
	}
	if in.PaybackMonths != nil {
		if *in.PaybackMonths < 1 || *in.PaybackMonths > 60 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "payback_months is 1 to 60")
			return
		}
		next.PaybackMonths, changed["payback_months"] = *in.PaybackMonths, *in.PaybackMonths
	}
	if in.sent["gross_margin_percent"] {
		if in.GrossMarginPercent != nil && (*in.GrossMarginPercent < 1 || *in.GrossMarginPercent > 100) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "gross_margin_percent is 1 to 100, or null to clear it")
			return
		}
		next.GrossMarginPercent, changed["gross_margin_percent"] = in.GrossMarginPercent, in.GrossMarginPercent
	}
	// The Overview's other targets: what the brand aims for in a month,
	// each cleared with null.
	if in.sent["target_new_payers_monthly"] {
		if v := in.TargetNewPayersMonthly; v != nil && (*v <= 0 || *v > 100_000_000) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_new_payers_monthly is above zero, or null to clear it")
			return
		}
		next.TargetNewPayersMonthly, changed["target_new_payers_monthly"] = in.TargetNewPayersMonthly, in.TargetNewPayersMonthly
	}
	if in.sent["monthly_budget_micros"] {
		if v := in.MonthlyBudgetMicros; v != nil && *v <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "monthly_budget_micros is above zero, in micros of the currency, or null to clear it")
			return
		}
		next.MonthlyBudgetMicros, changed["monthly_budget_micros"] = in.MonthlyBudgetMicros, in.MonthlyBudgetMicros
	}
	if in.sent["target_revenue_monthly_micros"] {
		if v := in.TargetRevenueMonthlyMicros; v != nil && *v <= 0 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "target_revenue_monthly_micros is above zero, in micros of the currency, or null to clear it")
			return
		}
		next.TargetRevenueMonthlyMicros, changed["target_revenue_monthly_micros"] = in.TargetRevenueMonthlyMicros, in.TargetRevenueMonthlyMicros
	}
	if in.sent["target_roas"] {
		if v := in.TargetROAS; v != nil {
			if *v <= 0 || *v > 1000 {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "target_roas is revenue over spend, above zero and at most 1000 (3 means three times the spend), or null to clear it")
				return
			}
			rounded := math.Round(*v*100) / 100
			in.TargetROAS = &rounded
		}
		next.TargetROAS, changed["target_roas"] = in.TargetROAS, in.TargetROAS
	}
	if in.OverviewNumbers != nil {
		list, seen := []string{}, map[string]bool{}
		for _, id := range *in.OverviewNumbers {
			if !slices.Contains(paidads.OverviewNumberIDs, id) || seen[id] {
				writeError(w, r, http.StatusBadRequest, "invalid_request", "overview_numbers lists each number once, from: "+strings.Join(paidads.OverviewNumberIDs, ", "))
				return
			}
			seen[id] = true
			list = append(list, id)
		}
		if len(list) > 8 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "overview_numbers holds at most 8 numbers; an empty list shows the usual six")
			return
		}
		next.OverviewNumbers, changed["overview_numbers"] = list, list
	}
	// The ceiling is the owner's guard on what ads can cost in a day, so
	// only a person raises or removes it; anyone may set or lower it.
	if in.sent["daily_budget_ceiling_micros"] {
		nc := in.DailyBudgetCeilingMicros
		if nc != nil && (*nc <= 0 || *nc > adBudgetMax) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_budget_ceiling_micros is above zero and at most 1000000000000, in micros of the currency, or null for no ceiling")
			return
		}
		looser := cur.DailyBudgetCeilingMicros != nil && (nc == nil || *nc > *cur.DailyBudgetCeilingMicros)
		if looser && (a.ActorType != "human" || a.TokenID != "" || a.ViewID != "") {
			writeError(w, r, http.StatusForbidden, "person_only", "only a person, signed in, raises or removes the daily budget ceiling: ask them to do it in Outflare under Paid ads, Setup")
			return
		}
		next.DailyBudgetCeilingMicros, changed["daily_budget_ceiling_micros"] = nc, nc
	}
	// Letting rules' findings go live is a person's decision: turning
	// shadow mode off needs a signed-in person, never a token or an agent.
	if in.ShadowMode != nil && *in.ShadowMode != cur.ShadowMode {
		if !*in.ShadowMode && a.ActorType != "human" {
			writeError(w, r, http.StatusForbidden, "person_only", "only a person, signed in, turns shadow_mode off: ask them to do it in Outflare under Paid ads, Set up")
			return
		}
		next.ShadowMode, changed["shadow_mode"] = *in.ShadowMode, *in.ShadowMode
	}
	var actor *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	var out paidads.Economics
	err = pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		if out, err = paidads.Save(r.Context(), tx, next, actor); err != nil {
			return err
		}
		fields := make([]string, 0, len(changed))
		for k := range changed {
			fields = append(fields, k)
		}
		return recordWithProps(r.Context(), tx, &a.principal, a.WorkspaceID, "ad_economics.updated", "brand", a.Brand, changed,
			map[string]any{"brand_id": a.Brand, "fields": fields, "has_target": out.HasTarget(), "shadow_mode": out.ShadowMode,
				"acquisition_window_days": out.AcquisitionWindowDays, "payback_months": out.PaybackMonths})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}
