package api

import (
	"net/http"
	"strconv"
)

// More of what the platforms report, from what the ad sync stored:
// conversions by the platform's own action, and Google's search keywords
// and the searches they matched. Reads never call the platforms.

type adConversionActionOut struct {
	Action      string  `json:"action"`
	Connector   string  `json:"connector"`
	Currency    string  `json:"currency"`
	Conversions float64 `json:"conversions"`
	ValueMicros *int64  `json:"value_micros"`
}

// listAdConversionActions sums conversions by action over the range: of
// campaigns, which hold all of an account's, or of the one campaign, ad
// group or ad entity_id names.
func (s *Server) listAdConversionActions(w http.ResponseWriter, r *http.Request) {
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	where := f.where + ` AND e.level = 'campaign'`
	if id := r.URL.Query().Get("entity_id"); id != "" {
		if !isUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "entity_id is a campaign's, ad group's or ad's id, from GET .../ads/campaigns or .../ads/items")
			return
		}
		f.args = append(f.args, id)
		where = f.where + ` AND e.id = $` + strconv.Itoa(len(f.args))
	}
	rows, err := s.pool.Query(r.Context(), `
		SELECT x.action, a.connector, a.currency, sum(x.conversions)::float8, sum(x.value_micros)::bigint
		FROM ad_conversion_actions_daily x JOIN ad_entities e ON e.id = x.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE x.workspace_id = $1 AND x.day BETWEEN $2 AND $3`+where+`
		GROUP BY 1, 2, 3
		ORDER BY 4 DESC, 1, 2, 3
		LIMIT 500`, f.args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adConversionActionOut{}
	for rows.Next() {
		var x adConversionActionOut
		if err := rows.Scan(&x.Action, &x.Connector, &x.Currency, &x.Conversions, &x.ValueMicros); err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, x)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}

// searchSums are adSums for keyword and search term rows, which have no
// delivery numbers beside clicks.
const searchSums = `COALESCE(sum(m.spend_micros), 0)::bigint, COALESCE(sum(m.impressions), 0)::bigint, COALESCE(sum(m.clicks), 0)::bigint,
	sum(m.conversions)::float8, sum(m.conversion_value_micros)::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint`

// searchFilter adds campaign_id and ad_group_id to f's filters, on g (the
// ad group) and its parent, and reads limit.
func (s *Server) searchFilter(w http.ResponseWriter, r *http.Request, f *adFilter) (limit int, ok bool) {
	q := r.URL.Query()
	for _, k := range []struct{ param, col string }{{"campaign_id", "g.parent_id"}, {"ad_group_id", "g.id"}} {
		id := q.Get(k.param)
		if id == "" {
			continue
		}
		if !isUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", k.param+" is an id from GET .../ads/campaigns or .../ads/items")
			return 0, false
		}
		f.args = append(f.args, id)
		f.where += ` AND ` + k.col + ` = $` + strconv.Itoa(len(f.args))
	}
	limit = 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "limit is 1 to 1000")
			return 0, false
		}
		limit = n
	}
	return limit, true
}

type adKeywordOut struct {
	ID           string   `json:"id"`
	ExternalID   string   `json:"external_id"`
	Text         string   `json:"text"`
	MatchType    string   `json:"match_type"`
	Status       string   `json:"status"`
	AdGroupID    string   `json:"ad_group_id"`
	AdGroupName  string   `json:"ad_group_name"`
	CampaignID   *string  `json:"campaign_id"`
	CampaignName string   `json:"campaign_name"`
	Currency     string   `json:"currency"`
	Totals       adTotals `json:"totals"`
}

func (s *Server) listAdKeywords(w http.ResponseWriter, r *http.Request) {
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	limit, ok := s.searchFilter(w, r, &f)
	if !ok {
		return
	}
	f.args = append(f.args, limit)
	// e is the campaign, as adQuery's brand filter expects.
	rows, err := s.pool.Query(r.Context(), `
		SELECT k.id, k.external_id, k.text, k.match_type, k.status, g.id, g.name, e.id, COALESCE(e.name, ''), a.currency, `+searchSums+`
		FROM ad_keywords k JOIN ad_entities g ON g.id = k.ad_group_id LEFT JOIN ad_entities e ON e.id = g.parent_id
		JOIN ad_accounts a ON a.id = k.ad_account_id
		JOIN ad_keyword_metrics_daily m ON m.keyword_id = k.id AND m.day BETWEEN $2 AND $3
		WHERE k.workspace_id = $1`+f.where+`
		GROUP BY k.id, g.id, e.id, a.id
		ORDER BY sum(m.spend_micros) DESC, sum(m.impressions) DESC, k.text, k.id
		LIMIT $`+strconv.Itoa(len(f.args)), f.args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adKeywordOut{}
	for rows.Next() {
		var x adKeywordOut
		t := &x.Totals
		if err := rows.Scan(append([]any{&x.ID, &x.ExternalID, &x.Text, &x.MatchType, &x.Status, &x.AdGroupID, &x.AdGroupName, &x.CampaignID, &x.CampaignName, &x.Currency},
			t.dest()...)...); err != nil {
			s.internal(w, r, err)
			return
		}
		t.derive()
		out = append(out, x)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}

type adSearchTermOut struct {
	Term             string   `json:"term"`
	KeywordText      string   `json:"keyword_text"`
	KeywordMatchType string   `json:"keyword_match_type"`
	AdGroupID        string   `json:"ad_group_id"`
	AdGroupName      string   `json:"ad_group_name"`
	CampaignID       *string  `json:"campaign_id"`
	CampaignName     string   `json:"campaign_name"`
	Currency         string   `json:"currency"`
	Totals           adTotals `json:"totals"`
}

func (s *Server) listAdSearchTerms(w http.ResponseWriter, r *http.Request) {
	f, ok := s.adQuery(w, r)
	if !ok {
		return
	}
	limit, ok := s.searchFilter(w, r, &f)
	if !ok {
		return
	}
	f.args = append(f.args, limit)
	rows, err := s.pool.Query(r.Context(), `
		SELECT m.term, m.keyword_text, m.keyword_match_type, g.id, g.name, e.id, COALESCE(e.name, ''), a.currency, `+searchSums+`
		FROM ad_search_terms_daily m JOIN ad_entities g ON g.id = m.ad_group_id LEFT JOIN ad_entities e ON e.id = g.parent_id
		JOIN ad_accounts a ON a.id = m.ad_account_id
		WHERE m.workspace_id = $1 AND m.day BETWEEN $2 AND $3`+f.where+`
		GROUP BY m.term, m.keyword_text, m.keyword_match_type, g.id, e.id, a.id
		ORDER BY sum(m.spend_micros) DESC, sum(m.impressions) DESC, m.term
		LIMIT $`+strconv.Itoa(len(f.args)), f.args...)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	defer rows.Close()
	out := []adSearchTermOut{}
	for rows.Next() {
		var x adSearchTermOut
		t := &x.Totals
		if err := rows.Scan(append([]any{&x.Term, &x.KeywordText, &x.KeywordMatchType, &x.AdGroupID, &x.AdGroupName, &x.CampaignID, &x.CampaignName, &x.Currency},
			t.dest()...)...); err != nil {
			s.internal(w, r, err)
			return
		}
		t.derive()
		out = append(out, x)
	}
	if rows.Err() != nil {
		s.internal(w, r, rows.Err())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}
