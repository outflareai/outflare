package ads

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// taboolaTokenRoute answers the client credentials flow for the fake.
func taboolaTokenRoute(r *http.Request) any {
	_ = r.ParseForm()
	if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "northpeak-client" ||
		r.Form.Get("client_secret") != "taboola-test-not-a-real-secret" {
		return map[string]any{"error": "invalid_client"}
	}
	return map[string]any{"access_token": "tok", "token_type": "bearer", "expires_in": 43200}
}

func taboolaCreds() c.Creds {
	return c.Creds{"client_id": "northpeak-client", "client_secret": "taboola-test-not-a-real-secret"}
}

func TestTaboolaAds(t *testing.T) {
	api := "/backstage/api/1.0"
	cl, reqs, _ := fake(t, map[string]any{
		"POST /backstage/oauth/token":           taboolaTokenRoute,
		"GET " + api + "/users/current/account": map[string]any{"id": 1163, "name": "Northpeak", "account_id": "northpeak", "currency": "USD"},
		// northpeak is not a network: its advertisers call is a 404.
		"GET " + api + "/users/current/allowed-accounts": map[string]any{"results": []any{
			map[string]any{"id": 1163, "name": "Northpeak", "account_id": "northpeak", "partner_types": []string{"ADVERTISER"}, "currency": "eur", "time_zone_name": "Europe/Berlin"},
			map[string]any{"id": 1164, "name": "Acme", "account_id": "acme", "partner_types": []string{"ADVERTISER"}, "currency": "USD"},
		}},
		"GET " + api + "/northpeak/campaigns/": func(r *http.Request) any {
			if r.URL.Query().Get("page") == "1" {
				return map[string]any{"results": []any{map[string]any{"id": "5750752", "name": "Spring", "status": "RUNNING", "daily_cap": 100.5, "marketing_objective": "LEADS_GENERATION"}},
					"metadata": map[string]any{"total": 3, "count": 1}}
			}
			return map[string]any{"results": []any{
				map[string]any{"id": "5750753", "name": "Old", "status": "TERMINATED", "daily_cap": 0},
				map[string]any{"id": "5750754", "name": "Held", "status": "PAUSED"},
			}, "metadata": map[string]any{"total": 3, "count": 2}}
		},
		"GET " + api + "/northpeak/reports/campaign-summary/dimensions/campaign_day_breakdown": func(r *http.Request) any {
			if r.URL.Query().Get("start_date") != "2026-09-01" {
				return map[string]any{"results": []any{}}
			}
			return map[string]any{"timezone": "CET", "results": []any{
				map[string]any{"date": "2026-10-01 00:00:00.0", "campaign": "5750752", "campaign_name": "Spring", "spent": 12.34, "impressions": 9000, "clicks": 40,
					"cpa_actions_num": 3, "conversions_value": 59.9, "currency": "EUR"},
				map[string]any{"date": "2026-10-01 00:00:00.0", "campaign": 5750753, "campaign_name": "Old", "spent": 0, "impressions": 0, "clicks": 0},
			}}
		},
	})
	tb, creds := TaboolaAds{}, taboolaCreds()
	acct0, err := tb.Test(ctx, cl, creds)
	if err != nil || acct0.ID != "northpeak" || acct0.Details["accounts"] != "1" {
		t.Fatalf("test %+v %v", acct0, err)
	}
	tokenReq := (*reqs)[0]
	if tokenReq.method != "POST" || tokenReq.header.Get("Content-Type") != "application/x-www-form-urlencoded" || !strings.Contains(tokenReq.body, "grant_type=client_credentials") {
		t.Fatalf("token exchange %+v", tokenReq)
	}
	for _, r := range (*reqs)[1:] {
		if r.path == "/backstage/oauth/token" {
			t.Fatal("the token is kept, not fetched again for every call")
		}
		if r.header.Get("Authorization") != "Bearer tok" {
			t.Fatalf("API call without the token: %+v", r)
		}
	}
	// Without an account id, the credentials' own account is read, alone.
	accts, err := tb.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].ID != "northpeak" || accts[0].Currency != "EUR" || accts[0].TimeZone != "Europe/Berlin" || accts[0].Meta["numeric_id"] != "1163" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := tb.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 3 || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 100_500_000 || camps[0].Objective != "leads_generation" ||
		camps[1].Status != c.AdRemoved || camps[1].DailyBudgetMicros != nil || camps[2].Status != c.AdPaused {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	n := len(*reqs)
	days, err := tb.AdDays(ctx, cl, creds, accts[0], "2026-09-01", "2026-10-02")
	if err != nil || len(days) != 1 {
		t.Fatalf("days %+v %v", days, err)
	}
	d := days[0]
	if d.Date != "2026-10-01" || d.CampaignID != "5750752" || d.CampaignName != "Spring" || d.SpendMicros != 12_340_000 || d.Impressions != 9000 || d.Clicks != 40 ||
		*d.Conversions != 3 || *d.ConversionValueMicros != 59_900_000 {
		t.Fatalf("day %+v", d)
	}
	// 32 days are two calls of at most 31 days, without empty campaigns.
	got := (*reqs)[n:]
	if len(got) != 2 || !strings.Contains(got[0].query, "end_date=2026-10-01") || !strings.Contains(got[1].query, "start_date=2026-10-02") ||
		!strings.Contains(got[0].query, "exclude_empty_campaigns=true") {
		t.Fatalf("report windows %+v", got)
	}
	// An account id the credentials can't reach is a clear refusal.
	creds["account_id"] = "elsewhere"
	if _, err := tb.AdAccounts(ctx, cl, creds); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("an unknown account: %v", err)
	}
}

func TestTaboolaAdsNetwork(t *testing.T) {
	api := "/backstage/api/1.0"
	cl, _, _ := fake(t, map[string]any{
		"POST /backstage/oauth/token": taboolaTokenRoute,
		"GET " + api + "/northpeak-network/advertisers": map[string]any{"results": []any{
			map[string]any{"id": 1306, "name": "Northpeak EU", "account_id": "northpeak-eu", "partner_types": []string{"ADVERTISER"}, "currency": "EUR", "time_zone_name": "Europe/Berlin"},
			map[string]any{"id": 1307, "name": "Northpeak US", "account_id": "northpeak-us", "partner_types": []string{"ADVERTISER"}, "currency": "USD", "time_zone_name": "US/Pacific"},
			map[string]any{"id": 1308, "name": "Northpeak Blog", "account_id": "northpeak-blog", "partner_types": []string{"PUBLISHER"}},
		}},
	})
	creds := taboolaCreds()
	creds["account_id"] = "northpeak-network"
	accts, err := TaboolaAds{}.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 2 || accts[1].ID != "northpeak-us" || accts[1].Meta["numeric_id"] != "1307" {
		t.Fatalf("a network lists its advertisers %+v %v", accts, err)
	}
}

func TestTaboolaAdsRefusals(t *testing.T) {
	var tokens, calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/backstage/oauth/token":
			_ = r.ParseForm()
			if r.Form.Get("client_secret") != "taboola-test-not-a-real-secret" {
				w.WriteHeader(400)
				w.Write([]byte("<BadClientCredentialsException><error>invalid_client</error></BadClientCredentialsException>"))
				return
			}
			tokens++
			json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("tok%d", tokens), "expires_in": 43200})
		case "/backstage/api/1.0/users/current/account":
			calls++
			// The first token is refused, as an expired one would be.
			if r.Header.Get("Authorization") == "Bearer tok1" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"account_id": "northpeak", "name": "Northpeak"})
		case "/backstage/api/1.0/northpeak/campaigns/":
			w.WriteHeader(400)
			w.Write([]byte(`{"http_status": 400, "message": "Invalid page"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{"taboola_ads": srv.URL}}
	tb := TaboolaAds{}
	bad := taboolaCreds()
	bad["client_secret"] = "wrong-not-a-real-secret"
	if _, err := tb.Test(ctx, cl, bad); c.KindOf(err) != c.ErrAuth || !strings.Contains(err.Error(), "client id and secret") {
		t.Fatalf("a wrong secret is an auth error: %v", err)
	}
	var me taboolaAccount
	if _, err := tb.get(ctx, cl, taboolaCreds(), "/users/current/account", nil, &me); err != nil || me.AccountID != "northpeak" || tokens != 2 || calls != 2 {
		t.Fatalf("a refused token is traded again once: %+v %v tokens=%d calls=%d", me, err, tokens, calls)
	}
	if _, err := tb.AdCampaigns(ctx, cl, taboolaCreds(), c.AdAccount{ID: "northpeak"}); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "Invalid page") {
		t.Fatalf("a bad request is not an auth error: %v", err)
	}
}

func TestTaboolaAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /backstage/oauth/token": taboolaTokenRoute,
		"GET /backstage/api/1.0/northpeak/universal_pixel/conversion_rule": map[string]any{"results": []any{
			map[string]any{"id": 60, "display_name": "Lead", "status": "ACTIVE", "type": "EVENT_BASED", "event_name": "lead"},
			map[string]any{"id": 61, "display_name": "Product View", "status": "ACTIVE", "type": "BASIC", "event_name": nil},
			map[string]any{"id": 62, "display_name": "Old", "status": "ARCHIVED", "type": "EVENT_BASED", "event_name": "old_signup"},
		}},
		"POST /1163/log/3/bulk-s2s-action": []byte{},
	})
	tb, creds := TaboolaAds{}, taboolaCreds()
	acct := c.AdAccount{ID: "northpeak", Meta: map[string]string{"numeric_id": "1163"}}
	ts, err := tb.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].ID != "lead" || ts[0].Name != "Lead" || len(ts[0].EventTypes) != 0 {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"tblci": "GiCtest-not-a-real-click"})
	other := conv(2, map[string]string{"tblci": "GiCsecond-not-a-real-click"})
	other.Currency = "PLN"
	empty := c.AdConversion{EventID: "msg-2", Index: 1, ClickIDs: map[string]string{"gclid": "g"}}
	res, err := tb.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv, empty, other})
	if err != nil || !res[0].OK || res[0].MatchKeys[0] != "tblci" || !res[1].Skipped || !res[2].OK {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	if last.path != "/1163/log/3/bulk-s2s-action" || last.header.Get("Authorization") != "" {
		t.Fatalf("bulk S2S goes to the numeric account id, without a token: %+v", last)
	}
	var body struct {
		Actions []map[string]any `json:"actions"`
	}
	if err := json.Unmarshal([]byte(last.body), &body); err != nil || len(body.Actions) != 2 {
		t.Fatalf("body %s %v", last.body, err)
	}
	a := body.Actions[0]
	if a["click-id"] != "GiCtest-not-a-real-click" || a["name"] != "lead" || a["timestamp"] != float64(time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC).UnixMilli()) ||
		a["revenue"] != 12.5 || a["currency"] != "USD" || a["orderid"] != "msg-1" {
		t.Fatalf("action %+v", a)
	}
	if _, ok := body.Actions[1]["revenue"]; ok {
		t.Fatalf("revenue in a currency Taboola doesn't take is left out: %+v", body.Actions[1])
	}
	if _, err := tb.SendConversions(ctx, cl, creds, c.AdAccount{ID: "northpeak"}, ts[0], []c.AdConversion{cv}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no numeric id: %v", err)
	}
}
