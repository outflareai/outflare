package ads

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// The findings levers on Google: each mutation takes the shape the API
// reference gives, the reads parse the report rows, and an exclusion is
// idempotent.
func TestGoogleFindingsLevers(t *testing.T) {
	ctx := context.Background()
	acct := c.AdAccount{ID: "123", Currency: "USD"}
	creds := c.Creds{"access_token": "t"}
	// The campaign's criteria: a device opt-out already on mobile, a
	// negative keyword, one ad schedule window, a negative place.
	criteria := []any{
		map[string]any{"campaignCriterion": map[string]any{"resourceName": "customers/123/campaignCriteria/7~30001", "type": "DEVICE", "bidModifier": 0.0, "device": map[string]any{"type": "MOBILE"}}},
		map[string]any{"campaignCriterion": map[string]any{"resourceName": "customers/123/campaignCriteria/7~999", "type": "KEYWORD", "negative": true, "keyword": map[string]any{"text": "free crm", "matchType": "EXACT"}}},
		map[string]any{"campaignCriterion": map[string]any{"resourceName": "customers/123/campaignCriteria/7~2036", "type": "LOCATION", "negative": true, "location": map[string]any{"geoTargetConstant": "geoTargetConstants/2036"}}},
	}
	cl, reqs, _ := fake(t, map[string]any{
		"POST /v23/customers/123/googleAds:search": func(r *http.Request) any {
			q := readBody(r)
			switch {
			case strings.Contains(q, "FROM campaign_criterion"):
				// Only the types the query asks for, as Google answers.
				var rows []any
				for _, r := range criteria {
					if strings.Contains(q, "'"+r.(map[string]any)["campaignCriterion"].(map[string]any)["type"].(string)+"'") {
						rows = append(rows, r)
					}
				}
				return map[string]any{"results": rows}
			case strings.Contains(q, "FROM ad_group_criterion"):
				return map[string]any{"results": []any{map[string]any{"adGroupCriterion": map[string]any{"resourceName": "customers/123/adGroupCriteria/71~555", "negative": true,
					"keyword": map[string]any{"text": "free crm", "matchType": "PHRASE"}}}}}
			case strings.Contains(q, "FROM campaign WHERE campaign.id = 7") && strings.Contains(q, "bidding_strategy_type"):
				return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "7", "biddingStrategyType": "MAXIMIZE_CONVERSIONS"}}}}
			case strings.Contains(q, "FROM campaign WHERE campaign.id = 8") && strings.Contains(q, "bidding_strategy_type"):
				return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "8", "biddingStrategyType": "TARGET_ROAS", "biddingStrategy": "customers/123/biddingStrategies/5"}}}}
			case strings.Contains(q, "FROM shared_set"):
				return map[string]any{"results": []any{}}
			case strings.Contains(q, "segments.device"):
				return map[string]any{"results": []any{
					map[string]any{"segments": map[string]any{"device": "MOBILE"}, "metrics": map[string]any{"impressions": "1000", "clicks": "320", "costMicros": "90000000", "conversions": 0.0}},
					map[string]any{"segments": map[string]any{"device": "DESKTOP"}, "metrics": map[string]any{"impressions": "900", "clicks": "200", "costMicros": "80000000", "conversions": 12.0}},
					map[string]any{"segments": map[string]any{"device": "MOBILE"}, "metrics": map[string]any{"impressions": "10", "clicks": "5", "costMicros": "1000000", "conversions": 0.0}}}}
			case strings.Contains(q, "segments.ad_network_type"):
				return map[string]any{"results": []any{map[string]any{"segments": map[string]any{"adNetworkType": "SEARCH_PARTNERS"}, "metrics": map[string]any{"clicks": "40", "costMicros": "20000000", "conversions": 0.0}}}}
			case strings.Contains(q, "segments.hour"):
				return map[string]any{"results": []any{map[string]any{"segments": map[string]any{"hour": 3}, "metrics": map[string]any{"clicks": "12", "costMicros": "3000000"}}}}
			case strings.Contains(q, "FROM geographic_view"):
				return map[string]any{"results": []any{map[string]any{"geographicView": map[string]any{"countryCriterionId": "2036"}, "metrics": map[string]any{"clicks": "100", "costMicros": "5000000"}}}}
			case strings.Contains(q, "FROM geo_target_constant"):
				return map[string]any{"results": []any{map[string]any{"geoTargetConstant": map[string]any{"resourceName": "geoTargetConstants/2036", "countryCode": "AU", "targetType": "Country"}}}}
			case strings.Contains(q, "FROM group_placement_view"):
				return map[string]any{"results": []any{map[string]any{"groupPlacementView": map[string]any{"placement": "mobileapp::2-com.example.game", "placementType": "MOBILE_APPLICATION", "displayName": "A game"},
					"metrics": map[string]any{"clicks": "400", "costMicros": "30000000"}}}}
			case strings.Contains(q, "search_budget_lost_impression_share"):
				return map[string]any{"results": []any{
					map[string]any{"campaign": map[string]any{"networkSettings": map[string]any{"targetSearchNetwork": true, "targetContentNetwork": false}}, "metrics": map[string]any{"searchBudgetLostImpressionShare": 0.2, "searchRankLostImpressionShare": 0.1}},
					map[string]any{"campaign": map[string]any{"networkSettings": map[string]any{"targetSearchNetwork": true}}, "metrics": map[string]any{"searchBudgetLostImpressionShare": 0.1, "searchRankLostImpressionShare": 0.3}}}}
			}
			return map[string]any{"results": []any{}}
		},
		"POST /v23/customers/123/adGroupCriteria:mutate":  map[string]any{"results": []any{map[string]any{"resourceName": "customers/123/adGroupCriteria/71~91"}}},
		"POST /v23/customers/123/adGroupAds:mutate":       map[string]any{"results": []any{}},
		"POST /v23/customers/123/campaigns:mutate":        map[string]any{"results": []any{}},
		"POST /v23/customers/123/campaignCriteria:mutate": map[string]any{"results": []any{map[string]any{"resourceName": "customers/123/campaignCriteria/7~1"}}},
	})
	g := GoogleAds{}
	calls := func(suffix string) []string {
		var out []string
		for _, r := range *reqs {
			if strings.HasSuffix(r.path, suffix) {
				out = append(out, r.body)
			}
		}
		return out
	}

	if err := g.SetKeywordStatus(ctx, cl, creds, acct, "71", "91", false); err != nil {
		t.Fatal(err)
	}
	if got := calls("adGroupCriteria:mutate"); len(got) != 1 || !strings.Contains(got[0], `"resourceName":"customers/123/adGroupCriteria/71~91","status":"PAUSED"`) || !strings.Contains(got[0], `"updateMask":"status"`) {
		t.Fatalf("keyword status: %v", got)
	}
	if err := g.SetAdStatus(ctx, cl, creds, acct, "71", "81", true); err != nil {
		t.Fatal(err)
	}
	if got := calls("adGroupAds:mutate"); len(got) != 1 || !strings.Contains(got[0], `"resourceName":"customers/123/adGroupAds/71~81","status":"ENABLED"`) {
		t.Fatalf("ad status: %v", got)
	}
	if err := g.SetKeywordStatus(ctx, cl, creds, acct, "x", "91", false); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad id: %v", err)
	}

	// A target on a Maximise conversions campaign goes on the strategy's
	// field; a portfolio strategy is refused; a return on spend on a cost
	// per conversion strategy is refused.
	cpa := int64(12_000_000)
	if err := g.SetCampaignTarget(ctx, cl, creds, acct, "7", c.AdTarget{TargetCPAMicros: &cpa}); err != nil {
		t.Fatal(err)
	}
	if got := calls("campaigns:mutate"); len(got) != 1 || !strings.Contains(got[0], `"maximizeConversions":{"targetCpaMicros":"12000000"}`) || !strings.Contains(got[0], `"updateMask":"maximize_conversions.target_cpa_micros"`) {
		t.Fatalf("target: %v", got)
	}
	roas := 3.5
	if err := g.SetCampaignTarget(ctx, cl, creds, acct, "7", c.AdTarget{TargetROAS: &roas}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("roas on a cpa strategy: %v", err)
	}
	if err := g.SetCampaignTarget(ctx, cl, creds, acct, "8", c.AdTarget{TargetROAS: &roas}); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "portfolio") {
		t.Fatalf("portfolio: %v", err)
	}
	if err := g.SetCampaignTarget(ctx, cl, creds, acct, "7", c.AdTarget{}); err != nil {
		t.Fatal(err)
	}
	if got := calls("campaigns:mutate"); !strings.Contains(got[len(got)-1], `"targetCpaMicros":"0"`) {
		t.Fatalf("dropping the target: %v", got)
	}

	// Exclusions. The mobile opt-out is already there: nothing is sent.
	// Tablet is new: a device criterion with bid modifier 0. A place is a
	// negative location; the one there already is left; removing it
	// removes that criterion. Search partners go through network
	// settings. A schedule window adds the rest of the day on every day.
	n := len(calls("campaignCriteria:mutate"))
	if err := g.AddExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeDevice, Value: "mobile"}); err != nil {
		t.Fatal(err)
	}
	if len(calls("campaignCriteria:mutate")) != n {
		t.Fatal("an exclusion already there was sent again")
	}
	if err := g.AddExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeDevice, Value: "tablet"}); err != nil {
		t.Fatal(err)
	}
	got := calls("campaignCriteria:mutate")
	if len(got) != n+1 || !strings.Contains(got[n], `"bidModifier":0,"campaign":"customers/123/campaigns/7","device":{"type":"TABLET"}`) {
		t.Fatalf("device: %v", got)
	}
	if err := g.AddExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeLocation, Value: "geoTargetConstants/2554"}); err != nil {
		t.Fatal(err)
	}
	got = calls("campaignCriteria:mutate")
	if !strings.Contains(got[len(got)-1], `"location":{"geoTargetConstant":"geoTargetConstants/2554"},"negative":true`) {
		t.Fatalf("location: %v", got)
	}
	if err := g.RemoveExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeLocation, Value: "geoTargetConstants/2036"}); err != nil {
		t.Fatal(err)
	}
	got = calls("campaignCriteria:mutate")
	if !strings.Contains(got[len(got)-1], `"remove":"customers/123/campaignCriteria/7~2036"`) {
		t.Fatalf("remove location: %v", got)
	}
	if err := g.AddExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludePlacement, Value: "2-com.example.game", Detail: "mobile_application"}); err != nil {
		t.Fatal(err)
	}
	got = calls("campaignCriteria:mutate")
	if !strings.Contains(got[len(got)-1], `"mobileApplication":{"appId":"2-com.example.game"},"negative":true`) {
		t.Fatalf("placement: %v", got)
	}
	if err := g.AddExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeNetwork, Value: "search_partners"}); err != nil {
		t.Fatal(err)
	}
	got = calls("campaigns:mutate")
	if !strings.Contains(got[len(got)-1], `"networkSettings":{"targetSearchNetwork":false}`) || !strings.Contains(got[len(got)-1], `"updateMask":"network_settings.target_search_network"`) {
		t.Fatalf("network: %v", got)
	}
	if err := g.RemoveExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeNetwork, Value: "display"}); err != nil {
		t.Fatal(err)
	}
	got = calls("campaigns:mutate")
	if !strings.Contains(got[len(got)-1], `"networkSettings":{"targetContentNetwork":true}`) {
		t.Fatalf("network back on: %v", got)
	}
	n = len(calls("campaignCriteria:mutate"))
	if err := g.AddExclusion(ctx, cl, creds, acct, "7", c.AdExclusion{Kind: c.ExcludeSchedule, Value: "00-06"}); err != nil {
		t.Fatal(err)
	}
	got = calls("campaignCriteria:mutate")
	if len(got) != n+7 || !strings.Contains(got[n], `"adSchedule":{"dayOfWeek":"MONDAY","endHour":24,"endMinute":"ZERO","startHour":6,"startMinute":"ZERO"}`) {
		t.Fatalf("schedule: %d new, %v", len(got)-n, got[n:])
	}
	if _, _, err := scheduleWindow("25-3"); err == nil {
		t.Fatal("a bad window was taken")
	}
	if w := servingWindows(22, 6); len(w) != 1 || w[0] != [2]int{6, 22} {
		t.Fatalf("overnight: %v", w)
	}

	// Negatives: the campaign's exact negative is found and removed; the
	// ad group's phrase one too; one that isn't there is nothing to do.
	if err := g.RemoveNegativeKeyword(ctx, cl, creds, acct, "7", "", "Free CRM", "exact"); err != nil {
		t.Fatal(err)
	}
	got = calls("campaignCriteria:mutate")
	if !strings.Contains(got[len(got)-1], `"remove":"customers/123/campaignCriteria/7~999"`) {
		t.Fatalf("remove negative: %v", got)
	}
	if err := g.RemoveNegativeKeyword(ctx, cl, creds, acct, "7", "71", "free crm", "phrase"); err != nil {
		t.Fatal(err)
	}
	got = calls("adGroupCriteria:mutate")
	if !strings.Contains(got[len(got)-1], `"remove":"customers/123/adGroupCriteria/71~555"`) {
		t.Fatalf("remove ad group negative: %v", got)
	}
	n = len(*reqs)
	if err := g.RemoveNegativeKeyword(ctx, cl, creds, acct, "7", "", "nothing here", "exact"); err != nil || len(*reqs) != n+1 {
		t.Fatalf("a negative that isn't there: %v, %d calls", err, len(*reqs)-n)
	}

	// Signals: impression share averaged, networks, segments summed by
	// value with the country code looked up and the app id trimmed.
	s, err := g.AdCampaignSignals(ctx, cl, creds, acct, "7", "2026-09-01", "2026-10-30")
	if err != nil {
		t.Fatal(err)
	}
	if s.BudgetLostImpressionShare < 0.149 || s.BudgetLostImpressionShare > 0.151 || s.RankLostImpressionShare < 0.199 || !s.SearchPartners || s.DisplayExpansion {
		t.Fatalf("signals: %+v", s)
	}
	by := map[string]c.AdSegment{}
	for _, x := range s.Segments {
		by[x.Kind+" "+x.Value] = x
	}
	if m := by["device mobile"]; m.Clicks != 325 || m.SpendMicros != 91_000_000 || m.Impressions != 1010 {
		t.Fatalf("mobile: %+v", m)
	}
	if d := by["device desktop"]; d.Conversions != 12 {
		t.Fatalf("desktop: %+v", d)
	}
	if p := by["network search_partners"]; p.Clicks != 40 {
		t.Fatalf("partners: %+v", p)
	}
	if h := by["hour 03"]; h.Clicks != 12 || h.Label != "03:00" {
		t.Fatalf("hour: %+v", h)
	}
	if l := by["location geoTargetConstants/2036"]; l.Clicks != 100 || l.Label != "AU" {
		t.Fatalf("place: %+v", l)
	}
	if a := by["placement 2-com.example.game"]; a.Clicks != 400 || a.Detail != "mobile_application" || a.Label != "A game" {
		t.Fatalf("placement: %+v", a)
	}
}

func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
