package api

import (
	"context"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/paidads"
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
	rows.Close()
	// In the brand's currency, an action on a connector is one row.
	report, err := s.adReport(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	merged, at := []adConversionActionOut{}, map[string]int{}
	for _, x := range out {
		if k, ok := report.rates.Rate(x.Currency, report.to); ok && report.to != "" && x.Currency != report.to {
			if x.ValueMicros != nil {
				x.ValueMicros = ptr(int64(math.Round(float64(*x.ValueMicros) * k)))
			}
			x.Currency = report.to
		}
		key := x.Action + "\x00" + x.Connector + "\x00" + x.Currency
		if i, ok := at[key]; ok {
			merged[i].Conversions += x.Conversions
			if x.ValueMicros != nil {
				if merged[i].ValueMicros == nil {
					merged[i].ValueMicros = x.ValueMicros
				} else {
					merged[i].ValueMicros = ptr(*merged[i].ValueMicros + *x.ValueMicros)
				}
			}
			continue
		}
		at[key] = len(merged)
		merged = append(merged, x)
	}
	out = merged
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}

// searchSums are adSums for keyword and search term rows, which have no
// delivery numbers beside clicks.
const searchSums = `COALESCE(sum(m.spend_micros), 0)::bigint, COALESCE(sum(m.impressions), 0)::bigint, COALESCE(sum(m.clicks), 0)::bigint,
	sum(m.conversions)::float8, sum(m.conversion_value_micros)::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint,
	NULL::float8, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint, NULL::bigint`

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
	// Currency is the ad account's; ReportCurrency is what totals are in
	// (the brand's currency for one brand), and ExchangeRate how many of
	// it one unit of Currency is.
	ReportCurrency string  `json:"report_currency"`
	ExchangeRate   float64 `json:"exchange_rate"`
	// What Google estimates the keyword must bid to show on the first
	// page, at the top of the page and in the first position, in micros,
	// and its quality score (1 to 10), as of the last sync; null where
	// Google said nothing.
	FirstPageCPCMicros     *int64 `json:"first_page_cpc_micros"`
	TopOfPageCPCMicros     *int64 `json:"top_of_page_cpc_micros"`
	FirstPositionCPCMicros *int64 `json:"first_position_cpc_micros"`
	QualityScore           *int   `json:"quality_score"`
	// Outflare's own count of who the keyword's clicks brought (the paid
	// ads ledger): people who signed up in the range, how many of those
	// have paid since, people whose first payment fell in the range, and
	// what each of those cost. PaidShare is SignupsPaid over Signups, null
	// without a sign-up.
	Signups               int      `json:"signups"`
	SignupsPaid           int      `json:"signups_paid"`
	NewPayers             int      `json:"new_payers"`
	PaidShare             *float64 `json:"paid_share"`
	CostPerNewPayerMicros *int64   `json:"cost_per_new_payer_micros"`
}

// adKeywordCount is the ledger's count for one keyword of an ad group.
type adKeywordCount struct{ signups, paid, payers int }

// adKeywordCounts reads, for the ad groups given, who each keyword's
// clicks brought over the range (days from and to in loc): the ledger
// keeps the keyword of the click behind every acquisition. The key is the
// ad group, the keyword in lower case and its match type; the match type
// is empty when the click wasn't one Google listed (the keyword came from
// the landing page's tags), and such a count belongs to the keyword
// whatever its match type.
func (s *Server) adKeywordCounts(ctx context.Context, ws string, groups []string, from, to string, loc *time.Location) (map[string]adKeywordCount, error) {
	out := map[string]adKeywordCount{}
	if len(groups) == 0 {
		return out, nil
	}
	f, _ := time.Parse(time.DateOnly, from)
	t, _ := time.Parse(time.DateOnly, to)
	rows, err := s.pool.Query(ctx, `
		SELECT q.ad_group_entity_id::text, lower(q.keyword), COALESCE(c.match_type, ''),
			count(*) FILTER (WHERE q.signed_up_at >= $3 AND q.signed_up_at < $4)::int,
			count(*) FILTER (WHERE q.signed_up_at >= $3 AND q.signed_up_at < $4 AND q.first_paid_at IS NOT NULL)::int,
			count(*) FILTER (WHERE q.first_paid_at >= $3 AND q.first_paid_at < $4)::int
		FROM ad_acquisitions q LEFT JOIN ad_clicks c ON c.ad_account_id = q.ad_account_id AND c.click_id = q.click_id AND q.click_id <> ''
		WHERE q.workspace_id = $1 AND q.ad_group_entity_id = ANY($2::uuid[]) AND q.keyword <> ''
		  AND ((q.signed_up_at >= $3 AND q.signed_up_at < $4) OR (q.first_paid_at >= $3 AND q.first_paid_at < $4))
		GROUP BY 1, 2, 3`, ws, groups, paidads.DayStart(f, loc), paidads.DayStart(t.AddDate(0, 0, 1), loc))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var group, text, match string
		var n adKeywordCount
		if err := rows.Scan(&group, &text, &match, &n.signups, &n.paid, &n.payers); err != nil {
			return nil, err
		}
		out[group+" "+match+" "+text] = n
	}
	return out, rows.Err()
}

// adKeywordCountOf adds up a keyword's counts: those of its match type
// and, with untyped, those without one.
func adKeywordCountOf(counts map[string]adKeywordCount, group, text, match string, untyped bool) adKeywordCount {
	text = strings.ToLower(text)
	var n adKeywordCount
	if untyped {
		n = counts[group+"  "+text]
	}
	if match != "" {
		m := counts[group+" "+match+" "+text]
		n.signups, n.paid, n.payers = n.signups+m.signups, n.paid+m.paid, n.payers+m.payers
	}
	return n
}

func paidShare(signups, paid int) *float64 {
	if signups == 0 {
		return nil
	}
	v := float64(paid) / float64(signups)
	return &v
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
		SELECT k.id, k.external_id, k.text, k.match_type, k.status, g.id, g.name, e.id, COALESCE(e.name, ''), a.currency,
			k.first_page_cpc_micros, k.top_of_page_cpc_micros, k.first_position_cpc_micros, k.quality_score::int, `+searchSums+`
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
		if err := rows.Scan(append([]any{&x.ID, &x.ExternalID, &x.Text, &x.MatchType, &x.Status, &x.AdGroupID, &x.AdGroupName, &x.CampaignID, &x.CampaignName, &x.Currency,
			&x.FirstPageCPCMicros, &x.TopOfPageCPCMicros, &x.FirstPositionCPCMicros, &x.QualityScore},
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
	rows.Close()
	report, err := s.adReport(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	groups := []string{}
	for _, x := range out {
		groups = append(groups, x.AdGroupID)
	}
	counts, err := s.adKeywordCounts(r.Context(), accessFrom(r.Context()).WorkspaceID, groups, f.from, f.to, loc)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	// A count without a match type goes to one keyword only: of an ad
	// group's keywords with the same words, the one that spent most (the
	// rows are in that order).
	given := map[string]bool{}
	for i := range out {
		x := &out[i]
		x.ReportCurrency, x.ExchangeRate = report.apply(x.Currency, &x.Totals)
		words := x.AdGroupID + " " + strings.ToLower(x.Text)
		n := adKeywordCountOf(counts, x.AdGroupID, x.Text, x.MatchType, !given[words])
		given[words] = true
		x.Signups, x.SignupsPaid, x.NewPayers, x.PaidShare = n.signups, n.paid, n.payers, paidShare(n.signups, n.paid)
		if n.payers > 0 {
			v := x.Totals.SpendMicros / int64(n.payers)
			x.CostPerNewPayerMicros = &v
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}

type adSearchTermOut struct {
	Term             string `json:"term"`
	KeywordText      string `json:"keyword_text"`
	KeywordMatchType string `json:"keyword_match_type"`
	// MatchType is how the search matched the keyword (Google: exact,
	// near_exact, phrase, near_phrase, broad), the way with the most
	// impressions; empty when the platform didn't say.
	MatchType    string   `json:"match_type"`
	AdGroupID    string   `json:"ad_group_id"`
	AdGroupName  string   `json:"ad_group_name"`
	CampaignID   *string  `json:"campaign_id"`
	CampaignName string   `json:"campaign_name"`
	Currency     string   `json:"currency"`
	Totals       adTotals `json:"totals"`
	// Currency is the ad account's; ReportCurrency is what totals are in
	// (the brand's currency for one brand), and ExchangeRate how many of
	// it one unit of Currency is.
	ReportCurrency string  `json:"report_currency"`
	ExchangeRate   float64 `json:"exchange_rate"`
	// Who the keyword the search matched brought, on Outflare's own count
	// (a network doesn't say which search a click came from, so these are
	// the keyword's, not the search's own): sign-ups in the range, how
	// many of those have paid since, and that share.
	KeywordSignups     int      `json:"keyword_signups"`
	KeywordSignupsPaid int      `json:"keyword_signups_paid"`
	KeywordPaidShare   *float64 `json:"keyword_paid_share"`
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
		SELECT m.term, m.keyword_text, m.keyword_match_type,
			COALESCE((array_agg(m.search_term_match_type ORDER BY m.impressions DESC) FILTER (WHERE m.search_term_match_type <> ''))[1], ''),
			g.id, g.name, e.id, COALESCE(e.name, ''), a.currency, `+searchSums+`
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
		if err := rows.Scan(append([]any{&x.Term, &x.KeywordText, &x.KeywordMatchType, &x.MatchType, &x.AdGroupID, &x.AdGroupName, &x.CampaignID, &x.CampaignName, &x.Currency},
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
	rows.Close()
	report, err := s.adReport(r)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	loc, ok := s.reportZone(w, r)
	if !ok {
		return
	}
	groups := []string{}
	for _, x := range out {
		groups = append(groups, x.AdGroupID)
	}
	counts, err := s.adKeywordCounts(r.Context(), accessFrom(r.Context()).WorkspaceID, groups, f.from, f.to, loc)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for i := range out {
		x := &out[i]
		x.ReportCurrency, x.ExchangeRate = report.apply(x.Currency, &x.Totals)
		if x.KeywordText != "" {
			n := adKeywordCountOf(counts, x.AdGroupID, x.KeywordText, x.KeywordMatchType, true)
			x.KeywordSignups, x.KeywordSignupsPaid, x.KeywordPaidShare = n.signups, n.paid, paidShare(n.signups, n.paid)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": f.from, "to": f.to, "data": out})
}
