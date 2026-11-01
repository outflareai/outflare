package api

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// Ad groups (Meta's ad sets) and ads, below the campaigns, from what the
// ad sync stored for the platforms that read them (Meta and Google). Their
// numbers are already in their campaign's.

type adItemOut struct {
	ID                string            `json:"id"`
	Level             string            `json:"level"`
	ParentID          *string           `json:"parent_id"`
	ParentName        string            `json:"parent_name"`
	CampaignID        *string           `json:"campaign_id"`
	CampaignName      string            `json:"campaign_name"`
	AdAccountID       string            `json:"ad_account_id"`
	Connector         string            `json:"connector"`
	AccountName       string            `json:"account_name"`
	ExternalID        string            `json:"external_id"`
	Name              string            `json:"name"`
	Status            string            `json:"status"`
	ProviderStatus    string            `json:"provider_status"`
	DailyBudgetMicros *int64            `json:"daily_budget_micros"`
	Details           map[string]string `json:"details"`
	BrandID           *string           `json:"brand_id"`
	Currency          string            `json:"currency"`
	Totals            adTotals          `json:"totals"`
}

func (s *Server) listAdItems(w http.ResponseWriter, r *http.Request) {
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	level := q.Get("level")
	if level == "" {
		level = connectors.AdLevelAd
	}
	if level != connectors.AdLevelGroup && level != connectors.AdLevelAd {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "level is ad_group or ad")
		return
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 1000")
			return
		}
		limit = n
	}
	f.args = append(f.args, level)
	where := f.where + ` AND e.level = $` + strconv.Itoa(len(f.args))
	// p is the parent, g the grandparent: an ad's campaign.
	for _, k := range []string{"campaign_id", "parent_id"} {
		id := q.Get(k)
		if id == "" {
			continue
		}
		if !isUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", k+" is an id from GET .../ads/campaigns or .../ads/items")
			return
		}
		f.args = append(f.args, id)
		n := strconv.Itoa(len(f.args))
		if k == "parent_id" {
			where += ` AND e.parent_id = $` + n
		} else {
			where += ` AND (CASE WHEN e.level = 'ad' THEN p.parent_id ELSE e.parent_id END) = $` + n
		}
	}
	if st := q.Get("status"); st != "" {
		if !slices.Contains([]string{connectors.AdActive, connectors.AdPaused, connectors.AdRemoved, connectors.AdOther}, st) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "status is active, paused, removed or other")
			return
		}
		f.args = append(f.args, st)
		where += ` AND e.status = $` + strconv.Itoa(len(f.args))
	}
	f.args = append(f.args, limit)
	rows, err := s.pool.Query(r.Context(), `
		SELECT e.id, e.level, e.parent_id, COALESCE(p.name, ''),
			CASE WHEN e.level = 'ad' THEN g.id ELSE p.id END, COALESCE(CASE WHEN e.level = 'ad' THEN g.name ELSE p.name END, ''),
			a.id, a.connector, a.name, e.external_id, e.name, e.status, e.provider_status, e.daily_budget_micros, e.details,
			COALESCE(e.brand_id, a.brand_id), a.currency, `+adSums+`
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
		LEFT JOIN ad_entities p ON p.id = e.parent_id
		LEFT JOIN ad_entities g ON g.id = p.parent_id
		LEFT JOIN ad_metrics_daily m ON m.ad_entity_id = e.id AND m.day BETWEEN $2 AND $3
		WHERE e.workspace_id = $1`+where+`
		GROUP BY e.id, a.id, p.id, g.id
		HAVING sum(m.spend_micros) > 0 OR sum(m.impressions) > 0 OR e.status = 'active'
		ORDER BY COALESCE(sum(m.spend_micros), 0) DESC, COALESCE(sum(m.impressions), 0) DESC, lower(e.name), e.id
		LIMIT $`+strconv.Itoa(len(f.args)), f.args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adItemOut{}
	for rows.Next() {
		var x adItemOut
		var details []byte
		t := &x.Totals
		if err := rows.Scan(&x.ID, &x.Level, &x.ParentID, &x.ParentName, &x.CampaignID, &x.CampaignName, &x.AdAccountID, &x.Connector, &x.AccountName,
			&x.ExternalID, &x.Name, &x.Status, &x.ProviderStatus, &x.DailyBudgetMicros, &details, &x.BrandID, &x.Currency,
			&t.SpendMicros, &t.Impressions, &t.Clicks, &t.Conversions, &t.ConversionValueMicros); err != nil {
			s.internal(w, r, err)
			return
		}
		x.Details = map[string]string{}
		_ = json.Unmarshal(details, &x.Details)
		t.derive()
		out = append(out, x)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}
