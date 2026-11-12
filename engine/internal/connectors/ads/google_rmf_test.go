package ads

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// googleFake serves GAQL by what the query reads and records every
// mutate's body by its path.
func googleFake(t *testing.T, search func(q string) any, mutates map[string]any) (*c.Client, *[]seen) {
	t.Helper()
	routes := map[string]any{
		"POST /v23/customers/111/googleAds:search": func(r *http.Request) any {
			b, _ := io.ReadAll(r.Body)
			var body struct {
				Query string `json:"query"`
			}
			_ = json.Unmarshal(b, &body)
			return search(body.Query)
		},
	}
	for k, v := range mutates {
		routes["POST /v23/customers/111/"+k] = v
	}
	cl, reqs, _ := fake(t, routes)
	return cl, reqs
}

func bodyOf(t *testing.T, reqs []seen, path string) map[string]any {
	t.Helper()
	for i := len(reqs) - 1; i >= 0; i-- {
		if reqs[i].path == path {
			var m map[string]any
			if err := json.Unmarshal([]byte(reqs[i].body), &m); err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
	t.Fatalf("no request to %s", path)
	return nil
}

func firstOp(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	ops, _ := body["operations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("operations: %v", body)
	}
	return ops[0].(map[string]any)
}

// C.65: a website conversion action is created with Google's category and
// counting, and its tag snippets are read back with the list.
func TestGoogleWebConversion(t *testing.T) {
	var queries []string
	cl, reqs := googleFake(t, func(q string) any {
		queries = append(queries, q)
		return map[string]any{"results": []any{
			map[string]any{"conversionAction": map[string]any{"id": "77", "name": "Purchase", "type": "WEBPAGE", "category": "PURCHASE", "status": "ENABLED",
				"primaryForGoal": true, "countingType": "MANY_PER_CLICK", "clickThroughLookbackWindowDays": "30",
				"valueSettings": map[string]any{"defaultValue": 25.5, "defaultCurrencyCode": "USD", "alwaysUseDefaultValue": false},
				"tagSnippets":   []any{map[string]any{"type": "WEBPAGE", "pageFormat": "HTML", "globalSiteTag": "<script>gtag</script>", "eventSnippet": "<script>event</script>"}}}},
			map[string]any{"conversionAction": map[string]any{"id": "78", "name": "Offline", "type": "UPLOAD_CLICKS", "category": "SIGNUP", "status": "ENABLED", "countingType": "ONE_PER_CLICK"}},
		}}
	}, map[string]any{"conversionActions:mutate": map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/conversionActions/77"}}}})
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111", Currency: "USD"}
	value := int64(25_500_000)
	id, err := g.CreateWebConversion(ctx, cl, creds, acct, c.AdWebConversion{Name: " Purchase ", Category: c.ConvCategoryPurchase, Counting: "many", ClickWindowDays: 30,
		DefaultValueMicros: &value, Currency: "usd"})
	if err != nil || id != "77" {
		t.Fatalf("create: %q %v", id, err)
	}
	op := firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/conversionActions:mutate"))["create"].(map[string]any)
	if op["name"] != "Purchase" || op["type"] != "WEBPAGE" || op["category"] != "PURCHASE" || op["countingType"] != "MANY_PER_CLICK" || op["status"] != "ENABLED" ||
		op["clickThroughLookbackWindowDays"] != "30" {
		t.Fatalf("create op %v", op)
	}
	if vs := op["valueSettings"].(map[string]any); vs["defaultValue"] != 25.5 || vs["defaultCurrencyCode"] != "USD" {
		t.Fatalf("value settings %v", vs)
	}
	list, err := g.AdConversionActions(ctx, cl, creds, acct)
	if err != nil || len(list) != 2 {
		t.Fatalf("list %+v %v", list, err)
	}
	w := list[0]
	if w.ID != "77" || w.Type != "webpage" || w.Category != "purchase" || !w.Primary || w.Counting != "many" || w.ClickWindowDays != 30 ||
		w.DefaultValueMicros == nil || *w.DefaultValueMicros != 25_500_000 || len(w.Snippets) != 1 || w.Snippets[0].GlobalSiteTag != "<script>gtag</script>" ||
		w.Snippets[0].EventSnippet != "<script>event</script>" || w.Snippets[0].PageFormat != "html" {
		t.Fatalf("webpage action %+v", w)
	}
	if list[1].Counting != "one" || len(list[1].Snippets) != 0 || list[1].Snippets == nil {
		t.Fatalf("upload action %+v", list[1])
	}
	if !strings.Contains(queries[len(queries)-1], "conversion_action.tag_snippets") {
		t.Fatalf("query %s", queries[len(queries)-1])
	}
	for _, bad := range []c.AdWebConversion{
		{Name: "", Category: c.ConvCategoryLead, Counting: "one", ClickWindowDays: 30},
		{Name: "x", Category: "refund", Counting: "one", ClickWindowDays: 30},
		{Name: "x", Category: c.ConvCategoryLead, Counting: "twice", ClickWindowDays: 30},
		{Name: "x", Category: c.ConvCategoryLead, Counting: "one", ClickWindowDays: 91},
		{Name: "x", Category: c.ConvCategoryLead, Counting: "one", ClickWindowDays: 30, AlwaysUseDefault: true},
	} {
		if _, err := g.CreateWebConversion(ctx, cl, creds, acct, bad); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
}

// C.75: a callout is an asset and its account link in one batch, listed
// from customer_asset, and removed by unlinking.
func TestGoogleCallouts(t *testing.T) {
	cl, reqs := googleFake(t, func(q string) any {
		return map[string]any{"results": []any{map[string]any{"customerAsset": map[string]any{"resourceName": "customers/111/customerAssets/501~CALLOUT",
			"asset": "customers/111/assets/501", "status": "ENABLED"}, "asset": map[string]any{"id": "501", "calloutAsset": map[string]any{"calloutText": "Free shipping"}}}}}
	}, map[string]any{
		"googleAds:mutate": map[string]any{"mutateOperationResponses": []any{
			map[string]any{"assetResult": map[string]any{"resourceName": "customers/111/assets/502"}},
			map[string]any{"customerAssetResult": map[string]any{"resourceName": "customers/111/customerAssets/502~CALLOUT"}}}},
		"customerAssets:mutate": map[string]any{},
	})
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}
	list, err := g.AdCallouts(ctx, cl, creds, acct)
	if err != nil || len(list) != 1 || list[0].ID != "501" || list[0].Text != "Free shipping" || list[0].Status != c.AdActive {
		t.Fatalf("list %+v %v", list, err)
	}
	id, err := g.AddCallout(ctx, cl, creds, acct, "  24/7   support ")
	if err != nil || id != "502" {
		t.Fatalf("add %q %v", id, err)
	}
	ops := bodyOf(t, *reqs, "/v23/customers/111/googleAds:mutate")["mutateOperations"].([]any)
	asset := ops[0].(map[string]any)["assetOperation"].(map[string]any)["create"].(map[string]any)
	link := ops[1].(map[string]any)["customerAssetOperation"].(map[string]any)["create"].(map[string]any)
	if asset["calloutAsset"].(map[string]any)["calloutText"] != "24/7 support" || link["fieldType"] != "CALLOUT" || link["asset"] != asset["resourceName"] {
		t.Fatalf("batch %v", ops)
	}
	if _, err := g.AddCallout(ctx, cl, creds, acct, strings.Repeat("x", 26)); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("too long: %v", err)
	}
	if err := g.RemoveCallout(ctx, cl, creds, acct, "501"); err != nil {
		t.Fatal(err)
	}
	if op := firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/customerAssets:mutate")); op["remove"] != "customers/111/customerAssets/501~CALLOUT" {
		t.Fatalf("remove %v", op)
	}
	// One that isn't linked is already gone.
	n := len(*reqs)
	if err := g.RemoveCallout(ctx, cl, creds, acct, "999"); err != nil {
		t.Fatal(err)
	}
	for _, r := range (*reqs)[n:] {
		if strings.HasSuffix(r.path, "customerAssets:mutate") {
			t.Fatal("removed a callout that wasn't there")
		}
	}
}

// C.96, C.97, M.96, M.97, R.130: portfolio strategies are created with
// their target, their target moves where their type keeps it, a campaign
// joins one, and the report reads Google's numbers.
func TestGooglePortfolios(t *testing.T) {
	var queries []string
	strategy := map[string]any{"id": "900", "name": "Leads tCPA", "type": "TARGET_CPA", "status": "ENABLED", "campaignCount": "3", "effectiveCurrencyCode": "USD",
		"targetCpa": map[string]any{"targetCpaMicros": "20000000"}}
	cl, reqs := googleFake(t, func(q string) any {
		queries = append(queries, q)
		switch {
		case strings.Contains(q, "metrics.clicks"):
			return map[string]any{"results": []any{map[string]any{"biddingStrategy": strategy,
				"metrics": map[string]any{"clicks": "40", "impressions": "900", "costMicros": "80000000", "averageCpc": 2000000, "conversions": 4, "costPerConversion": 20000000, "allConversions": 6}}}}
		case strings.Contains(q, "FROM bidding_strategy"):
			return map[string]any{"results": []any{map[string]any{"biddingStrategy": strategy},
				map[string]any{"biddingStrategy": map[string]any{"id": "901", "name": "Value", "type": "TARGET_ROAS", "status": "ENABLED", "targetRoas": map[string]any{"targetRoas": 3.5}}}}}
		}
		return map[string]any{"results": []any{}}
	}, map[string]any{
		"biddingStrategies:mutate": map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/biddingStrategies/902"}}},
		"campaigns:mutate":         map[string]any{},
	})
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111", Currency: "USD"}
	list, err := g.AdPortfolios(ctx, cl, creds, acct)
	if err != nil || len(list) != 2 || list[0].Type != "target_cpa" || list[0].TargetCPAMicros == nil || *list[0].TargetCPAMicros != 20_000_000 || list[0].CampaignCount != 3 ||
		list[1].TargetROAS == nil || *list[1].TargetROAS != 3.5 {
		t.Fatalf("list %+v %v", list, err)
	}
	cpa := int64(25_000_000)
	id, err := g.CreatePortfolio(ctx, cl, creds, acct, c.AdPortfolioSpec{Name: "New tCPA", Type: c.PortfolioTargetCPA, TargetCPAMicros: &cpa})
	if err != nil || id != "902" {
		t.Fatalf("create %q %v", id, err)
	}
	op := firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/biddingStrategies:mutate"))["create"].(map[string]any)
	if op["name"] != "New tCPA" || op["targetCpa"].(map[string]any)["targetCpaMicros"] != "25000000" {
		t.Fatalf("create op %v", op)
	}
	roas := 4.0
	if _, err := g.CreatePortfolio(ctx, cl, creds, acct, c.AdPortfolioSpec{Name: "R", Type: c.PortfolioTargetROAS, TargetROAS: &roas}); err != nil {
		t.Fatal(err)
	}
	if op := firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/biddingStrategies:mutate"))["create"].(map[string]any); op["targetRoas"].(map[string]any)["targetRoas"] != 4.0 {
		t.Fatalf("roas op %v", op)
	}
	if _, err := g.CreatePortfolio(ctx, cl, creds, acct, c.AdPortfolioSpec{Name: "x", Type: c.PortfolioTargetCPA}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no target: %v", err)
	}
	// Moving the target of the Target CPA strategy.
	cpa = 18_000_000
	if err := g.UpdatePortfolio(ctx, cl, creds, acct, "900", c.AdPortfolioSpec{TargetCPAMicros: &cpa}); err != nil {
		t.Fatal(err)
	}
	body := bodyOf(t, *reqs, "/v23/customers/111/biddingStrategies:mutate")
	upd := firstOp(t, body)
	if upd["updateMask"] != "target_cpa.target_cpa_micros" || upd["update"].(map[string]any)["targetCpa"].(map[string]any)["targetCpaMicros"] != "18000000" ||
		upd["update"].(map[string]any)["resourceName"] != "customers/111/biddingStrategies/900" {
		t.Fatalf("update %v", upd)
	}
	// A Target CPA strategy takes no return on spend.
	if err := g.UpdatePortfolio(ctx, cl, creds, acct, "900", c.AdPortfolioSpec{TargetROAS: &roas}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("wrong target: %v", err)
	}
	// A campaign joins the strategy.
	if err := g.SetBidding(ctx, cl, creds, acct, "7", c.AdBidding{Strategy: c.BidPortfolio, BiddingStrategyID: "900"}); err != nil {
		t.Fatal(err)
	}
	upd = firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/campaigns:mutate"))
	if upd["updateMask"] != "bidding_strategy" || upd["update"].(map[string]any)["biddingStrategy"] != "customers/111/biddingStrategies/900" {
		t.Fatalf("attach %v", upd)
	}
	if err := g.SetBidding(ctx, cl, creds, acct, "7", c.AdBidding{Strategy: c.BidPortfolio}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no strategy: %v", err)
	}
	rep, err := g.AdPortfolioReport(ctx, cl, creds, acct, "2026-10-01", "2026-10-28")
	if err != nil || len(rep) != 2 {
		t.Fatalf("report %+v %v", rep, err)
	}
	x := rep[0]
	if x.ID != "900" || x.Clicks != 40 || x.Impressions != 900 || x.SpendMicros != 80_000_000 || x.CPCMicros == nil || *x.CPCMicros != 2_000_000 || x.Conversions != 4 ||
		x.AllConversions != 6 || x.CostPerConvMicros == nil || *x.CostPerConvMicros != 20_000_000 || x.Type != "target_cpa" || x.Status != "enabled" {
		t.Fatalf("row %+v", x)
	}
	if rep[1].Clicks != 0 || rep[1].ID != "901" {
		t.Fatalf("a strategy without numbers still shows: %+v", rep[1])
	}
	var reportQuery string
	for _, q := range queries {
		if strings.Contains(q, "metrics.cost_per_conversion") {
			reportQuery = q
		}
	}
	for _, f := range []string{"bidding_strategy.type", "bidding_strategy.status", "metrics.average_cpc", "metrics.all_conversions", "segments.date BETWEEN '2026-10-01' AND '2026-10-28'"} {
		if !strings.Contains(reportQuery, f) {
			t.Fatalf("report query lacks %s: %s", f, reportQuery)
		}
	}
}

// M.110, M.130, M.140: a campaign, an ad and a keyword are removed with
// the remove operation of their service, and read back.
func TestGoogleRemove(t *testing.T) {
	removed := map[string]bool{}
	cl, reqs := googleFake(t, func(q string) any {
		st := "ENABLED"
		switch {
		case strings.Contains(q, "FROM campaign "):
			if removed["campaigns"] {
				st = "REMOVED"
			}
			return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "7", "status": st}}}}
		case strings.Contains(q, "FROM ad_group_ad "):
			if removed["adGroupAds"] {
				st = "REMOVED"
			}
			return map[string]any{"results": []any{map[string]any{"adGroupAd": map[string]any{"status": st}}}}
		case strings.Contains(q, "FROM ad_group_criterion "):
			return map[string]any{"results": []any{}}
		}
		return map[string]any{"results": []any{}}
	}, map[string]any{
		"campaigns:mutate":       func(r *http.Request) any { removed["campaigns"] = true; return map[string]any{} },
		"adGroupAds:mutate":      func(r *http.Request) any { removed["adGroupAds"] = true; return map[string]any{} },
		"adGroupCriteria:mutate": func(r *http.Request) any { removed["adGroupCriteria"] = true; return map[string]any{} },
	})
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}
	if err := g.Remove(ctx, cl, creds, acct, c.RemoveCampaign, "", "7"); err != nil {
		t.Fatal(err)
	}
	if op := firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/campaigns:mutate")); op["remove"] != "customers/111/campaigns/7" {
		t.Fatalf("campaign %v", op)
	}
	if st, err := g.RemovedStatus(ctx, cl, creds, acct, c.RemoveCampaign, "", "7"); err != nil || st != c.AdRemoved {
		t.Fatalf("read back %q %v", st, err)
	}
	// Removing it again changes nothing.
	n := len(*reqs)
	if err := g.Remove(ctx, cl, creds, acct, c.RemoveCampaign, "", "7"); err != nil || len(*reqs) != n+1 {
		t.Fatalf("again: %v, %d calls", err, len(*reqs)-n)
	}
	if err := g.Remove(ctx, cl, creds, acct, c.RemoveAd, "71", "81"); err != nil {
		t.Fatal(err)
	}
	if op := firstOp(t, bodyOf(t, *reqs, "/v23/customers/111/adGroupAds:mutate")); op["remove"] != "customers/111/adGroupAds/71~81" {
		t.Fatalf("ad %v", op)
	}
	if st, _ := g.RemovedStatus(ctx, cl, creds, acct, c.RemoveAd, "71", "81"); st != c.AdRemoved {
		t.Fatalf("ad read back %q", st)
	}
	// A keyword Google no longer lists counts as removed, so nothing is sent.
	if err := g.Remove(ctx, cl, creds, acct, c.RemoveKeyword, "71", "91"); err != nil || removed["adGroupCriteria"] {
		t.Fatalf("keyword: %v %v", err, removed)
	}
	if err := g.Remove(ctx, cl, creds, acct, c.RemoveAd, "", "81"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no ad group: %v", err)
	}
}

// R.10, R.20, R.50, R.70: all conversions on campaigns and ad groups,
// keyword bid estimates and quality score, and a search term's match type.
func TestGoogleRMFReportFields(t *testing.T) {
	var queries []string
	cl, _ := googleFake(t, func(q string) any {
		queries = append(queries, q)
		switch {
		case strings.Contains(q, "segments.conversion_action_name"):
			return map[string]any{"results": []any{}}
		case strings.Contains(q, "FROM campaign "):
			return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "7", "name": "Spring"}, "segments": map[string]any{"date": "2026-10-01"},
				"metrics": map[string]any{"costMicros": "1000000", "impressions": "10", "clicks": "2", "conversions": 1, "conversionsValue": 10, "allConversions": 3.5, "allConversionsValue": 42}}}}
		case strings.Contains(q, "FROM ad_group "):
			return map[string]any{"results": []any{map[string]any{"adGroup": map[string]any{"id": "71"}, "campaign": map[string]any{"id": "7"}, "segments": map[string]any{"date": "2026-10-01"},
				"metrics": map[string]any{"costMicros": "1000000", "allConversions": 2}}}}
		case strings.Contains(q, "FROM keyword_view"):
			return map[string]any{"results": []any{map[string]any{"adGroupCriterion": map[string]any{"criterionId": "91", "status": "ENABLED", "keyword": map[string]any{"text": "acme crm", "matchType": "PHRASE"},
				"positionEstimates": map[string]any{"firstPageCpcMicros": "1200000", "topOfPageCpcMicros": "2500000", "firstPositionCpcMicros": "4100000"}, "qualityInfo": map[string]any{"qualityScore": 7}},
				"adGroup": map[string]any{"id": "71"}, "segments": map[string]any{"date": "2026-10-01"}, "metrics": map[string]any{"clicks": "3"}}}}
		case strings.Contains(q, "FROM search_term_view"):
			return map[string]any{"results": []any{map[string]any{"searchTermView": map[string]any{"searchTerm": "acme crm price"}, "adGroup": map[string]any{"id": "71"},
				"segments": map[string]any{"date": "2026-10-01", "searchTermMatchType": "NEAR_PHRASE", "keyword": map[string]any{"info": map[string]any{"text": "acme crm", "matchType": "PHRASE"}}},
				"metrics":  map[string]any{"clicks": "1"}}}}
		}
		return map[string]any{"results": []any{}}
	}, nil)
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}
	days, err := g.AdDays(ctx, cl, creds, acct, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].AllConversions == nil || *days[0].AllConversions != 3.5 || days[0].AllConversionValueMicros == nil || *days[0].AllConversionValueMicros != 42_000_000 {
		t.Fatalf("days %+v %v", days, err)
	}
	items, err := g.AdItemDays(ctx, cl, creds, acct, c.AdLevelGroup, "2026-10-01", "2026-10-01")
	if err != nil || len(items) != 1 || items[0].AllConversions == nil || *items[0].AllConversions != 2 || items[0].AllConversionValueMicros != nil {
		t.Fatalf("items %+v %v", items, err)
	}
	kws, err := g.AdKeywordDays(ctx, cl, creds, acct, "2026-10-01", "2026-10-01")
	if err != nil || len(kws) != 1 {
		t.Fatalf("keywords %+v %v", kws, err)
	}
	k := kws[0]
	if k.FirstPageCPCMicros == nil || *k.FirstPageCPCMicros != 1_200_000 || *k.TopOfPageCPCMicros != 2_500_000 || *k.FirstPositionCPCMicros != 4_100_000 || k.QualityScore == nil || *k.QualityScore != 7 {
		t.Fatalf("estimates %+v", k.AdKeywordEstimates)
	}
	terms, err := g.AdSearchTermDays(ctx, cl, creds, acct, "2026-10-01", "2026-10-01")
	if err != nil || len(terms) != 1 || terms[0].MatchType != "near_phrase" || terms[0].KeywordMatchType != "phrase" {
		t.Fatalf("terms %+v %v", terms, err)
	}
	all := strings.Join(queries, "\n")
	for _, f := range []string{"metrics.all_conversions, metrics.all_conversions_value FROM campaign", "ad_group_criterion.position_estimates.first_page_cpc_micros",
		"ad_group_criterion.position_estimates.first_position_cpc_micros", "segments.search_term_match_type"} {
		if !strings.Contains(all, f) {
			t.Fatalf("no %s in the queries", f)
		}
	}
}
