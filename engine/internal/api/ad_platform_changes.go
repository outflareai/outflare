package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
)

// Changes made on the ad platforms themselves (plans/paid-ads/plan.md,
// sync and alerts): what adsync reads from Google's change history every
// 15 minutes into ad_change_events, shown on the Changes tab beside
// Outflare's own changes (listBrandAdChanges). Each says when, what, how, from
// where (web, api, automated_rule, recommendation_auto_apply, script) and
// who: outflare for Outflare's own, google for the platform's automation,
// else the SHA-256 of the user's email, never the address.

func (s *Server) routesAdPlatformChanges(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/ads/platform-changes", s.onBrand(auth.ScopeAnalyticsRead, s.listAdPlatformChanges))
}

type adPlatformChangeOut struct {
	ID          string         `json:"id"`
	AccountID   string         `json:"account_id"`
	AccountName string         `json:"account_name"`
	Connector   string         `json:"connector"`
	BrandID     *string        `json:"brand_id"`
	ExternalID  string         `json:"external_id"`
	ChangedAt   time.Time      `json:"changed_at"`
	Actor       string         `json:"actor"`
	Source      string         `json:"source"`
	Resource    string         `json:"resource_type"`
	Operation   string         `json:"operation"`
	CampaignID  *string        `json:"campaign_id"`
	Campaign    string         `json:"campaign_external_id"`
	Summary     string         `json:"summary"`
	Detail      map[string]any `json:"detail"`
}

var adPlatformChangeSources = map[string]bool{"web": true, "api": true, "automated_rule": true, "recommendation_auto_apply": true, "script": true, "editor": true, "bulk": true, "other": true}

func (s *Server) listAdPlatformChanges(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	brand, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	args := []any{a.WorkspaceID}
	where := `c.workspace_id = $1`
	if v := q.Get("connector"); v != "" {
		args = append(args, v)
		where += ` AND a.connector = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("account_id"); v != "" {
		if !isUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "account_id is an ad account's id, from GET .../ads/accounts")
			return
		}
		args = append(args, v)
		where += ` AND c.ad_account_id = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("campaign_id"); v != "" {
		if !isUUID(v) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "campaign_id is a campaign's id")
			return
		}
		args = append(args, v)
		where += ` AND e.id = $` + strconv.Itoa(len(args))
	}
	if v := q.Get("actor"); v != "" {
		switch v {
		case "outflare", "google":
			args = append(args, v)
			where += ` AND c.actor = $` + strconv.Itoa(len(args))
		case "people":
			where += ` AND c.actor NOT IN ('outflare', 'google', '')`
		default:
			writeError(w, r, http.StatusBadRequest, "invalid_request", "actor is outflare, google or people")
			return
		}
	}
	if v := q.Get("source"); v != "" {
		if !adPlatformChangeSources[v] {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "source is web, api, automated_rule, recommendation_auto_apply, script, editor, bulk or other")
			return
		}
		args = append(args, v)
		where += ` AND c.source = $` + strconv.Itoa(len(args))
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
	where += ` AND ($` + strconv.Itoa(len(args)) + `::timestamptz IS NULL OR c.changed_at < $` + strconv.Itoa(len(args)) + `)`
	args = append(args, limit)
	rows, err := s.pool.Query(r.Context(), `
		SELECT c.id, c.ad_account_id, a.name, a.connector, c.brand_id, c.external_id, c.changed_at, c.actor, c.source, c.resource_type, c.operation,
			e.id, c.campaign_external_id, c.summary, c.detail
		FROM ad_change_events c JOIN ad_accounts a ON a.id = c.ad_account_id
		LEFT JOIN ad_entities e ON e.ad_account_id = c.ad_account_id AND e.level = 'campaign' AND e.external_id = c.campaign_external_id AND c.campaign_external_id <> ''
		WHERE `+where+` ORDER BY c.changed_at DESC, c.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adPlatformChangeOut, error) {
		var v adPlatformChangeOut
		var detail []byte
		err := row.Scan(&v.ID, &v.AccountID, &v.AccountName, &v.Connector, &v.BrandID, &v.ExternalID, &v.ChangedAt, &v.Actor, &v.Source, &v.Resource, &v.Operation,
			&v.CampaignID, &v.Campaign, &v.Summary, &detail)
		v.Detail = map[string]any{}
		_ = json.Unmarshal(detail, &v.Detail)
		return v, err
	})
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if out == nil {
		out = []adPlatformChangeOut{}
	}
	var next *string
	if len(out) == limit {
		t := out[len(out)-1].ChangedAt.Format(time.RFC3339Nano)
		next = &t
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "next_cursor": nextCursor(pageBefore, next)})
}
