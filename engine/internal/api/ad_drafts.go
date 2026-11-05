package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/actionctx"
	"github.com/outflareai/outflare/engine/internal/adsync"
	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// New ads (step 6 of the paid ads plan): the brand agent (or a person)
// writes a draft (a new ad in an ad group, a new ad group with keywords
// and an ad, or a new search campaign) with why it should do better. A
// person approves it, which makes it on the platform paused (the ads.draft
// job, adsync/draft.go), checks it there, and turns it on. Writing a draft
// spends nothing, so an agent does it under its grant; approving and
// turning on are a person's.

func (s *Server) routesAdDrafts(mux *http.ServeMux) {
	manage := auth.ScopeAdsManage
	mux.HandleFunc("GET /v1/brands/{brand}/ads/drafts", s.onBrand(auth.ScopeAnalyticsRead, s.listAdDrafts))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/drafts", s.onBrand(manage, s.createAdDraft))
	mux.HandleFunc("GET /v1/brands/{brand}/ads/drafts/{id}", s.onBrand(auth.ScopeAnalyticsRead, s.getAdDraft))
	mux.HandleFunc("PATCH /v1/brands/{brand}/ads/drafts/{id}", s.onBrand(manage, s.updateAdDraft))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/drafts/{id}/approve", s.onBrand(manage, s.approveAdDraft))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/drafts/{id}/go-live", s.onBrand(manage, s.goLiveAdDraft))
	mux.HandleFunc("POST /v1/brands/{brand}/ads/drafts/{id}/dismiss", s.onBrand(manage, s.dismissAdDraft))
}

type adDraftOut struct {
	ID           string             `json:"id"`
	BrandID      string             `json:"brand_id"`
	AccountID    string             `json:"account_id"`
	AccountName  string             `json:"account_name"`
	Connector    string             `json:"connector"`
	Currency     string             `json:"currency"`
	Kind         string             `json:"kind"`
	CampaignID   *string            `json:"campaign_id"`
	CampaignName *string            `json:"campaign_name"`
	AdGroupID    *string            `json:"ad_group_id"`
	AdGroupName  *string            `json:"ad_group_name"`
	Content      connectors.AdDraft `json:"content"`
	Reason       string             `json:"reason"`
	Status       string             `json:"status"`
	Made         connectors.AdMade  `json:"made"`
	Error        string             `json:"error"`
	CreatedBy    *string            `json:"created_by"`
	ActionID     *string            `json:"action_id"`
	ApprovedBy   *string            `json:"approved_by"`
	ApprovedAt   *time.Time         `json:"approved_at"`
	MadeAt       *time.Time         `json:"made_at"`
	LiveBy       *string            `json:"live_by"`
	LiveAt       *time.Time         `json:"live_at"`
	CreatedAt    time.Time          `json:"created_at"`
	UpdatedAt    time.Time          `json:"updated_at"`
}

const adDraftCols = `d.id, d.brand_id, a.id, a.name, a.connector, a.currency, d.kind, c.id, c.name, g.id, g.name, d.content, d.reason, d.status, d.made, d.error,
	d.created_by, d.action_id, d.approved_by, d.approved_at, d.made_at, d.live_by, d.live_at, d.created_at, d.updated_at`

const adDraftFrom = `FROM ad_drafts d JOIN ad_accounts a ON a.id = d.ad_account_id
	LEFT JOIN ad_entities c ON c.id = d.campaign_entity_id LEFT JOIN ad_entities g ON g.id = d.ad_group_entity_id`

func scanAdDraft(row pgx.Row) (adDraftOut, error) {
	var v adDraftOut
	var content, made []byte
	err := row.Scan(&v.ID, &v.BrandID, &v.AccountID, &v.AccountName, &v.Connector, &v.Currency, &v.Kind, &v.CampaignID, &v.CampaignName, &v.AdGroupID, &v.AdGroupName,
		&content, &v.Reason, &v.Status, &made, &v.Error, &v.CreatedBy, &v.ActionID, &v.ApprovedBy, &v.ApprovedAt, &v.MadeAt, &v.LiveBy, &v.LiveAt, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return v, err
	}
	_ = json.Unmarshal(content, &v.Content)
	_ = json.Unmarshal(made, &v.Made)
	v.Content.Kind = v.Kind
	return v, nil
}

var adDraftStatuses = map[string]bool{"draft": true, "creating": true, "paused": true, "going_live": true, "live": true, "failed": true, "dismissed": true}

func (s *Server) listAdDrafts(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	args := []any{a.WorkspaceID, a.Brand}
	where := `d.workspace_id = $1 AND d.brand_id = $2`
	for _, f := range []struct{ param, col, what string }{{"account_id", "a.id", "an ad account's id"}, {"campaign_id", "c.id", "a campaign's id"}} {
		if v := q.Get(f.param); v != "" {
			if !isUUID(v) {
				writeError(w, r, http.StatusBadRequest, "invalid_request", f.param+" is "+f.what)
				return
			}
			args = append(args, v)
			where += ` AND ` + f.col + ` = $` + strconv.Itoa(len(args))
		}
	}
	if v := q.Get("status"); v != "" {
		if !adDraftStatuses[v] {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is draft, creating, paused, going_live, live, failed or dismissed")
			return
		}
		args = append(args, v)
		where += ` AND d.status = $` + strconv.Itoa(len(args))
	}
	limit, ok := pageLimit(w, r, 100, 500)
	if !ok {
		return
	}
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `SELECT `+adDraftCols+` `+adDraftFrom+` WHERE `+where+`
		ORDER BY CASE d.status WHEN 'draft' THEN 0 WHEN 'paused' THEN 1 WHEN 'creating' THEN 1 WHEN 'going_live' THEN 1 WHEN 'failed' THEN 2 WHEN 'live' THEN 3 ELSE 4 END,
		d.updated_at DESC, d.id LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adDraftOut, error) { return scanAdDraft(row) })
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []adDraftOut{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// adDraftIn is a draft as written: where it goes (Outflare's ids) and
// what to make.
type adDraftIn struct {
	connectors.AdDraft
	AccountID  string `json:"account_id"`
	CampaignID string `json:"campaign_id"`
	AdGroupID  string `json:"ad_group_id"`
	Reason     string `json:"reason"`
}

// adDraftPlace is where a draft goes, as read from the mirror.
type adDraftPlace struct {
	accountID, connector, integrationID string
	campaign, group                     *string
	campaignExt, groupExt               string
}

// adCreatorFor opens an account's connection as an AdCreator, answering
// the error itself when it can't make drafts of kind.
func (s *Server) adCreatorFor(w http.ResponseWriter, r *http.Request, ws, integrationID, kind string) (connectors.AdCreator, bool) {
	o, err := s.integrations.Open(r.Context(), s.pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		writeError(w, r, http.StatusConflict, "not_active", "the ad platform is no longer connected")
		return nil, false
	}
	if err != nil {
		s.connectorFailed(w, r, err)
		return nil, false
	}
	cr, ok := o.Connector.(connectors.AdCreator)
	if !ok {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", o.Connector.Definition().Name+" ads can't be made here yet")
		return nil, false
	}
	if !slices.Contains(cr.DraftKinds(), kind) {
		writeError(w, r, http.StatusUnprocessableEntity, "connector_invalid", "a new "+strings.ReplaceAll(kind, "_", " ")+" can't be made on "+o.Connector.Definition().Name+" here")
		return nil, false
	}
	return cr, true
}

// adDraftPlaceFor finds where a draft goes: a new campaign goes in an
// account, a new ad group in a campaign, a new ad in an ad group. Each
// must be the route's brand's, or shared by every brand.
func (s *Server) adDraftPlaceFor(w http.ResponseWriter, r *http.Request, a *access, in adDraftIn) (adDraftPlace, bool) {
	var p adDraftPlace
	var brand *string
	var status string
	var err error
	switch in.Kind {
	case connectors.DraftCampaign:
		if !isUUID(in.AccountID) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a new campaign names its ad account (account_id)")
			return p, false
		}
		err = s.pool.QueryRow(r.Context(), `SELECT a.id, a.connector, a.integration_id, a.brand_id, a.status FROM ad_accounts a WHERE a.workspace_id = $1 AND a.id = $2`,
			a.WorkspaceID, in.AccountID).Scan(&p.accountID, &p.connector, &p.integrationID, &brand, &status)
	case connectors.DraftAdGroup:
		if !isUUID(in.CampaignID) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a new ad group names the campaign it goes in (campaign_id)")
			return p, false
		}
		p.campaign = &in.CampaignID
		err = s.pool.QueryRow(r.Context(), `SELECT a.id, a.connector, a.integration_id, COALESCE(e.brand_id, a.brand_id), a.status, e.external_id
			FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND e.id = $2 AND e.level = 'campaign' AND e.status <> 'removed'`,
			a.WorkspaceID, in.CampaignID).Scan(&p.accountID, &p.connector, &p.integrationID, &brand, &status, &p.campaignExt)
	case connectors.DraftAd:
		if !isUUID(in.AdGroupID) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "a new ad names the ad group (ad set on Meta) it goes in (ad_group_id)")
			return p, false
		}
		var campaign string
		err = s.pool.QueryRow(r.Context(), `SELECT a.id, a.connector, a.integration_id, COALESCE(c.brand_id, a.brand_id), a.status, c.id, c.external_id, g.external_id
			FROM ad_entities g JOIN ad_entities c ON c.id = g.parent_id AND c.level = 'campaign' JOIN ad_accounts a ON a.id = g.ad_account_id
			WHERE g.workspace_id = $1 AND g.id = $2 AND g.level = 'ad_group' AND g.status <> 'removed'`,
			a.WorkspaceID, in.AdGroupID).Scan(&p.accountID, &p.connector, &p.integrationID, &brand, &status, &campaign, &p.campaignExt, &p.groupExt)
		p.campaign, p.group = &campaign, &in.AdGroupID
		if err == nil && in.CampaignID != "" && in.CampaignID != campaign {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "the ad group is not in that campaign")
			return p, false
		}
	default:
		writeError(w, r, http.StatusBadRequest, "invalid_request", "kind is ad, ad_group or campaign")
		return p, false
	}
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && brand != nil && *brand != a.Brand) {
		writeError(w, r, http.StatusNotFound, "not_found", map[string]string{connectors.DraftCampaign: "ad account", connectors.DraftAdGroup: "campaign",
			connectors.DraftAd: "ad group"}[in.Kind]+" not found")
		return p, false
	}
	if err != nil {
		s.internal(w, r, err)
		return p, false
	}
	if status != "active" {
		writeError(w, r, http.StatusConflict, "not_active", "the sign-in no longer sees this ad account")
		return p, false
	}
	return p, true
}

// checkAdDraft checks what a draft makes against the platform's rules,
// with the platform's ids filled in as the job will.
func checkAdDraft(w http.ResponseWriter, r *http.Request, cr connectors.AdCreator, d connectors.AdDraft, p adDraftPlace) bool {
	d.CampaignID, d.AdGroupID = p.campaignExt, p.groupExt
	if err := cr.CheckDraft(d); err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", strings.TrimPrefix(err.Error(), "invalid: "))
		return false
	}
	return true
}

func (s *Server) createAdDraft(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var in adDraftIn
	if !decode(w, r, &in) {
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if len([]rune(in.Reason)) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is at most 2,000 characters")
		return
	}
	p, ok := s.adDraftPlaceFor(w, r, a, in)
	if !ok {
		return
	}
	cr, ok := s.adCreatorFor(w, r, a.WorkspaceID, p.integrationID, in.Kind)
	if !ok || !checkAdDraft(w, r, cr, in.AdDraft, p) {
		return
	}
	content, _ := json.Marshal(in.AdDraft)
	var actor, action *string
	if a.ActorID != "" {
		actor = &a.ActorID
	}
	if id := actionctx.ID(r.Context()); id != "" {
		action = &id
	}
	var out adDraftOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(r.Context(), `INSERT INTO ad_drafts (workspace_id, brand_id, ad_account_id, campaign_entity_id, ad_group_entity_id, kind, content, reason, created_by, action_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) RETURNING id`,
			a.WorkspaceID, a.Brand, p.accountID, p.campaign, p.group, in.Kind, content, in.Reason, actor, action).Scan(&id); err != nil {
			return err
		}
		if err := s.recordAdDraft(r.Context(), tx, a, "ad_draft.written", id, p.connector, in.Kind, nil); err != nil {
			return err
		}
		var err error
		out, err = scanAdDraft(tx.QueryRow(r.Context(), `SELECT `+adDraftCols+` `+adDraftFrom+` WHERE d.id = $1`, id))
		return err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) recordAdDraft(ctx context.Context, tx pgx.Tx, a *access, action, id, connector, kind string, extra map[string]any) error {
	data := map[string]any{"draft_id": id, "brand_id": a.Brand, "connector": connector, "kind": kind}
	for k, v := range extra {
		data[k] = v
	}
	return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, action, "ad_draft", id, data, map[string]any{"connector": connector, "kind": kind, "brand_id": a.Brand})
}

// loadAdDraft reads one of the route's brand's drafts, answering 404
// itself.
func (s *Server) loadAdDraft(w http.ResponseWriter, r *http.Request, a *access) (adDraftOut, bool) {
	id := r.PathValue("id")
	if !isUUID(id) {
		writeError(w, r, http.StatusNotFound, "not_found", "draft not found")
		return adDraftOut{}, false
	}
	v, err := scanAdDraft(s.pool.QueryRow(r.Context(), `SELECT `+adDraftCols+` `+adDraftFrom+` WHERE d.workspace_id = $1 AND d.brand_id = $2 AND d.id = $3`,
		a.WorkspaceID, a.Brand, id))
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusNotFound, "not_found", "draft not found")
		return v, false
	}
	if err != nil {
		s.internal(w, r, err)
		return v, false
	}
	return v, true
}

func (s *Server) getAdDraft(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.loadAdDraft(w, r, accessFrom(r.Context())); ok {
		writeJSON(w, http.StatusOK, v)
	}
}

// updateAdDraft rewrites what a draft makes, before it is approved: the
// whole of it, as for a new draft. Where it goes stays.
func (s *Server) updateAdDraft(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	var in adDraftIn
	if !decode(w, r, &in) {
		return
	}
	v, ok := s.loadAdDraft(w, r, a)
	if !ok {
		return
	}
	if v.Status != "draft" {
		writeError(w, r, http.StatusConflict, "not_active", "this draft is "+v.Status+"; only a draft that waits for a person can be changed")
		return
	}
	in.Kind, in.AccountID, in.Reason = v.Kind, v.AccountID, strings.TrimSpace(in.Reason)
	if v.CampaignID != nil {
		in.CampaignID = *v.CampaignID
	}
	if v.AdGroupID != nil {
		in.AdGroupID = *v.AdGroupID
	}
	if len([]rune(in.Reason)) > 2000 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "reason is at most 2,000 characters")
		return
	}
	p, ok := s.adDraftPlaceFor(w, r, a, in)
	if !ok {
		return
	}
	cr, ok := s.adCreatorFor(w, r, a.WorkspaceID, p.integrationID, in.Kind)
	if !ok || !checkAdDraft(w, r, cr, in.AdDraft, p) {
		return
	}
	in.AdDraft.Kind = v.Kind
	content, _ := json.Marshal(in.AdDraft)
	var out adDraftOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `UPDATE ad_drafts SET content = $2, reason = CASE WHEN $3 = '' THEN reason ELSE $3 END, updated_at = now()
			WHERE id = $1 AND status = 'draft'`, v.ID, content, in.Reason)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this draft was decided a moment ago")
		}
		if err := s.recordAdDraft(r.Context(), tx, a, "ad_draft.edited", v.ID, v.Connector, v.Kind, nil); err != nil {
			return err
		}
		out, err = scanAdDraft(tx.QueryRow(r.Context(), `SELECT `+adDraftCols+` `+adDraftFrom+` WHERE d.id = $1`, v.ID))
		return err
	})
	s.answerAdDraft(w, r, http.StatusOK, out, err)
}

func (s *Server) answerAdDraft(w http.ResponseWriter, r *http.Request, status int, out adDraftOut, err error) {
	var conflict errAdSuggestionTaken
	if errors.As(err, &conflict) {
		writeError(w, r, http.StatusConflict, "not_active", string(conflict))
		return
	}
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, out)
}

// moveAdDraft moves a draft from one status to another with its audit
// event, and queues a job step when one is named.
func (s *Server) moveAdDraft(w http.ResponseWriter, r *http.Request, a *access, v adDraftOut, from, to, action, step, set string) {
	var out adDraftOut
	err := pgx.BeginFunc(r.Context(), s.pool, func(tx pgx.Tx) error {
		args := []any{v.ID, from, to}
		if set != "" {
			// set names the person in $4.
			args = append(args, a.ActorID)
		}
		tag, err := tx.Exec(r.Context(), `UPDATE ad_drafts SET status = $3, error = '', `+set+` updated_at = now() WHERE id = $1 AND status = $2`, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errAdSuggestionTaken("this draft changed a moment ago")
		}
		if step != "" {
			if err := adsync.EnqueueDraft(r.Context(), tx, a.WorkspaceID, v.ID, step); err != nil {
				return err
			}
		}
		if err := s.recordAdDraft(r.Context(), tx, a, action, v.ID, v.Connector, v.Kind, nil); err != nil {
			return err
		}
		out, err = scanAdDraft(tx.QueryRow(r.Context(), `SELECT `+adDraftCols+` `+adDraftFrom+` WHERE d.id = $1`, v.ID))
		return err
	})
	status := http.StatusOK
	if step != "" {
		status = http.StatusAccepted
	}
	s.answerAdDraft(w, r, status, out, err)
}

// personOnly refuses a call that isn't a person's.
func personOnly(w http.ResponseWriter, r *http.Request, a *access, what string) bool {
	if a.ActorType != "human" {
		writeError(w, r, http.StatusForbidden, "forbidden", "only a person "+what+", in Outflare under Paid ads, Suggestions; agents write drafts")
		return false
	}
	return true
}

func (s *Server) approveAdDraft(w http.ResponseWriter, r *http.Request) {
	if !s.needIntegrations(w, r) {
		return
	}
	a := accessFrom(r.Context())
	if !personOnly(w, r, a, "approves a new ad") {
		return
	}
	v, ok := s.loadAdDraft(w, r, a)
	if !ok {
		return
	}
	if v.Status != "draft" && v.Status != "failed" {
		writeError(w, r, http.StatusConflict, "not_active", "this draft is "+v.Status+", not waiting for a person")
		return
	}
	in := adDraftIn{AdDraft: v.Content, AccountID: v.AccountID}
	in.Kind = v.Kind
	if v.CampaignID != nil {
		in.CampaignID = *v.CampaignID
	}
	if v.AdGroupID != nil {
		in.AdGroupID = *v.AdGroupID
	}
	p, ok := s.adDraftPlaceFor(w, r, a, in)
	if !ok {
		return
	}
	cr, ok := s.adCreatorFor(w, r, a.WorkspaceID, p.integrationID, v.Kind)
	if !ok || !checkAdDraft(w, r, cr, v.Content, p) {
		return
	}
	s.moveAdDraft(w, r, a, v, v.Status, "creating", "ad_draft.approved", adsync.DraftCreate, "approved_by = $4, approved_at = now(),")
}

func (s *Server) goLiveAdDraft(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	if !personOnly(w, r, a, "turns on a new ad") {
		return
	}
	v, ok := s.loadAdDraft(w, r, a)
	if !ok {
		return
	}
	if v.Status != "paused" {
		writeError(w, r, http.StatusConflict, "not_active", "this draft is "+v.Status+"; only ads made paused can be turned on")
		return
	}
	s.moveAdDraft(w, r, a, v, "paused", "going_live", "ad_draft.launched", adsync.DraftGoLive, "live_by = $4,")
}

func (s *Server) dismissAdDraft(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	v, ok := s.loadAdDraft(w, r, a)
	if !ok {
		return
	}
	if v.Status != "draft" && v.Status != "failed" {
		writeError(w, r, http.StatusConflict, "not_active", "this draft is "+v.Status+"; once made, pause or remove it on the platform")
		return
	}
	s.moveAdDraft(w, r, a, v, v.Status, "dismissed", "ad_draft.dismissed", "", "")
}
