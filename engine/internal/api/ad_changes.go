package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Managing campaigns (the brand agent plan, step 7): a person, or an agent
// within the grants a person set, pauses or resumes a campaign and sets
// its daily budget on the workspace's own ad accounts. That and adding the
// tracking template (ad_tracking_fix.go) are all
// Outflare changes about ads. Each request makes an ad_changes row with
// its audit event and queues the job that calls the platform (adsync,
// JobChange): the call is the side effect, so it never runs here. The
// job reads the campaign back afterwards and records whether the
// platform shows the change. For an agent, the action layer holds a
// budget raise above a fifth and any change that would put the brand's
// campaigns over the grant's spend caps (actions.go, adsHold).

func (s *Server) routesAdChanges(mux *http.ServeMux) {
	manage := auth.ScopeAdsManage
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/campaigns/{id}/pause", s.inBrand(manage, s.pauseAdCampaign))
	mux.HandleFunc("POST /v1/workspaces/{ws}/ads/campaigns/{id}/resume", s.inBrand(manage, s.resumeAdCampaign))
	mux.HandleFunc("PATCH /v1/workspaces/{ws}/ads/campaigns/{id}", s.inBrand(manage, s.updateAdCampaign))
	mux.HandleFunc("GET /v1/workspaces/{ws}/ads/changes", s.inBrand(auth.ScopeAnalyticsRead, s.listAdChanges))
	// The kill switch: every active campaign of a brand, paused at once.
	mux.HandleFunc("POST /v1/brands/{brand}/ads/pause-all", s.onBrand(manage, s.pauseAllAds))
}

// adChangeOut is one change as the API shows it.
type adChangeOut struct {
	ID           string             `json:"id"`
	CampaignID   string             `json:"campaign_id"`
	CampaignName string             `json:"campaign_name"`
	Connector    string             `json:"connector"`
	AccountName  string             `json:"account_name"`
	BrandID      *string            `json:"brand_id"`
	Kind         string             `json:"kind"`
	Before       adsync.ChangeState `json:"before"`
	After        adsync.ChangeState `json:"after"`
	Status       string             `json:"status"`
	Error        string             `json:"error"`
	VerifiedAt   *time.Time         `json:"verified_at"`
	VerifyNote   string             `json:"verify_note"`
	ActorID      *string            `json:"actor_id"`
	ActionID     *string            `json:"action_id"`
	BatchID      *string            `json:"batch_id"`
	CreatedAt    time.Time          `json:"created_at"`
	AppliedAt    *time.Time         `json:"applied_at"`
}

const adChangeCols = `c.id, c.ad_entity_id, e.name, a.connector, a.name, c.brand_id, c.kind, c.before, c.after, c.status, c.error, c.verified_at, c.verify_note,
	c.actor_id, c.action_id, c.batch_id, c.created_at, c.applied_at`

const adChangeFrom = `FROM ad_changes c JOIN ad_entities e ON e.id = c.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id`

func scanAdChange(row pgx.Row) (adChangeOut, error) {
	var v adChangeOut
	var before, after []byte
	err := row.Scan(&v.ID, &v.CampaignID, &v.CampaignName, &v.Connector, &v.AccountName, &v.BrandID, &v.Kind, &before, &after, &v.Status, &v.Error,
		&v.VerifiedAt, &v.VerifyNote, &v.ActorID, &v.ActionID, &v.BatchID, &v.CreatedAt, &v.AppliedAt)
	if err != nil {
		return v, err
	}
	_ = json.Unmarshal(before, &v.Before)
	_ = json.Unmarshal(after, &v.After)
	return v, nil
}

// adCampaignRow is a campaign as a change reads it, with its account.
type adCampaignRow struct {
	id, externalID, name, status string
	budget                       *int64
	brand                        *string
	accountID, connector         string
	accountStatus, integrationID string
}

// loadAdCampaign reads one of the workspace's campaigns the caller may
// change, answering the error itself when it can't.
func (s *Server) loadAdCampaign(w http.ResponseWriter, r *http.Request, q pgx.Tx, a *access, id string) (adCampaignRow, bool) {
	var c adCampaignRow
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
		return c, false
	}
	var querier interface {
		QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	} = s.pool
	if q != nil {
		querier = q
	}
	err := querier.QueryRow(r.Context(), `SELECT e.id, e.external_id, e.name, e.status, e.daily_budget_micros, COALESCE(e.brand_id, a.brand_id), a.id, a.connector, a.status, a.integration_id
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'campaign'`, a.WorkspaceID, id).
		Scan(&c.id, &c.externalID, &c.name, &c.status, &c.budget, &c.brand, &c.accountID, &c.connector, &c.accountStatus, &c.integrationID)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !a.sees(c.brand)) {
		writeError(w, r, http.StatusNotFound, "not_found", "campaign not found")
		return c, false
	}
	if err != nil {
		s.internal(w, r, err)
		return c, false
	}
	if !a.writes(c.brand) {
		writeError(w, r, http.StatusForbidden, "forbidden", "you can only change campaigns of your own brands")
		return c, false
	}
	if c.accountStatus != "active" {
		writeError(w, r, http.StatusConflict, "not_active", "the sign-in no longer sees this campaign's ad account")
		return c, false
	}
	return c, true
}

// adChangeQueued answers 409 when a change of the kind is already queued
// for the campaign, so a retry or an impatient second click queues one
// change, not two.
func (s *Server) adChangeQueued(w http.ResponseWriter, r *http.Request, campaignID, kind string) bool {
	var queued bool
	if err := s.pool.QueryRow(r.Context(), `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = $2 AND status = 'queued')`, campaignID, kind).Scan(&queued); err != nil {
		s.internal(w, r, err)
		return true
	}
	if queued {
		writeError(w, r, http.StatusConflict, "not_active", "a "+kind+" of this campaign is already queued; GET .../ads/changes shows it")
		return true
	}
	return false
}

// adsManagerFor checks that the campaign's platform connection can change
// campaigns, without calling it.
func (s *Server) adsManagerFor(w http.ResponseWriter, r *http.Request, ws, integrationID string) bool {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign's ad platform is no longer connected")
		return false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return false
	}
	if _, ok := o.Connector.(connectors.AdsManager); !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" campaigns can be read here but not changed yet")
		return false
	}
	return true
}

// changeAdCampaign records one change for a campaign in tx, with its
// audit event, and queues the job that makes it.
func (s *Server) changeAdCampaign(ctx context.Context, tx pgx.Tx, a *access, c adCampaignRow, kind string, after adsync.ChangeState, batch *string) (adChangeOut, error) {
	before := adsync.ChangeState{Status: c.status, DailyBudgetMicros: c.budget}
	beforeJSON, _ := json.Marshal(before)
	afterJSON, _ := json.Marshal(after)
	var actor, action *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	if id := actionctx.ID(ctx); id != "" {
		action = &id
	}
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, actor_id, action_id, batch_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) RETURNING id`,
		a.WorkspaceID, c.brand, c.id, kind, beforeJSON, afterJSON, actor, action, batch).Scan(&id); err != nil {
		return adChangeOut{}, err
	}
	if err := adsync.EnqueueChange(ctx, tx, a.WorkspaceID, id); err != nil {
		return adChangeOut{}, err
	}
	actionName := map[string]string{adsync.ChangePause: "ad_campaign.paused", adsync.ChangeResume: "ad_campaign.resumed", adsync.ChangeBudget: "ad_campaign.budget_changed",
		adsync.ChangeKeywordAdd: "ad_campaign.keyword_added", adsync.ChangeNegativeAdd: "ad_campaign.negative_added", adsync.ChangeKeywordSplit: "ad_campaign.keyword_split",
		adsync.ChangeBidding: "ad_campaign.bidding_changed", adsync.ChangeAudienceAdd: "ad_campaign.audience_added", adsync.ChangeAudienceRemove: "ad_campaign.audience_removed",
		adsync.ChangeCountryAdd: "ad_campaign.country_added", adsync.ChangeCountryRemove: "ad_campaign.country_removed"}[kind]
	data := map[string]any{"change_id": id, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand,
		"before": before, "after": after}
	props := map[string]any{"connector": c.connector, "kind": kind}
	if after.DailyBudgetMicros != nil {
		props["daily_budget_micros"] = *after.DailyBudgetMicros
	}
	if after.MatchType != "" {
		props["match_type"] = after.MatchType
	}
	if after.Bidding != nil {
		props["strategy"] = after.Bidding.Strategy
	}
	if after.Country != "" {
		props["country"] = after.Country
	}
	if batch != nil {
		data["batch_id"], props["batch"] = *batch, true
	}
	if err := recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, actionName, "ad_campaign", c.id, data, props); err != nil {
		return adChangeOut{}, err
	}
	return scanAdChange(tx.QueryRow(ctx, `SELECT `+adChangeCols+` `+adChangeFrom+` WHERE c.id = $1`, id))
}

func (s *Server) pauseAdCampaign(w http.ResponseWriter, r *http.Request) {
	s.setAdCampaignStatus(w, r, false)
}

func (s *Server) resumeAdCampaign(w http.ResponseWriter, r *http.Request) {
	s.setAdCampaignStatus(w, r, true)
}

func (s *Server) setAdCampaignStatus(w http.ResponseWriter, r *http.Request, active bool) {
	a := accessFrom(r.Context())
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	kind, want := adsync.ChangePause, connectors.AdPaused
	if active {
		kind, want = adsync.ChangeResume, connectors.AdActive
	}
	switch {
	case c.status == connectors.AdRemoved:
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	case c.status == want:
		writeError(w, r, http.StatusConflict, "not_active", "the campaign is already "+want)
		return
	}
	if s.adChangeQueued(w, r, c.id, kind) || !s.adsManagerFor(w, r, a.WorkspaceID, c.integrationID) {
		return
	}
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.changeAdCampaign(r.Context(), tx, a, c, kind, adsync.ChangeState{Status: want}, nil)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}

// adBudgetMax is the most a daily budget can be set to here: one million
// in the account's currency. A typo of that size is refused.
const adBudgetMax int64 = 1_000_000 * 1_000_000

func (s *Server) updateAdCampaign(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var in struct {
		DailyBudgetMicros *int64 `json:"daily_budget_micros"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.DailyBudgetMicros == nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_budget_micros is required: the campaign's daily budget in micros of its account's currency")
		return
	}
	if *in.DailyBudgetMicros < 10_000 || *in.DailyBudgetMicros > adBudgetMax {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "daily_budget_micros is at least 10000 (a cent) and at most 1000000000000 (a million)")
		return
	}
	c, ok := s.loadAdCampaign(w, r, nil, a, r.PathValue("id"))
	if !ok {
		return
	}
	if c.status == connectors.AdRemoved {
		writeError(w, r, http.StatusConflict, "not_active", "the campaign was removed on the platform")
		return
	}
	if s.adChangeQueued(w, r, c.id, adsync.ChangeBudget) || !s.adsManagerFor(w, r, a.WorkspaceID, c.integrationID) {
		return
	}
	var out adChangeOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = s.changeAdCampaign(r.Context(), tx, a, c, adsync.ChangeBudget, adsync.ChangeState{DailyBudgetMicros: in.DailyBudgetMicros}, nil)
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "change": out})
}

// pauseAllAds pauses every active campaign of the brand, on every platform
// that can be changed, as one batch of changes.
func (s *Server) pauseAllAds(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ctx := r.Context()
	rows, err := s.pool.Query(ctx, `SELECT e.id, e.external_id, e.name, e.status, e.daily_budget_micros, COALESCE(e.brand_id, a.brand_id), a.id, a.connector, a.status, a.integration_id
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE e.workspace_id = $1 AND e.level = 'campaign' AND e.status = 'active' AND a.status = 'active' AND COALESCE(e.brand_id, a.brand_id) = $2
		ORDER BY a.connector, e.name`, a.WorkspaceID, a.Brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	camps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adCampaignRow, error) {
		var c adCampaignRow
		err := row.Scan(&c.id, &c.externalID, &c.name, &c.status, &c.budget, &c.brand, &c.accountID, &c.connector, &c.accountStatus, &c.integrationID)
		return c, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// Only platforms that can be changed; the others are reported, not
	// silently skipped.
	manageable := map[string]bool{}
	skipped := []string{}
	for _, c := range camps {
		if _, seen := manageable[c.integrationID]; seen {
			continue
		}
		o, err := s.integrations.Open(ctx, s.pool, a.WorkspaceID, c.integrationID)
		if err != nil {
			manageable[c.integrationID] = false
			continue
		}
		_, ok := o.Connector.(connectors.AdsManager)
		manageable[c.integrationID] = ok
		if !ok {
			skipped = append(skipped, o.Connector.Definition().Name)
		}
	}
	batch := newID()
	out := []adChangeOut{}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		out = out[:0]
		for _, c := range camps {
			if !manageable[c.integrationID] {
				continue
			}
			v, err := s.changeAdCampaign(ctx, tx, a, c, adsync.ChangePause, adsync.ChangeState{Status: connectors.AdPaused}, &batch)
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "ads.paused_all", "brand", a.Brand,
			map[string]any{"batch_id": batch, "brand_id": a.Brand, "campaigns": len(out), "skipped_platforms": skipped},
			map[string]any{"campaigns": len(out), "skipped_platforms": len(skipped)})
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"queued": true, "batch_id": batch, "changes": out, "skipped_platforms": skipped})
}

func (s *Server) listAdChanges(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	args := []any{a.WorkspaceID}
	where := `c.workspace_id = $1`
	if v := r.URL.Query().Get("campaign_id"); v != "" {
		if !isUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "campaign_id must be a campaign's id")
			return
		}
		args = append(args, v)
		where += ` AND c.ad_entity_id = $` + strconv.Itoa(len(args))
	}
	if v := r.URL.Query().Get("connector"); v != "" {
		args = append(args, v)
		where += ` AND a.connector = $` + strconv.Itoa(len(args))
	}
	if v := r.URL.Query().Get("account_id"); v != "" {
		if !isUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an ad account's id, from GET .../ads/accounts")
			return
		}
		args = append(args, v)
		where += ` AND a.id = $` + strconv.Itoa(len(args))
	}
	if v := r.URL.Query().Get("status"); v != "" {
		if v != "queued" && v != "applied" && v != "failed" {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is queued, applied or failed")
			return
		}
		args = append(args, v)
		where += ` AND c.status = $` + strconv.Itoa(len(args))
	}
	where += brand.sql(`c.brand_id`, &args)
	before, ok := beforeTime(w, r)
	if !ok {
		return
	}
	limit, ok := pageLimit(w, r, 50, 500)
	if !ok {
		return
	}
	args = append(args, before)
	where += ` AND ($` + strconv.Itoa(len(args)) + `::timestamptz IS NULL OR c.created_at < $` + strconv.Itoa(len(args)) + `)`
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+adChangeCols+` `+adChangeFrom+` WHERE `+where+` ORDER BY c.created_at DESC, c.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adChangeOut, error) { return scanAdChange(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []adChangeOut{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].CreatedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}
