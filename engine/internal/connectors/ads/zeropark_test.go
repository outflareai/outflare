package ads

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Zeropark's Stats API, answered as its reference (/v3/api-docs) shows:
// the token in the api-token header, the account from
// /api/account/details, and campaigns with their stats from
// /api/stats/campaign/all, paged, for a named or a CUSTOM interval with
// dd/mm/yyyy days.
func TestZeroparkAds(t *testing.T) {
	campaign := func(id, name, state string, spent float64, redirects, conversions int, payout float64) map[string]any {
		return map[string]any{
			"details": map[string]any{"id": id, "name": name, "state": map[string]any{"state": state, "actions": []any{"PAUSE"}},
				"dailyBudget": map[string]any{"type": "LIMITED", "amount": 50.0}, "totalBudget": map[string]any{"type": "UNLIMITED"}},
			"stats": map[string]any{"spent": spent, "payout": payout, "redirects": redirects, "conversions": conversions, "profit": payout - spent},
		}
	}
	cl, reqs, _ := fake(t, map[string]any{
		"GET /api/account/details": map[string]any{"firstName": "Ana", "lastName": "Lima", "email": "ana@northpeak.example",
			"clientId": "zp-1001", "accountBalance": 412.5, "timezone": "Europe/Berlin"},
		"GET /api/stats/campaign/all": func(r *http.Request) any {
			q := r.URL.Query()
			if q.Get("interval") == "TODAY" {
				// Two pages of 1,000 and one more: the paging is followed.
				page, _ := strconv.Atoi(q.Get("page"))
				var el []any
				switch page {
				case 0:
					el = append(el, campaign("11111111-1111-4111-8111-111111111111", "Push US", "ACTIVE", 0, 0, 0, 0))
					for i := 1; i < 1000; i++ {
						el = append(el, campaign("f"+strconv.Itoa(i), "Filler", "PAUSED", 0, 0, 0, 0))
					}
				case 1:
					el = append(el, campaign("22222222-2222-4222-8222-222222222222", "Pop DE", "OUT_OF_DAILY_BUDGET", 0, 0, 0, 0),
						campaign("33333333-3333-4333-8333-333333333333", "Old", "DELETED", 0, 0, 0, 0))
				}
				return map[string]any{"page": page, "limit": 1000, "total": 1002, "elements": el}
			}
			if q.Get("interval") != "CUSTOM" || q.Get("startDate") != q.Get("endDate") {
				return map[string]any{"page": 0, "limit": 1000, "total": 0, "elements": []any{}}
			}
			switch q.Get("startDate") {
			case "01/11/2026":
				return map[string]any{"page": 0, "limit": 1000, "total": 2, "elements": []any{
					campaign("11111111-1111-4111-8111-111111111111", "Push US", "ACTIVE", 12.345678, 4115, 3, 24.6),
					campaign("22222222-2222-4222-8222-222222222222", "Pop DE", "ACTIVE", 0, 0, 0, 0)}}
			case "02/11/2026":
				return map[string]any{"page": 0, "limit": 1000, "total": 1, "elements": []any{
					campaign("22222222-2222-4222-8222-222222222222", "Pop DE", "ACTIVE", 3.5, 700, 0, 0)}}
			}
			return map[string]any{"page": 0, "limit": 1000, "total": 0, "elements": []any{}}
		},
	})
	z := ZeroparkAds{}
	creds := c.Creds{"api_token": "zp_test_not_a_real_token"}

	// The token goes in api-token; the owner's name and email aren't kept.
	acct, err := z.Test(ctx, cl, creds)
	if err != nil || acct.ID != "zp-1001" || acct.Details["balance_usd"] != "412.50" || acct.Details["time_zone"] != "Europe/Berlin" {
		t.Fatalf("test %+v %v", acct, err)
	}
	if h := (*reqs)[0].header; h.Get("api-token") != "zp_test_not_a_real_token" || h.Get("Authorization") != "" {
		t.Fatalf("headers %v", h)
	}
	for _, v := range acct.Details {
		if v == "ana@northpeak.example" || v == "Ana" {
			t.Fatalf("kept the owner's details: %v", acct)
		}
	}

	accts, err := z.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].Currency != "USD" || accts[0].TimeZone != "Europe/Berlin" || accts[0].ID != "zp-1001" {
		t.Fatalf("accounts %+v %v", accts, err)
	}

	camps, err := z.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 1002 {
		t.Fatalf("campaigns %d %v", len(camps), err)
	}
	byID := map[string]c.AdCampaign{}
	for _, cp := range camps {
		byID[cp.ID] = cp
	}
	if cp := byID["11111111-1111-4111-8111-111111111111"]; cp.Status != c.AdActive || cp.Name != "Push US" || cp.DailyBudgetMicros == nil || *cp.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("active campaign %+v", cp)
	}
	if cp := byID["22222222-2222-4222-8222-222222222222"]; cp.Status != c.AdActive || cp.ProviderStatus != "OUT_OF_DAILY_BUDGET" {
		t.Fatalf("held campaign %+v", cp)
	}
	if cp := byID["33333333-3333-4333-8333-333333333333"]; cp.Status != c.AdRemoved {
		t.Fatalf("deleted campaign %+v", cp)
	}
	if byID["f1"].Status != c.AdPaused {
		t.Fatalf("paused campaign %+v", byID["f1"])
	}

	// Each day is asked for on its own, in Zeropark's dd/mm/yyyy; empty
	// rows are left out; money is in micros of dollars.
	got, err := z.AdDays(ctx, cl, creds, accts[0], "2026-11-01", "2026-11-03")
	if err != nil || len(got) != 2 {
		t.Fatalf("days %+v %v", got, err)
	}
	d := got[0]
	if d.Date != "2026-11-01" || d.CampaignID != "11111111-1111-4111-8111-111111111111" || d.SpendMicros != 12_345_678 || d.Clicks != 4115 ||
		d.Impressions != 0 || d.Conversions == nil || *d.Conversions != 3 || d.ConversionValueMicros == nil || *d.ConversionValueMicros != 24_600_000 {
		t.Fatalf("first day %+v", d)
	}
	if got[1].Date != "2026-11-02" || got[1].SpendMicros != 3_500_000 || got[1].ConversionValueMicros != nil {
		t.Fatalf("second day %+v", got[1])
	}
	days := 0
	for _, r := range *reqs {
		if r.path == "/api/stats/campaign/all" && r.query != "" {
			if q, _ := url.ParseQuery(r.query); q.Get("interval") == "CUSTOM" {
				days++
			}
		}
	}
	if days != 3 {
		t.Fatalf("asked for %d days, want 3", days)
	}
}

func TestZeroparkAdsRefusals(t *testing.T) {
	status := 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"message":"Unauthorized"}`))
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{"zeropark_ads": srv.URL}}
	z := ZeroparkAds{}
	if _, err := z.Test(ctx, cl, c.Creds{"api_token": "zp_test_not_a_real_token"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a revoked token is an auth error: %v", err)
	}
	if _, err := z.Test(ctx, cl, c.Creds{}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no token: %v", err)
	}
	status = 429
	if _, err := z.AdAccounts(ctx, cl, c.Creds{"api_token": "zp_test_not_a_real_token"}); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("too many calls is a quota error: %v", err)
	}
}

// A campaign's targets for one day: every key a click can carry (target,
// address, id), the spend in micros of dollars, and only the targets that
// spent or sent a visit.
func TestZeroparkAdTargetDays(t *testing.T) {
	const cid = "11111111-1111-4111-8111-111111111111"
	cl, reqs, _ := fake(t, map[string]any{
		"GET /api/stats/campaign/" + cid + "/targets": func(r *http.Request) any {
			q := r.URL.Query()
			if q.Get("interval") != "CUSTOM" || q.Get("startDate") != "01/11/2026" || q.Get("endDate") != "01/11/2026" {
				return map[string]any{"page": 0, "limit": 1000, "total": 0, "elements": []any{}}
			}
			return map[string]any{"page": 0, "limit": 1000, "total": 3, "elements": []any{
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000001", "target": "pub-17", "address": "pub-17", "source": "feed-a",
					"stats": map[string]any{"spent": 7.25, "redirects": 2900}},
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000002", "target": "Example site", "address": "site.example", "source": "feed-b",
					"stats": map[string]any{"spent": 0.004, "redirects": 1}},
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000003", "target": "idle", "stats": map[string]any{"spent": 0, "redirects": 0}},
			}}
		},
	})
	got, err := ZeroparkAds{}.AdTargetDays(ctx, cl, c.Creds{"api_token": "zp_test_not_a_real_token"}, c.AdAccount{ID: "zp-1001"}, cid, "2026-11-01")
	if err != nil || len(got) != 2 {
		t.Fatalf("targets %+v %v", got, err)
	}
	if g := got[0]; g.SpendMicros != 7_250_000 || g.Clicks != 2900 || len(g.Keys) != 2 || g.Keys[0] != "pub-17" || g.Keys[1] != "aaaaaaaa-0000-4000-8000-000000000001" || g.Source != "feed-a" {
		t.Fatalf("first target %+v", g)
	}
	if g := got[1]; g.SpendMicros != 4000 || len(g.Keys) != 3 {
		t.Fatalf("second target %+v", g)
	}
	if h := (*reqs)[0].header; h.Get("api-token") != "zp_test_not_a_real_token" {
		t.Fatalf("headers %v", h)
	}
	if _, err := (ZeroparkAds{}).AdTargetDays(ctx, cl, c.Creds{"api_token": "zp_test_not_a_real_token"}, c.AdAccount{}, "../x", "2026-11-01"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a bad campaign id: %v", err)
	}
}

// A campaign's targets as Zeropark holds them: the state (a word or an
// object holding one), the bid and the smallest bid, for every target
// with traffic in the last 30 days.
func TestZeroparkAdTargetStates(t *testing.T) {
	const cid = "11111111-1111-4111-8111-111111111111"
	cl, _, _ := fake(t, map[string]any{
		"GET /api/stats/campaign/" + cid + "/targets": func(r *http.Request) any {
			if q := r.URL.Query(); q.Get("interval") != "LAST_30_DAYS" || q.Get("startDate") != "" {
				return map[string]any{"page": 0, "limit": 1000, "total": 0, "elements": []any{}}
			}
			return map[string]any{"page": 0, "limit": 1000, "total": 5, "elements": []any{
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000001", "target": "pub-17", "address": "pub-17", "source": "feed-a",
					"state": map[string]any{"state": "ACTIVE", "actions": []string{"PAUSE"}}, "bid": map[string]any{"value": 0.0125, "autoBid": false}, "minBid": 0.0005},
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000002", "target": "Example site", "address": "site.example",
					"state": "PAUSED", "bid": map[string]any{"value": 0.02, "autoBid": true}},
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000003", "target": "pub-19", "state": map[string]any{"state": "OVER_BUDGET"}},
				map[string]any{"target": "pub-20", "state": 7},
				map[string]any{"id": " "},
			}}
		},
	})
	z, creds, acct := ZeroparkAds{}, c.Creds{"api_token": "zp_test_not_a_real_token"}, c.AdAccount{ID: "zp-1001"}
	var _ c.AdTargetStateReader = z
	got, err := z.AdTargetStates(ctx, cl, creds, acct, cid)
	if err != nil || len(got) != 4 {
		t.Fatalf("targets %+v %v", got, err)
	}
	if g := got[0]; g.ID != "aaaaaaaa-0000-4000-8000-000000000001" || g.State != c.AdTargetActive || g.StateDetail != "" || g.Bid != (c.AdBid{Micros: 12_500}) ||
		g.MinBidMicros != 500 || len(g.Keys) != 2 || g.Source != "feed-a" {
		t.Fatalf("an active target %+v", g)
	}
	if g := got[1]; g.State != c.AdTargetPaused || g.Bid != (c.AdBid{Micros: 20_000, Auto: true}) || len(g.Keys) != 3 {
		t.Fatalf("a paused target %+v", g)
	}
	if g := got[2]; g.State != c.AdTargetStopped || g.StateDetail != "over_budget" || g.Bid.Micros != 0 {
		t.Fatalf("a target Zeropark holds back %+v", g)
	}
	// A state it can't read counts as active; a target without an id goes
	// by its name.
	if g := got[3]; g.State != c.AdTargetActive || g.ID != "pub-20" {
		t.Fatalf("a target with an unknown state %+v", g)
	}
	if _, err := z.AdTargetStates(ctx, cl, creds, acct, "../x"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a bad campaign id: %v", err)
	}
}

// Changes: a campaign is paused, resumed and given a daily budget, and a
// target paused and resumed, with Zeropark's own calls (arguments in the
// query string, the token in api-token).
func TestZeroparkAdsChanges(t *testing.T) {
	const cid = "11111111-1111-4111-8111-111111111111"
	state := "ACTIVE"
	budget := 50.0
	var paused []string
	ok := map[string]any{"state": "ACTIVE"}
	cl, reqs, _ := fake(t, map[string]any{
		"GET /api/stats/campaign/all": func(r *http.Request) any {
			if r.URL.Query().Get("campaignNameOrId") != cid {
				return map[string]any{"page": 0, "limit": 100, "total": 0, "elements": []any{}}
			}
			return map[string]any{"page": 0, "limit": 100, "total": 1, "elements": []any{map[string]any{
				"details": map[string]any{"id": cid, "name": "Push US", "state": map[string]any{"state": state}, "dailyBudget": map[string]any{"type": "LIMITED", "amount": budget}},
				"stats":   map[string]any{"spent": 1.5, "redirects": 300}}}}
		},
		"POST /api/campaign/" + cid + "/pause":  func(r *http.Request) any { state = "PAUSED"; return ok },
		"POST /api/campaign/" + cid + "/resume": func(r *http.Request) any { state = "ACTIVE"; return ok },
		"POST /api/campaign/" + cid + "/set/budget/daily": func(r *http.Request) any {
			budget, _ = strconv.ParseFloat(r.URL.Query().Get("budget"), 64)
			return ok
		},
		"POST /api/campaign/" + cid + "/targets/pause": func(r *http.Request) any {
			paused = append(paused, r.URL.Query()["hashOrAddress"]...)
			return map[string]any{"state": "PAUSED", "hashes": paused}
		},
		"POST /api/campaign/" + cid + "/targets/resume": func(r *http.Request) any {
			paused = nil
			return map[string]any{"state": "ACTIVE", "hashes": r.URL.Query()["hashOrAddress"]}
		},
	})
	z, creds, acct := ZeroparkAds{}, c.Creds{"api_token": "zp_test_not_a_real_token"}, c.AdAccount{ID: "zp-1001", Currency: "USD"}
	if d := z.Definition(); !d.Has(c.CapAdsManage) {
		t.Fatal("Zeropark isn't marked manageable")
	}

	cp, err := z.AdCampaign(ctx, cl, creds, acct, cid)
	if err != nil || cp.Status != c.AdActive || cp.DailyBudgetMicros == nil || *cp.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("campaign %+v %v", cp, err)
	}
	if _, err := z.AdCampaign(ctx, cl, creds, acct, "22222222-2222-4222-8222-222222222222"); err != c.ErrNotFound {
		t.Fatalf("an unknown campaign: %v", err)
	}
	if err := z.SetCampaignStatus(ctx, cl, creds, acct, cid, false); err != nil {
		t.Fatal(err)
	}
	if cp, _ := z.AdCampaign(ctx, cl, creds, acct, cid); cp.Status != c.AdPaused {
		t.Fatalf("after a pause: %+v", cp)
	}
	if err := z.SetCampaignStatus(ctx, cl, creds, acct, cid, true); err != nil || state != "ACTIVE" {
		t.Fatalf("resume: %v %s", err, state)
	}
	// The budget goes in dollars, rounded to the cent.
	if err := z.SetCampaignDailyBudget(ctx, cl, creds, acct, cid, 25_126_000); err != nil || budget != 25.13 {
		t.Fatalf("budget: %v %v", err, budget)
	}
	if err := z.SetCampaignDailyBudget(ctx, cl, creds, acct, cid, 0); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a zero budget: %v", err)
	}

	// A placement is a target, by its address or hash.
	if kinds := z.ExclusionKinds(); len(kinds) != 1 || kinds[0] != c.ExcludePlacement {
		t.Fatalf("exclusion kinds %v", kinds)
	}
	x := c.AdExclusion{Kind: c.ExcludePlacement, Value: "pub-17"}
	if err := z.AddExclusion(ctx, cl, creds, acct, cid, x); err != nil || len(paused) != 1 || paused[0] != "pub-17" {
		t.Fatalf("pause a target: %v %v", err, paused)
	}
	if err := z.RemoveExclusion(ctx, cl, creds, acct, cid, x); err != nil || paused != nil {
		t.Fatalf("resume a target: %v %v", err, paused)
	}
	if err := z.AddExclusion(ctx, cl, creds, acct, cid, c.AdExclusion{Kind: c.ExcludeDevice, Value: "mobile"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a device exclusion: %v", err)
	}
	if err := z.AddExclusion(ctx, cl, creds, acct, "../x", x); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a bad campaign id: %v", err)
	}
	for _, r := range *reqs {
		if r.header.Get("api-token") != "zp_test_not_a_real_token" {
			t.Fatalf("%s %s went without the token", r.method, r.path)
		}
	}
}

// Bids: the campaign's and one target's are read, and set within
// Zeropark's range with four decimals.
func TestZeroparkAdsBids(t *testing.T) {
	const cid = "11111111-1111-4111-8111-111111111111"
	var asked []string
	cl, _, _ := fake(t, map[string]any{
		"GET /api/stats/campaign/" + cid + "/bid": map[string]any{"value": 0.0125, "autoBid": false},
		"GET /api/stats/campaign/" + cid + "/targets": func(r *http.Request) any {
			if r.URL.Query().Get("targetAddresses") != "pub-17" {
				return map[string]any{"page": 0, "limit": 100, "total": 0, "elements": []any{}}
			}
			return map[string]any{"page": 0, "limit": 100, "total": 1, "elements": []any{
				map[string]any{"id": "aaaaaaaa-0000-4000-8000-000000000001", "target": "pub-17", "address": "pub-17", "bid": map[string]any{"value": 0.02, "autoBid": true}}}}
		},
		"POST /api/campaign/" + cid + "/bid": func(r *http.Request) any {
			asked = append(asked, "campaign?"+r.URL.RawQuery)
			return map[string]any{"value": 0.01}
		},
		"POST /api/campaign/" + cid + "/target/bid": func(r *http.Request) any {
			asked = append(asked, "target?"+r.URL.RawQuery)
			return map[string]any{"value": 0.01}
		},
	})
	z, creds, acct := ZeroparkAds{}, c.Creds{"api_token": "zp_test_not_a_real_token"}, c.AdAccount{ID: "zp-1001", Currency: "USD"}
	if d := z.Definition(); !d.Has(c.CapAdsBids) {
		t.Fatal("Zeropark isn't marked as taking bids")
	}
	b, err := z.Bid(ctx, cl, creds, acct, cid, "")
	if err != nil || b.Micros != 12_500 || b.Auto {
		t.Fatalf("campaign bid %+v %v", b, err)
	}
	b, err = z.Bid(ctx, cl, creds, acct, cid, "pub-17")
	if err != nil || b.Micros != 20_000 || !b.Auto {
		t.Fatalf("target bid %+v %v", b, err)
	}
	if _, err := z.Bid(ctx, cl, creds, acct, cid, "pub-99"); err != c.ErrNotFound {
		t.Fatalf("an unknown target: %v", err)
	}
	if err := z.SetBid(ctx, cl, creds, acct, cid, "", 9_500); err != nil {
		t.Fatal(err)
	}
	if err := z.SetBid(ctx, cl, creds, acct, cid, "pub-17", 15_000); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 || asked[0] != "campaign?bid=0.0095" || asked[1] != "target?bid=0.0150&hash=pub-17" {
		t.Fatalf("bid calls: %v", asked)
	}
	for _, bad := range []int64{0, 99, 5_000_001} {
		if err := z.SetBid(ctx, cl, creds, acct, cid, "", bad); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("a bid of %d: %v", bad, err)
		}
	}
}
