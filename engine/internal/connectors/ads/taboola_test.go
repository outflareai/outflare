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

// Changes: a campaign is paused, resumed and given a daily cap by a POST
// on its own address, and a publisher site blocked and unblocked by a
// PATCH that names only that site (Taboola's "Update a Campaign" and
// "Patching targeted publishers").
func TestTaboolaAdsChanges(t *testing.T) {
	api := "/backstage/api/1.0"
	camp := api + "/northpeak/campaigns/5750752"
	active := true
	var bodies []string
	cl, reqs, _ := fake(t, map[string]any{
		"POST /backstage/oauth/token": taboolaTokenRoute,
		"GET " + camp + "/": func(r *http.Request) any {
			return map[string]any{"id": 5750752, "name": "Spring", "status": "RUNNING", "is_active": active, "daily_cap": 100.5, "marketing_objective": "LEADS_GENERATION"}
		},
		"POST " + camp: func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if v, ok := b["is_active"].(bool); ok {
				active = v
			}
			raw, _ := json.Marshal(b)
			bodies = append(bodies, "POST "+string(raw))
			return map[string]any{"id": "5750752"}
		},
		"PATCH " + camp: func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			raw, _ := json.Marshal(b)
			bodies = append(bodies, "PATCH "+string(raw))
			return b
		},
	})
	tb, creds, acct := TaboolaAds{}, taboolaCreds(), c.AdAccount{ID: "northpeak", Currency: "USD"}
	if d := tb.Definition(); !d.Has(c.CapAdsManage) {
		t.Fatalf("capabilities %v", d.Capabilities)
	}
	got, err := tb.AdCampaign(ctx, cl, creds, acct, "5750752")
	if err != nil || got.Status != c.AdActive || got.Name != "Spring" || got.DailyBudgetMicros == nil || *got.DailyBudgetMicros != 100_500_000 {
		t.Fatalf("campaign %+v %v", got, err)
	}
	// Paused by the advertiser: Taboola still says RUNNING, with
	// is_active false.
	if err := tb.SetCampaignStatus(ctx, cl, creds, acct, "5750752", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := tb.AdCampaign(ctx, cl, creds, acct, "5750752"); got.Status != c.AdPaused || got.ProviderStatus != "RUNNING" {
		t.Fatalf("after the pause %+v", got)
	}
	if err := tb.SetCampaignStatus(ctx, cl, creds, acct, "5750752", true); err != nil {
		t.Fatal(err)
	}
	if err := tb.SetCampaignDailyBudget(ctx, cl, creds, acct, "5750752", 75_499_000); err != nil {
		t.Fatal(err)
	}
	x := c.AdExclusion{Kind: c.ExcludePlacement, Value: "examplenews-home"}
	if err := tb.AddExclusion(ctx, cl, creds, acct, "5750752", x); err != nil {
		t.Fatal(err)
	}
	if err := tb.RemoveExclusion(ctx, cl, creds, acct, "5750752", x); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`POST {"is_active":false}`, `POST {"is_active":true}`, `POST {"daily_cap":75.5}`,
		`PATCH {"patch_operation":"ADD","publisher_targeting":{"publishers":["examplenews-home"]}}`,
		`PATCH {"patch_operation":"REMOVE","publisher_targeting":{"publishers":["examplenews-home"]}}`,
	}
	if strings.Join(bodies, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent:\n%s", strings.Join(bodies, "\n"))
	}
	for _, r := range *reqs {
		if (r.method == "POST" || r.method == "PATCH") && r.path == camp && r.header.Get("Content-Type") != "application/json" {
			t.Fatalf("a change without a JSON body: %+v", r)
		}
	}
	// Refused before anything is sent: a campaign id that isn't a number,
	// no budget, something that isn't a site id, another kind.
	sent := len(bodies)
	if err := tb.SetCampaignStatus(ctx, cl, creds, acct, "../x", false); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a bad campaign id: %v", err)
	}
	if err := tb.SetCampaignDailyBudget(ctx, cl, creds, acct, "5750752", 0); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no budget: %v", err)
	}
	for _, bad := range []c.AdExclusion{{Kind: c.ExcludePlacement, Value: "https://site.example/a b"}, {Kind: c.ExcludePlacement}, {Kind: c.ExcludeDevice, Value: "mobile"}} {
		if err := tb.AddExclusion(ctx, cl, creds, acct, "5750752", bad); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if len(bodies) != sent {
		t.Fatalf("a refused change was sent: %v", bodies[sent:])
	}
	if _, err := tb.AdCampaign(ctx, cl, creds, acct, "999"); err != c.ErrNotFound {
		t.Fatalf("a campaign that isn't there: %v", err)
	}
	if kinds := tb.ExclusionKinds(); len(kinds) != 1 || kinds[0] != c.ExcludePlacement {
		t.Fatalf("kinds %v", kinds)
	}
}

// Ads come from the content report a day at a time, and their status from
// the item lists of the campaigns that spent.
func TestTaboolaCampaignAds(t *testing.T) {
	api := "/backstage/api/1.0"
	content := func(r *http.Request) any {
		switch r.URL.Query().Get("start_date") {
		case "2026-10-01":
			return map[string]any{"results": []any{
				map[string]any{"item": "301", "item_name": "Ten desk lamps", "thumbnail_url": "https://cdn.acme.example/lamp.jpg", "url": "https://acme.example/lamps",
					"campaign": "5750752", "spent": 7.95, "impressions": 61609, "clicks": 40, "actions": 4, "conversions_value": 80.5},
				map[string]any{"item": 302, "item_name": "Old headline", "url": "https://acme.example/chairs", "campaign": 5750752, "spent": 1.05, "impressions": 900, "clicks": 3},
				map[string]any{"item": "303", "item_name": "Nothing shown", "campaign": "5750752", "spent": 0, "impressions": 0, "clicks": 0},
			}}
		case "2026-10-02":
			return map[string]any{"results": []any{
				map[string]any{"item": "302", "item_name": "New headline", "url": "https://acme.example/chairs", "campaign": "5750752", "spent": 2, "impressions": 1000, "clicks": 5},
				map[string]any{"item": "401", "item_name": "Small campaign", "url": "https://acme.example/rugs", "campaign": "5750760", "spent": 0.5, "impressions": 100, "clicks": 1},
			}}
		}
		return map[string]any{"results": []any{}}
	}
	cl, reqs, _ := fake(t, map[string]any{
		"POST /backstage/oauth/token": taboolaTokenRoute,
		"GET " + api + "/northpeak/reports/top-campaign-content/dimensions/item_breakdown": content,
		"GET " + api + "/northpeak/campaigns/5750752/items/": map[string]any{"results": []any{
			map[string]any{"id": "301", "campaign_id": "5750752", "type": "ITEM", "url": "https://acme.example/lamps", "thumbnail_url": "https://cdn.acme.example/lamp.jpg",
				"title": "Ten desk lamps", "approval_state": "APPROVED", "is_active": true, "status": "RUNNING"},
			map[string]any{"id": 302, "type": "ITEM", "url": "https://acme.example/chairs", "title": "New headline", "approval_state": "APPROVED", "is_active": false, "status": "PAUSED"},
			map[string]any{"id": "304", "campaign_id": "5750752", "type": "ITEM", "url": "https://acme.example/sofas", "title": "Not allowed", "approval_state": "REJECTED", "status": "REJECTED"},
		}},
		"GET " + api + "/northpeak/campaigns/5750760/items/": map[string]any{"results": []any{}},
	})
	tb, creds, acct := TaboolaAds{}, taboolaCreds(), c.AdAccount{ID: "northpeak", Currency: "USD"}
	items, days, err := tb.CampaignAds(ctx, cl, creds, acct, "2026-10-01", "2026-10-03")
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 4 || days[0].ID != "301" || days[0].ParentID != "5750752" || days[0].Date != "2026-10-01" || days[0].SpendMicros != 7_950_000 || days[0].Clicks != 40 ||
		*days[0].Conversions != 4 || *days[0].ConversionValueMicros != 80_500_000 || days[1].ID != "302" || days[1].Conversions != nil ||
		days[2].Date != "2026-10-02" || days[3].ID != "401" || days[3].ParentID != "5750760" {
		t.Fatalf("days %+v", days)
	}
	// The listed items come first, then the one the lists didn't have,
	// which says nothing about its status.
	if len(items) != 4 || items[0].ID != "301" || items[0].Status != c.AdActive || items[0].ParentID != "5750752" || items[0].Details[c.AdDetailHeadline] != "Ten desk lamps" ||
		items[0].Details[c.AdDetailPolicy] != "approved" || items[0].Details[c.AdDetailThumbnail] == "" || items[0].Details[c.AdDetailType] != "item" ||
		items[1].ID != "302" || items[1].Status != c.AdPaused || items[1].ParentID != "5750752" || items[1].Name != "New headline" ||
		items[2].ID != "304" || items[2].Status != c.AdOther || items[2].ProviderStatus != "REJECTED" || items[2].Details[c.AdDetailPolicy] != "disapproved" ||
		items[3].ID != "401" || items[3].ProviderStatus != "" || items[3].ParentID != "5750760" || items[3].Details[c.AdDetailURL] != "https://acme.example/rugs" {
		t.Fatalf("items %+v", items)
	}
	if _, ok := items[0].Details[c.AdDetailTemplated]; ok {
		t.Fatal("Taboola's tracking code is on the campaign, so an item says nothing about the template")
	}
	// One report call a day, then the campaign that spent most first.
	var calls []string
	for _, r := range *reqs {
		switch {
		case strings.Contains(r.path, "/reports/"):
			calls = append(calls, r.query)
		case strings.HasSuffix(r.path, "/items/"):
			calls = append(calls, r.path[len(api):])
		}
	}
	want := []string{"end_date=2026-10-01&start_date=2026-10-01", "end_date=2026-10-02&start_date=2026-10-02", "end_date=2026-10-03&start_date=2026-10-03",
		"/northpeak/campaigns/5750752/items/", "/northpeak/campaigns/5750760/items/"}
	if strings.Join(calls, " ") != strings.Join(want, " ") {
		t.Fatalf("calls %v", calls)
	}
	// A long window reads its last 31 days only.
	n := len(*reqs)
	if _, _, err := tb.CampaignAds(ctx, cl, creds, acct, "2026-07-01", "2026-09-30"); err != nil {
		t.Fatal(err)
	}
	if got := (*reqs)[n:]; len(got) != 31 || !strings.Contains(got[0].query, "start_date=2026-08-31") {
		t.Fatalf("%d calls, first %s", len(got), got[0].query)
	}
}

// One item is paused and resumed with is_active, by its campaign and id.
func TestTaboolaAdStatus(t *testing.T) {
	api := "/backstage/api/1.0"
	var bodies []string
	cl, reqs, _ := fake(t, map[string]any{
		"POST /backstage/oauth/token": taboolaTokenRoute,
		"POST " + api + "/northpeak/campaigns/5750752/items/301/": func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			raw, _ := json.Marshal(b)
			bodies = append(bodies, string(raw))
			return map[string]any{"id": "301", "campaign_id": "5750752", "is_active": b["is_active"]}
		},
	})
	tb, creds, acct := TaboolaAds{}, taboolaCreds(), c.AdAccount{ID: "northpeak", Currency: "USD"}
	if err := tb.SetAdStatus(ctx, cl, creds, acct, "5750752", "301", false); err != nil {
		t.Fatal(err)
	}
	if err := tb.SetAdStatus(ctx, cl, creds, acct, "5750752", "301", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(bodies, " ") != `{"is_active":false} {"is_active":true}` {
		t.Fatalf("bodies %v", bodies)
	}
	n := len(*reqs)
	for _, bad := range [][2]string{{"", "301"}, {"5750752", ""}, {"5750752", "30x"}, {"../x", "301"}} {
		if err := tb.SetAdStatus(ctx, cl, creds, acct, bad[0], bad[1], false); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("%v: %v", bad, err)
		}
	}
	if len(*reqs) != n {
		t.Fatal("a bad id reached Taboola")
	}
}
