package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// What Google's Required Minimum Functionality asks of a full-service tool
// beside the campaign changes elsewhere (docs/paid-media.md): an account's
// conversion actions and a new website one with its tag (C.65), its own
// callouts (C.75), its portfolio Target CPA and Target ROAS strategies and
// their report (C.96, C.97, M.96, M.97, R.130), and removing a campaign,
// an ad or a keyword (M.110, M.130, M.140).
//
// Lists and the strategy report read the platform live. A change is a row
// queued with its audit event and carried out by the ad sync's change job,
// which reads it back: an account's in ad_account_changes, a removal in
// ad_changes like a pause. An agent's account change always waits for a
// person (actions.go, adsHold). Removing can't be undone on the platform,
// so it takes a person's own signed-in session and confirm: true; the
// action layer refuses an agent's outright.

func (s *Server) routesAdRMF(mux *http.ServeMux) {
	read, manage := auth.ScopeAnalyticsRead, auth.ScopeAdsManage
	acct := "/v1/brands/{brand}/ads/accounts/{id}"
	mux.HandleFunc("GET "+acct+"/conversion-actions", s.onBrand(read, s.listAdAccountConversions))
	mux.HandleFunc("POST "+acct+"/conversion-actions", s.onBrand(manage, s.createAdAccountConversion))
	mux.HandleFunc("GET "+acct+"/callouts", s.onBrand(read, s.listAdCallouts))
	mux.HandleFunc("POST "+acct+"/callouts", s.onBrand(manage, s.addAdCallout))
	mux.HandleFunc("DELETE "+acct+"/callouts/{asset}", s.onBrand(manage, s.removeAdCallout))
	mux.HandleFunc("GET "+acct+"/bidding-strategies", s.onBrand(read, s.listAdPortfolios))
	mux.HandleFunc("POST "+acct+"/bidding-strategies", s.onBrand(manage, s.createAdPortfolio))
	mux.HandleFunc("PATCH "+acct+"/bidding-strategies/{strategy}", s.onBrand(manage, s.updateAdPortfolio))
	mux.HandleFunc("GET "+acct+"/changes", s.onBrand(read, s.listAdAccountChanges))
	mux.HandleFunc("GET "+acct+"/changes/{change}", s.onBrand(read, s.getAdAccountChange))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/bidding-strategies", s.onBrand(read, s.adPortfolioReport))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/campaigns/{id}/remove", s.onBrand(manage, s.removeAdCampaign))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/items/{id}/remove", s.onBrand(manage, s.removeAdItem))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/keywords/{id}/remove", s.onBrand(manage, s.removeAdKeyword))
}

// ---- reading an account live ----

// adAccountOpen opens an ad account of the route's brand on its platform
// as the interface T, answering the error itself: 404 for an account the
// brand hasn't, 409 when its connection is gone, 422 connector_invalid
// when the platform can't do this here.
func adAccountOpen[T any](s *Server, w http.ResponseWriter, r *http.Request, what string) (adFeedAccount, *connectors.Opened, T, bool) {
	var zero T
	if !s.needIntegrations(w, r) {
		return adFeedAccount{}, nil, zero, false
	}
	a := accessFrom(r.Context())
	acct, ok := s.adFeedAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return acct, nil, zero, false
	}
	o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, acct.integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the ad account's platform is no longer connected")
		return acct, nil, zero, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return acct, nil, zero, false
	}
	t, ok := o.Connector.(T)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" "+what+" can't be managed here: Google Ads only")
		return acct, nil, zero, false
	}
	return acct, o, t, true
}

func (s *Server) listAdAccountConversions(w http.ResponseWriter, r *http.Request) {
	acct, o, m, ok := adAccountOpen[connectors.AdConversionManager](s, w, r, "conversion actions")
	if !ok {
		return
	}
	list, err := m.AdConversionActions(r.Context(), o.Client, o.Creds, acct.acct)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.AdConversionAction{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "categories": connectors.ConvCategories})
}

func (s *Server) listAdCallouts(w http.ResponseWriter, r *http.Request) {
	acct, o, m, ok := adAccountOpen[connectors.AdCalloutManager](s, w, r, "callouts")
	if !ok {
		return
	}
	list, err := m.AdCallouts(r.Context(), o.Client, o.Creds, acct.acct)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.AdCallout{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list, "max_length": connectors.CalloutMax})
}

func (s *Server) listAdPortfolios(w http.ResponseWriter, r *http.Request) {
	acct, o, m, ok := adAccountOpen[connectors.AdPortfolioManager](s, w, r, "bidding strategies")
	if !ok {
		return
	}
	list, err := m.AdPortfolios(r.Context(), o.Client, o.Creds, acct.acct)
	if err != nil {
		s.connectorFailed(w, r, err)
		return
	}
	if list == nil {
		list = []connectors.AdPortfolio{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// ---- account changes ----

type adAccountChangeOut struct {
	ID          string                `json:"id"`
	AdAccountID string                `json:"ad_account_id"`
	Connector   string                `json:"connector"`
	AccountName string                `json:"account_name"`
	BrandID     *string               `json:"brand_id"`
	Kind        string                `json:"kind"`
	Request     adsync.AccountRequest `json:"request"`
	Result      adsync.AccountResult  `json:"result"`
	Status      string                `json:"status"`
	Error       string                `json:"error"`
	VerifiedAt  *time.Time            `json:"verified_at"`
	VerifyNote  string                `json:"verify_note"`
	ActorID     *string               `json:"actor_id"`
	ActionID    *string               `json:"action_id"`
	CreatedAt   time.Time             `json:"created_at"`
	AppliedAt   *time.Time            `json:"applied_at"`
}

const adAccountChangeCols = `c.id, c.ad_account_id, a.connector, a.name, c.brand_id, c.kind, c.request, c.result, c.status, c.error, c.verified_at, c.verify_note,
	c.actor_id, c.action_id, c.created_at, c.applied_at`

func scanAdAccountChange(row pgx.Row) (adAccountChangeOut, error) {
	var v adAccountChangeOut
	var req, res []byte
	err := row.Scan(&v.ID, &v.AdAccountID, &v.Connector, &v.AccountName, &v.BrandID, &v.Kind, &req, &res, &v.Status, &v.Error, &v.VerifiedAt, &v.VerifyNote,
		&v.ActorID, &v.ActionID, &v.CreatedAt, &v.AppliedAt)
	if err != nil {
		return v, err
	}
	_ = json.Unmarshal(req, &v.Request)
	_ = json.Unmarshal(res, &v.Result)
	return v, nil
}

// adAccountActions are the audit actions (and product events) each
// account change records.
var adAccountActions = map[string]string{
	adsync.AccountConversionCreate: "ad_conversion_action.created",
	adsync.AccountCalloutAdd:       "ad_callout.added",
	adsync.AccountCalloutRemove:    "ad_callout.removed",
	adsync.AccountPortfolioCreate:  "ad_bidding_strategy.created",
	adsync.AccountPortfolioUpdate:  "ad_bidding_strategy.updated",
}

// queueAdAccountChange records one account change with its audit event,
// queues the job that makes it and answers 202. props are the event's
// properties beyond the connector and kind: ids and enums only.
func (s *Server) queueAdAccountChange(w http.ResponseWriter, r *http.Request, acct adFeedAccount, kind string, req adsync.AccountRequest, targetID string, props map[string]any) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	reqJSON, _ := json.Marshal(req)
	var actor, action *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	if id := actionctx.ID(ctx); id != "" {
		action = &id
	}
	var brand *string
	if acct.brand != "" {
		brand = &acct.brand
	}
	var out adAccountChangeOut
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ad_account_changes (workspace_id, brand_id, ad_account_id, kind, request, actor_id, action_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, a.WorkspaceID, brand, acct.id, kind, reqJSON, actor, action).Scan(&id); err != nil {
			return err
		}
		if err := adsync.EnqueueAccountChange(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		p := map[string]any{"connector": acct.connector, "kind": kind, "brand_id": brand}
		for k, v := range props {
			p[k] = v
		}
		target := "ad_account"
		if targetID == "" {
			targetID = acct.id
		} else {
			target = map[string]string{adsync.AccountCalloutRemove: "ad_callout", adsync.AccountPortfolioUpdate: "ad_bidding_strategy"}[kind]
		}
		data := map[string]any{"change_id": id, "ad_account_id": acct.id, "connector": acct.connector, "brand_id": brand, "request": req}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, adAccountActions[kind], target, targetID, data, p); err != nil {
			return err
		}
		var err error
		out, err = scanAdAccountChange(tx.QueryRow(ctx, `SELECT `+adAccountChangeCols+` FROM ad_account_changes c JOIN ad_accounts a ON a.id = c.ad_account_id WHERE c.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}

// adAccountChangeQueued answers 409 when the same change is already
// queued for the account, so a double click queues one.
func (s *Server) adAccountChangeQueued(w http.ResponseWriter, r *http.Request, acctID, kind string, req adsync.AccountRequest) bool {
	reqJSON, _ := json.Marshal(req)
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_account_changes WHERE ad_account_id = $1 AND kind = $2 AND status = 'queued' AND request = $3::jsonb)`,
		acctID, kind, reqJSON).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return true
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "the same change is already queued for this account; GET .../changes shows it")
		return true
	}
	return false
}

func (s *Server) createAdAccountConversion(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name               string `json:"name"`
		Category           string `json:"category"`
		Counting           string `json:"counting"`
		ClickWindowDays    *int   `json:"click_window_days"`
		DefaultValueMicros *int64 `json:"default_value_micros"`
		Currency           string `json:"currency"`
		AlwaysUseDefault   bool   `json:"always_use_default"`
	}
	if !decode(w, r, &in) {
		return
	}
	conv := connectors.AdWebConversion{Name: strings.TrimSpace(in.Name), Category: strings.TrimSpace(in.Category), Counting: strings.TrimSpace(in.Counting),
		ClickWindowDays: 30, DefaultValueMicros: in.DefaultValueMicros, Currency: strings.ToUpper(strings.TrimSpace(in.Currency)), AlwaysUseDefault: in.AlwaysUseDefault}
	if conv.Counting == "" {
		conv.Counting = "one"
	}
	if in.ClickWindowDays != nil {
		conv.ClickWindowDays = *in.ClickWindowDays
	}
	if conv.DefaultValueMicros != nil && *conv.DefaultValueMicros > adBudgetMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "default_value_micros is at most 1000000000000 (a million)")
		return
	}
	acct, _, _, ok := adAccountOpen[connectors.AdConversionManager](s, w, r, "conversion actions")
	if !ok {
		return
	}
	if conv.DefaultValueMicros != nil && conv.Currency == "" {
		conv.Currency = acct.acct.Currency
	}
	if err := connectors.CheckWebConversion(conv); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.(*connectors.Error).Message)
		return
	}
	req := adsync.AccountRequest{Conversion: &conv}
	if s.adAccountChangeQueued(w, r, acct.id, adsync.AccountConversionCreate, req) {
		return
	}
	s.queueAdAccountChange(w, r, acct, adsync.AccountConversionCreate, req, "", map[string]any{"category": conv.Category, "counting": conv.Counting,
		"click_window_days": conv.ClickWindowDays, "has_value": conv.DefaultValueMicros != nil})
}

func (s *Server) addAdCallout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if !decode(w, r, &in) {
		return
	}
	text, err := connectors.CheckCallout(in.Text)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.(*connectors.Error).Message)
		return
	}
	acct, _, _, ok := adAccountOpen[connectors.AdCalloutManager](s, w, r, "callouts")
	if !ok {
		return
	}
	req := adsync.AccountRequest{Text: text}
	if s.adAccountChangeQueued(w, r, acct.id, adsync.AccountCalloutAdd, req) {
		return
	}
	s.queueAdAccountChange(w, r, acct, adsync.AccountCalloutAdd, req, "", map[string]any{"length": len([]rune(text))})
}

// platformID checks an id the platform gave (digits on Google).
func platformID(w http.ResponseWriter, r *http.Request, v, what string) (string, bool) {
	v = strings.TrimSpace(v)
	if _, err := strconv.ParseInt(v, 10, 64); err != nil || len(v) > 30 {
		writeError(w, r, http.StatusNotFound, "not_found", what+" not found")
		return "", false
	}
	return v, true
}

func (s *Server) removeAdCallout(w http.ResponseWriter, r *http.Request) {
	id, ok := platformID(w, r, r.PathValue("asset"), "callout")
	if !ok {
		return
	}
	acct, _, _, ok := adAccountOpen[connectors.AdCalloutManager](s, w, r, "callouts")
	if !ok {
		return
	}
	req := adsync.AccountRequest{CalloutID: id}
	if s.adAccountChangeQueued(w, r, acct.id, adsync.AccountCalloutRemove, req) {
		return
	}
	s.queueAdAccountChange(w, r, acct, adsync.AccountCalloutRemove, req, id, nil)
}

// portfolioTargets checks a strategy's targets as the campaign bidding
// route checks a campaign's.
func portfolioTargets(w http.ResponseWriter, r *http.Request, cpa *int64, roas *float64) bool {
	if cpa != nil && (*cpa < 10_000 || *cpa > adBudgetMax) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "target_cpa_micros is at least 10000 (a cent) and at most 1000000000000 (a million)")
		return false
	}
	if roas != nil && (*roas < 0.01 || *roas > 1000) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "target_roas is a return on spend between 0.01 and 1000 (3 means three times what was spent)")
		return false
	}
	return true
}

func (s *Server) createAdPortfolio(w http.ResponseWriter, r *http.Request) {
	var in connectors.AdPortfolioSpec
	if !decode(w, r, &in) {
		return
	}
	in.Name, in.Type = strings.TrimSpace(in.Name), strings.TrimSpace(in.Type)
	if !portfolioTargets(w, r, in.TargetCPAMicros, in.TargetROAS) {
		return
	}
	if err := connectors.CheckPortfolio(in, true); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.(*connectors.Error).Message)
		return
	}
	acct, _, _, ok := adAccountOpen[connectors.AdPortfolioManager](s, w, r, "bidding strategies")
	if !ok {
		return
	}
	req := adsync.AccountRequest{Portfolio: &in}
	if s.adAccountChangeQueued(w, r, acct.id, adsync.AccountPortfolioCreate, req) {
		return
	}
	s.queueAdAccountChange(w, r, acct, adsync.AccountPortfolioCreate, req, "", map[string]any{"type": in.Type})
}

func (s *Server) updateAdPortfolio(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name            string   `json:"name"`
		TargetCPAMicros *int64   `json:"target_cpa_micros"`
		TargetROAS      *float64 `json:"target_roas"`
	}
	if !decode(w, r, &in) {
		return
	}
	id, ok := platformID(w, r, r.PathValue("strategy"), "bidding strategy")
	if !ok {
		return
	}
	spec := connectors.AdPortfolioSpec{Name: strings.TrimSpace(in.Name), TargetCPAMicros: in.TargetCPAMicros, TargetROAS: in.TargetROAS}
	if !portfolioTargets(w, r, in.TargetCPAMicros, in.TargetROAS) {
		return
	}
	if err := connectors.CheckPortfolio(spec, false); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.(*connectors.Error).Message)
		return
	}
	acct, _, _, ok := adAccountOpen[connectors.AdPortfolioManager](s, w, r, "bidding strategies")
	if !ok {
		return
	}
	req := adsync.AccountRequest{BiddingStrategyID: id, Portfolio: &spec}
	if s.adAccountChangeQueued(w, r, acct.id, adsync.AccountPortfolioUpdate, req) {
		return
	}
	s.queueAdAccountChange(w, r, acct, adsync.AccountPortfolioUpdate, req, id, map[string]any{"target_cpa": in.TargetCPAMicros != nil, "target_roas": in.TargetROAS != nil, "renamed": spec.Name != ""})
}

func (s *Server) listAdAccountChanges(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	acct, ok := s.adFeedAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 200)
	if !ok {
		return
	}
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID, acct.id, limit, before}
	where := ``
	if k := r.URL.Query().Get("kind"); k != "" {
		if adAccountActions[k] == "" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is conversion_action_create, callout_add, callout_remove, bidding_strategy_create or bidding_strategy_update")
			return
		}
		args = append(args, k)
		where = ` AND c.kind = $5`
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+adAccountChangeCols+` FROM ad_account_changes c JOIN ad_accounts a ON a.id = c.ad_account_id
		WHERE c.workspace_id = $1 AND c.ad_account_id = $2 AND ($4::timestamptz IS NULL OR c.created_at < $4)`+where+` ORDER BY c.created_at DESC, c.id DESC LIMIT $3`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adAccountChangeOut, error) { return scanAdAccountChange(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []adAccountChangeOut{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}

func (s *Server) getAdAccountChange(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	acct, ok := s.adFeedAccount(w, r, a, r.PathValue("id"))
	if !ok {
		return
	}
	id := r.PathValue("change")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "change not found")
		return
	}
	out, err := scanAdAccountChange(s.pool.QueryRow(r.Context(), `SELECT `+adAccountChangeCols+` FROM ad_account_changes c JOIN ad_accounts a ON a.id = c.ad_account_id
		WHERE c.workspace_id = $1 AND c.ad_account_id = $2 AND c.id = $3`, a.WorkspaceID, acct.id, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "change not found")
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- the bidding strategy report (R.130) ----

type adPortfolioRow struct {
	connectors.AdPortfolioReport
	AdAccountID string `json:"ad_account_id"`
	AccountName string `json:"account_name"`
	Connector   string `json:"connector"`
}

type adReportError struct {
	AdAccountID string `json:"ad_account_id"`
	Code        string `json:"code"`
	Message     string `json:"message"`
}

// adPortfolioReport reads every portfolio strategy of the brand's ad
// accounts, live, with its numbers over the range. An account the
// platform refuses is listed under errors and the others still show.
func (s *Server) adPortfolioReport(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	a := accessFrom(r.Context())
	args := []any{a.WorkspaceID, a.Brand}
	where := ``
	if id := r.URL.Query().Get("account_id"); id != "" {
		args = append(args, id)
		where = ` AND id = $3`
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id, integration_id, connector, coalesce(brand_id::text, ''), external_id, name, currency, time_zone, meta
		FROM ad_accounts WHERE workspace_id = $1 AND brand_id = $2 AND status = 'active' AND sync_on`+where+` ORDER BY connector, lower(name), id`, args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	accts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adFeedAccount, error) {
		var x adFeedAccount
		var meta map[string]string
		err := row.Scan(&x.id, &x.integrationID, &x.connector, &x.brand, &x.acct.ID, &x.acct.Name, &x.acct.Currency, &x.acct.TimeZone, &meta)
		x.acct.Meta = meta
		return x, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, failed := []adPortfolioRow{}, []adReportError{}
	for _, acct := range accts {
		o, err := s.integrations.Open(r.Context(), s.pool, a.WorkspaceID, acct.integrationID)
		if err != nil {
			failed = append(failed, adReportError{AdAccountID: acct.id, Code: "not_active", Message: "the ad account's platform is no longer connected"})
			continue
		}
		m, ok := o.Connector.(connectors.AdPortfolioManager)
		if !ok {
			continue
		}
		list, err := m.AdPortfolioReport(r.Context(), o.Client, o.Creds, acct.acct, f.from, f.to)
		if err != nil {
			code, msg := "connector_unavailable", err.Error()
			var ce *connectors.Error
			if errors.As(err, &ce) {
				code, msg = map[string]string{connectors.ErrAuth: "connector_auth", connectors.ErrQuota: "connector_quota", connectors.ErrInvalid: "connector_invalid"}[ce.Kind], ce.Message
				if code == "" {
					code = "connector_unavailable"
				}
			}
			failed = append(failed, adReportError{AdAccountID: acct.id, Code: code, Message: trimTo(msg, 300)})
			continue
		}
		for _, x := range list {
			if x.Currency == "" {
				x.Currency = acct.acct.Currency
			}
			out = append(out, adPortfolioRow{AdPortfolioReport: x, AdAccountID: acct.id, AccountName: acct.acct.Name, Connector: acct.connector})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out, "errors": failed,
		"notes": []string{"Read live from each Google Ads account: average CPC and cost per conversion are Google's own, in micros of the strategy's currency."}})
}

// ---- removing a campaign, an ad or a keyword (M.110, M.130, M.140) ----

// personRemoves answers 403 person_only unless the caller is a person on
// their own signed-in session, and 400 unless the body says confirm: true.
func personRemoves(w http.ResponseWriter, r *http.Request, what string) bool {
	a := accessFrom(r.Context())
	if a.ActorType != "human" || a.TokenID != "" || a.ViewID != "" {
		writeError(w, r, http.StatusForbidden, "person_only", "removing a "+what+" can't be undone, so only a person, signed in, removes one: ask them to do it in Outflare under Paid ads")
		return false
	}
	var in struct {
		Confirm bool `json:"confirm"`
	}
	if !decode(w, r, &in) {
		return false
	}
	if !in.Confirm {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "confirm must be true: removing a "+what+" can't be undone in the ad platform")
		return false
	}
	return true
}

// adRemoverFor checks that the campaign's platform can remove things here.
func (s *Server) adRemoverFor(w http.ResponseWriter, r *http.Request, ws, integrationID string) bool {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return false
	}
	if _, ok := o.Connector.(connectors.AdRemover); !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" campaigns, ads and keywords can't be removed here: Google Ads only")
		return false
	}
	return true
}

// queueAdRemove records a removal for the campaign c with its audit event
// and queues the job that makes it. key is the after field that tells two
// removals in one campaign apart (ad_id, keyword_id; "" for the campaign).
func (s *Server) queueAdRemove(w http.ResponseWriter, r *http.Request, c adCampaignRow, kind string, before, after adsync.ChangeState, key, keyValue, action, targetType, targetID string) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	var queued bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = $2 AND status = 'queued' AND ($3 = '' OR after->>$3 = $4))`,
		c.id, kind, key, keyValue).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "this removal is already queued; GET .../ads/changes shows it")
		return
	}
	if !s.adRemoverFor(w, r, a.WorkspaceID, c.integrationID) {
		return
	}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	var actor *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	var out adChangeOut
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, actor_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`, a.WorkspaceID, c.brand, c.id, kind, beforeJSON, afterJSON, actor).Scan(&id); err != nil {
			return err
		}
		if err := adsync.EnqueueChange(ctx, tx, a.WorkspaceID, id); err != nil {
			return err
		}
		data := map[string]any{"change_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand,
			"before": before, "after": after}
		if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, targetType, targetID, data,
			map[string]any{"connector": c.connector, "kind": kind, "brand_id": c.brand}); err != nil {
			return err
		}
		var err error
		out, err = scanAdChange(tx.QueryRow(ctx, `SELECT `+adChangeCols+` `+adChangeFrom+` WHERE c.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}

func (s *Server) removeAdCampaign(w http.ResponseWriter, r *http.Request) {
	if !personRemoves(w, r, "campaign") {
		return
	}
	a := accessFrom(r.Context())
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign is already removed")
		return
	}
	s.queueAdRemove(w, r, c, adsync.ChangeCampaignRemove, adsync.ChangeState{Status: c.status, DailyBudgetMicros: c.budget},
		adsync.ChangeState{Status: connectors.AdRemoved, Name: c.name}, "", "", "ad_campaign.removed", "ad_campaign", c.id)
}

// adChild is an ad or a keyword with its ad group and campaign.
type adChild struct {
	id, externalID, name, status, groupExternal, campaignID string
	extra                                                   string
}

func (s *Server) loadAdChild(ctx context.Context, w http.ResponseWriter, r *http.Request, query, id, what string) (adChild, bool) {
	var x adChild
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", what+" not found")
		return x, false
	}
	var campaign *string
	err := s.pool.QueryRow(ctx, query, accessFrom(ctx).WorkspaceID, id).Scan(&x.id, &x.externalID, &x.name, &x.status, &x.groupExternal, &campaign, &x.extra)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && campaign == nil) {
		writeError(w, r, http.StatusNotFound, "not_found", what+" not found")
		return x, false
	}
	if err != nil {
		s.internal(w, r, err)
		return x, false
	}
	x.campaignID = *campaign
	if x.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the "+what+" is already removed")
		return x, false
	}
	return x, true
}

func (s *Server) removeAdItem(w http.ResponseWriter, r *http.Request) {
	if !personRemoves(w, r, "ad") {
		return
	}
	a := accessFrom(r.Context())
	ad, ok := s.loadAdChild(r.Context(), w, r, `SELECT e.id, e.external_id, e.name, e.status, g.external_id, c.id, e.level
		FROM ad_entities e JOIN ad_entities g ON g.id = e.parent_id AND g.level = 'ad_group' LEFT JOIN ad_entities c ON c.id = g.parent_id AND c.level = 'campaign'
		WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'ad'`, r.PathValue("id"), "ad")
	if !ok {
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, ad.campaignID)
	if !ok {
		return
	}
	s.queueAdRemove(w, r, c, adsync.ChangeAdRemove, adsync.ChangeState{Status: ad.status, AdGroupID: ad.groupExternal, AdID: ad.externalID},
		adsync.ChangeState{Status: connectors.AdRemoved, AdGroupID: ad.groupExternal, AdID: ad.externalID, Name: ad.name},
		"ad_id", ad.externalID, "ad_ad.removed", "ad", ad.id)
}

func (s *Server) removeAdKeyword(w http.ResponseWriter, r *http.Request) {
	if !personRemoves(w, r, "keyword") {
		return
	}
	a := accessFrom(r.Context())
	k, ok := s.loadAdChild(r.Context(), w, r, `SELECT k.id, k.external_id, k.text, k.status, g.external_id, c.id, k.match_type
		FROM ad_keywords k JOIN ad_entities g ON g.id = k.ad_group_id LEFT JOIN ad_entities c ON c.id = g.parent_id AND c.level = 'campaign'
		WHERE k.workspace_id = $1 AND k.id = $2`, r.PathValue("id"), "keyword")
	if !ok {
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, k.campaignID)
	if !ok {
		return
	}
	s.queueAdRemove(w, r, c, adsync.ChangeKeywordRemove,
		adsync.ChangeState{Status: k.status, Keyword: k.name, MatchType: k.extra, AdGroupID: k.groupExternal, KeywordID: k.externalID},
		adsync.ChangeState{Status: connectors.AdRemoved, Keyword: k.name, MatchType: k.extra, AdGroupID: k.groupExternal, KeywordID: k.externalID},
		"keyword_id", k.externalID, "ad_keyword.removed", "ad_keyword", k.id)
}
