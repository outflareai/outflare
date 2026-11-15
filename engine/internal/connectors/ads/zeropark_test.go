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
