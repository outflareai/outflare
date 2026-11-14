package ads

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

var ctx = context.Background()

type seen struct {
	method, path, query, body string
	header                    http.Header
}

// fake serves canned answers by "METHOD path" and records the requests.
func fake(t *testing.T, routes map[string]any) (*c.Client, *[]seen, string) {
	t.Helper()
	var reqs []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(b))
		reqs = append(reqs, seen{r.Method, r.URL.Path, r.URL.RawQuery, string(b), r.Header.Clone()})
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(401)
			return
		}
		v, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if fn, ok := v.(func(*http.Request) any); ok {
			v = fn(r)
		}
		if raw, ok := v.([]byte); ok {
			w.Write(raw)
			return
		}
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{}}
	for _, id := range []string{"google_ads", "google_ads.datamanager", "meta_ads", "microsoft_ads.customer", "microsoft_ads.campaign", "microsoft_ads.reporting",
		"reddit_ads", "reddit_ads.conversions", "x_ads", "x_ads.auth", "linkedin_ads", "tiktok_ads", "snapchat_ads", "snapchat_ads.conversions",
		"taboola_ads", "taboola_ads.conversions", "outbrain_ads", "apple_ads", "apple_ads.token", "amazon_ads.na", "amazon_ads.token", "pinterest_ads", "chatgpt_ads", "chatgpt_ads.events"} {
		cl.BaseURLs[id] = srv.URL
	}
	return cl, &reqs, srv.URL
}

func TestDefinitions(t *testing.T) {
	ids := map[string]bool{}
	for _, conn := range All() {
		d := conn.Definition()
		if ids[d.ID] {
			t.Fatalf("duplicate id %s", d.ID)
		}
		ids[d.ID] = true
		if d.Category != c.CategoryAds || !d.Has(c.CapAdsRead) {
			t.Errorf("%s: want category ads with ads.read", d.ID)
		}
		if _, ok := conn.(c.AdsReader); !ok {
			t.Errorf("%s does not implement AdsReader", d.ID)
		}
		if d.Supports(c.AuthOAuth2) && d.OAuth == nil {
			t.Errorf("%s offers OAuth without a consent screen", d.ID)
		}
	}
	if len(ids) != 14 {
		t.Fatalf("want fourteen ad platforms, got %d", len(ids))
	}
}

func TestMissingSetup(t *testing.T) {
	// Google retired developer tokens: nothing is asked for beyond the
	// sign-in.
	if got := c.MissingSetup(GoogleAds{}, c.Creds{"access_token": "t"}); got != nil {
		t.Fatalf("google ads needs no setup: %+v", got)
	}
	if got := c.MissingSetup(MetaAds{}, c.Creds{}); got != nil {
		t.Fatalf("meta needs no setup: %+v", got)
	}
}

func TestGoogleAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /v23/customers:listAccessibleCustomers": map[string]any{"resourceNames": []string{"customers/111", "customers/900"}},
		"POST /v23/customers/111/googleAds:search": func(r *http.Request) any {
			b, _ := io.ReadAll(r.Body)
			q := string(b)
			switch {
			case strings.Contains(q, "FROM customer\""):
				return map[string]any{"results": []any{map[string]any{"customer": map[string]any{"id": "111", "descriptiveName": "Northpeak", "currencyCode": "USD", "timeZone": "America/New_York"}}}}
			case strings.Contains(q, "segments.date"):
				return map[string]any{"results": []any{map[string]any{
					"campaign": map[string]any{"id": "7", "name": "Brand search"}, "segments": map[string]any{"date": "2026-10-01"},
					"metrics": map[string]any{"costMicros": "12500000", "impressions": "1000", "clicks": "40", "conversions": 3.0, "conversionsValue": 450.5},
				}}}
			default:
				return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "7", "name": "Brand search", "status": "ENABLED", "advertisingChannelType": "SEARCH", "campaignBudget": "customers/111/campaignBudgets/70"},
					"campaignBudget": map[string]any{"amountMicros": "50000000"}}}}
			}
		},
		"POST /v23/customers/111/campaigns:mutate":       map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/campaigns/7"}}},
		"POST /v23/customers/111/campaignBudgets:mutate": map[string]any{"results": []any{map[string]any{"resourceName": "customers/111/campaignBudgets/70"}}},
		"POST /v23/customers/900/googleAds:search": func(r *http.Request) any {
			b, _ := io.ReadAll(r.Body)
			if strings.Contains(string(b), "customer_client") {
				return map[string]any{"results": []any{map[string]any{"customerClient": map[string]any{"id": "222", "descriptiveName": "Acme", "currencyCode": "EUR", "timeZone": "Europe/Berlin", "status": "ENABLED"}}}}
			}
			return map[string]any{"results": []any{map[string]any{"customer": map[string]any{"id": "900", "manager": true}}}}
		},
	})
	creds := c.Creds{"access_token": "tok", "developer_token": "dev"}
	g := GoogleAds{}
	accts, err := g.AdAccounts(ctx, cl, creds)
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 2 || accts[0].ID != "111" || accts[1].ID != "222" || accts[1].Meta["login_customer_id"] != "900" || accts[1].Currency != "EUR" {
		t.Fatalf("accounts: %+v", accts)
	}
	// Google retired developer tokens: one a workspace stored is never
	// sent.
	for _, r := range *reqs {
		if r.header.Get("developer-token") != "" {
			t.Fatalf("%s %s sent a developer token", r.method, r.path)
		}
	}
	camps, err := g.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 1 || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 50_000_000 || camps[0].Objective != "search" {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	days, err := g.AdDays(ctx, cl, creds, accts[0], "2026-10-01", "2026-10-02")
	if err != nil || len(days) != 1 {
		t.Fatalf("days %+v %v", days, err)
	}
	d := days[0]
	if d.CampaignID != "7" || d.SpendMicros != 12_500_000 || d.Clicks != 40 || *d.Conversions != 3 || *d.ConversionValueMicros != 450_500_000 {
		t.Fatalf("day %+v", d)
	}
	if _, err := g.AdAccounts(ctx, cl, c.Creds{"access_token": "tok"}); err != nil {
		t.Fatalf("without a developer token: %v", err)
	}
	if last := (*reqs)[len(*reqs)-1]; last.header.Get("developer-token") != "" {
		t.Fatalf("an empty developer token was sent: %v", last.header)
	}

	// Managing: one campaign read by id, paused, and its budget set, each
	// as a mutate of that one field.
	cp, err := g.AdCampaign(ctx, cl, creds, accts[0], "7")
	if err != nil || cp.ID != "7" || cp.Status != c.AdActive || *cp.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("campaign %+v %v", cp, err)
	}
	if _, err := g.AdCampaign(ctx, cl, creds, accts[0], "8"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("missing campaign: %v", err)
	}
	if err := g.SetCampaignStatus(ctx, cl, creds, accts[0], "7", false); err != nil {
		t.Fatal(err)
	}
	if err := g.SetCampaignDailyBudget(ctx, cl, creds, accts[0], "7", 60_000_000); err != nil {
		t.Fatal(err)
	}
	var mutates []string
	for _, r := range *reqs {
		if strings.HasSuffix(r.path, ":mutate") {
			mutates = append(mutates, r.path+" "+r.body)
		}
	}
	if len(mutates) != 2 || !strings.Contains(mutates[0], `"status":"PAUSED"`) || !strings.Contains(mutates[0], `"updateMask":"status"`) ||
		!strings.Contains(mutates[1], `"resourceName":"customers/111/campaignBudgets/70"`) || !strings.Contains(mutates[1], `"amountMicros":"60000000"`) {
		t.Fatalf("mutates: %v", mutates)
	}
	if err := g.SetCampaignDailyBudget(ctx, cl, creds, accts[0], "7", 0); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no budget: %v", err)
	}
}

func TestMetaAds(t *testing.T) {
	var base string
	cl, reqs, u := fake(t, map[string]any{
		"GET /v24.0/me": map[string]any{"id": "1", "name": "Ana"},
		"GET /v24.0/me/adaccounts": func(r *http.Request) any {
			if r.URL.Query().Get("after") == "" {
				return map[string]any{"data": []any{map[string]any{"account_id": "55", "name": "Northpeak", "currency": "USD", "timezone_name": "America/Los_Angeles"}},
					"paging": map[string]any{"next": base + "/v24.0/me/adaccounts?after=x"}}
			}
			return map[string]any{"data": []any{map[string]any{"account_id": "56", "name": "Acme", "currency": "JPY"}},
				"paging": map[string]any{"next": "https://evil.example/steal"}}
		},
		"GET /v24.0/act_55/campaigns": map[string]any{"data": []any{map[string]any{"id": "9", "name": "Prospecting", "effective_status": "PAUSED", "objective": "OUTCOME_LEADS", "daily_budget": "2500"}}},
		"GET /v24.0/9":                map[string]any{"id": "9", "name": "Prospecting", "effective_status": "PAUSED", "objective": "OUTCOME_LEADS", "daily_budget": "2500", "account_id": "55"},
		"POST /v24.0/9":               map[string]any{"success": true},
		"GET /v24.0/act_55/insights": map[string]any{"data": []any{map[string]any{
			"campaign_id": "9", "campaign_name": "Prospecting", "date_start": "2026-10-01", "spend": "25.40", "impressions": "900", "clicks": "12",
			"actions":       []any{map[string]any{"action_type": "lead", "value": "2"}, map[string]any{"action_type": "link_click", "value": "12"}},
			"action_values": []any{map[string]any{"action_type": "lead", "value": "80"}},
		}}},
	})
	base = u
	m := MetaAds{}
	creds := c.Creds{"api_key": "tok"}
	accts, err := m.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 2 || accts[1].Currency != "JPY" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := m.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 1 || camps[0].Status != c.AdPaused || *camps[0].DailyBudgetMicros != 25_000_000 || camps[0].Objective != "leads" {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	days, err := m.AdDays(ctx, cl, creds, accts[0], "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].SpendMicros != 25_400_000 || *days[0].Conversions != 2 || *days[0].ConversionValueMicros != 80_000_000 {
		t.Fatalf("days %+v %v", days, err)
	}
	if _, err := m.Test(ctx, cl, c.Creds{"api_key": "bad"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad token: %v", err)
	}

	// Managing: the campaign by id, resumed, and its budget in cents.
	cp, err := m.AdCampaign(ctx, cl, creds, accts[0], "9")
	if err != nil || cp.Status != c.AdPaused || *cp.DailyBudgetMicros != 25_000_000 {
		t.Fatalf("campaign %+v %v", cp, err)
	}
	if _, err := m.AdCampaign(ctx, cl, creds, accts[1], "9"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("another account's campaign: %v", err)
	}
	if err := m.SetCampaignStatus(ctx, cl, creds, accts[0], "9", true); err != nil {
		t.Fatal(err)
	}
	if err := m.SetCampaignDailyBudget(ctx, cl, creds, accts[0], "9", 30_500_000); err != nil {
		t.Fatal(err)
	}
	var posts []string
	for _, r := range *reqs {
		if r.method == "POST" && r.path == "/v24.0/9" {
			posts = append(posts, r.body)
		}
	}
	if len(posts) != 2 || !strings.Contains(posts[0], `"status":"ACTIVE"`) || !strings.Contains(posts[1], `"daily_budget":"3050"`) {
		t.Fatalf("posts: %v", posts)
	}
	if err := m.SetCampaignStatus(ctx, cl, creds, accts[0], "9x", true); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad id: %v", err)
	}
}

func TestMicrosoftAds(t *testing.T) {
	old := msPoll
	msPoll.every, msPoll.limit = time.Millisecond, time.Second
	t.Cleanup(func() { msPoll = old })
	var zipped bytes.Buffer
	zw := zip.NewWriter(&zipped)
	f, _ := zw.Create("report.csv")
	f.Write([]byte("\ufeff\"TimePeriod\",\"CampaignId\",\"CampaignName\",\"Spend\",\"Impressions\",\"Clicks\",\"Conversions\",\"Revenue\"\r\n" +
		"\"2026-10-01\",\"31\",\"Bing brand\",\"1,204.50\",\"20000\",\"300\",\"7\",\"900.00\"\r\n" +
		"\"10/2/2026\",\"31\",\"Bing brand\",\"10.00\",\"100\",\"2\",\"0\",\"0\"\r\n"))
	zw.Close()
	polls := 0
	var srvURL string
	cl, reqs, u := fake(t, map[string]any{
		"POST /User/Query":                 map[string]any{"User": map[string]any{"Id": 5}},
		"POST /Accounts/Search":            map[string]any{"Accounts": []any{map[string]any{"Id": 77, "Name": "Northpeak", "CurrencyCode": "USD", "TimeZone": "PacificTimeUSCanadaTijuana", "ParentCustomerId": 8}}},
		"POST /Campaigns/QueryByAccountId": map[string]any{"Campaigns": []any{map[string]any{"Id": 31, "Name": "Bing brand", "Status": "Active", "CampaignType": "Search", "DailyBudget": 20.0}}},
		"POST /Campaigns/QueryByIds":       map[string]any{"Campaigns": []any{map[string]any{"Id": 31, "Name": "Bing brand", "Status": "Paused", "CampaignType": "Search", "DailyBudget": 20.0}}},
		"POST /Campaigns/Update":           map[string]any{"PartialErrors": []any{}},
		"POST /GenerateReport/Submit":      map[string]any{"ReportRequestId": "r1"},
		"GET /download/report.zip":         zipped.Bytes(),
		"POST /GenerateReport/Poll": func(*http.Request) any {
			polls++
			if polls == 1 {
				return map[string]any{"ReportRequestStatus": map[string]any{"Status": "Pending"}}
			}
			return map[string]any{"ReportRequestStatus": map[string]any{"Status": "Success", "ReportDownloadUrl": srvURL + "/download/report.zip"}}
		},
	})
	srvURL = u
	ms := MicrosoftAds{}
	creds := c.Creds{"access_token": "tok", "developer_token": "dev"}
	accts, err := ms.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].ID != "77" || accts[0].Meta["customer_id"] != "8" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := ms.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 1 || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 20_000_000 {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	days, err := ms.AdDays(ctx, cl, creds, accts[0], "2026-10-01", "2026-10-02")
	if err != nil || len(days) != 2 {
		t.Fatalf("days %+v %v", days, err)
	}
	if days[0].SpendMicros != 1_204_500_000 || days[0].Impressions != 20000 || *days[0].ConversionValueMicros != 900_000_000 || days[1].Date != "2026-10-02" {
		t.Fatalf("days %+v", days)
	}
	for _, r := range *reqs {
		if strings.HasPrefix(r.path, "/GenerateReport") && (r.header.Get("CustomerAccountId") != "77" || r.header.Get("CustomerId") != "8" || r.header.Get("DeveloperToken") != "dev") {
			t.Fatalf("report call missing account headers: %v", r.header)
		}
	}
	// Managing: one campaign by id, paused, and a budget to the cent.
	cp, err := ms.AdCampaign(ctx, cl, creds, accts[0], "31")
	if err != nil || cp.Status != c.AdPaused || *cp.DailyBudgetMicros != 20_000_000 {
		t.Fatalf("campaign %+v %v", cp, err)
	}
	if err := ms.SetCampaignStatus(ctx, cl, creds, accts[0], "31", false); err != nil {
		t.Fatal(err)
	}
	if err := ms.SetCampaignDailyBudget(ctx, cl, creds, accts[0], "31", 25_505_000); err != nil {
		t.Fatal(err)
	}
	var updates []string
	for _, r := range *reqs {
		if r.path == "/Campaigns/Update" {
			updates = append(updates, r.body)
		}
	}
	if len(updates) != 2 || !strings.Contains(updates[0], `"Status":"Paused"`) || !strings.Contains(updates[1], `"DailyBudget":25.51`) || !strings.Contains(updates[1], `"AccountId":77`) {
		t.Fatalf("updates: %v", updates)
	}
	if err := ms.SetCampaignStatus(ctx, cl, creds, accts[0], "x", true); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad id: %v", err)
	}
}

func TestRedditAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /me":                        map[string]any{"data": map[string]any{"id": "t2_1", "username": "northpeak"}},
		"GET /me/businesses":             map[string]any{"data": []any{map[string]any{"id": "b1"}}},
		"GET /businesses/b1/ad_accounts": map[string]any{"data": []any{map[string]any{"id": "a1", "name": "Northpeak", "currency": "usd", "time_zone_id": "America/Chicago"}}},
		"GET /ad_accounts/a1/campaigns":  map[string]any{"data": []any{map[string]any{"id": "c1", "name": "Subreddit test", "effective_status": "ACTIVE", "objective": "CLICKS"}}},
		"POST /ad_accounts/a1/reports":   map[string]any{"data": map[string]any{"metrics": []any{map[string]any{"campaign_id": "c1", "date": "2026-10-01T00:00:00Z", "spend": 3_000_000, "impressions": 500, "clicks": 9}}}},
	})
	r := RedditAds{}
	creds := c.Creds{"access_token": "tok"}
	accts, err := r.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].Currency != "USD" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := r.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 1 || camps[0].Status != c.AdActive {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	days, err := r.AdDays(ctx, cl, creds, accts[0], "2026-10-01", "2026-10-03")
	if err != nil || len(days) != 1 || days[0].SpendMicros != 3_000_000 || days[0].Date != "2026-10-01" || days[0].Conversions != nil {
		t.Fatalf("days %+v %v", days, err)
	}
	last := (*reqs)[len(*reqs)-1]
	if !strings.Contains(last.body, `"ends_at":"2026-10-04T00:00:00Z"`) || !strings.Contains(last.body, `"America/Chicago"`) {
		t.Fatalf("report body %s", last.body)
	}
}

func TestXAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /12/accounts":                           map[string]any{"data": []any{map[string]any{"id": "18ce5", "name": "Northpeak", "timezone": "America/New_York"}}},
		"GET /12/accounts/18ce5/funding_instruments": map[string]any{"data": []any{map[string]any{"currency": "GBP"}}},
		"GET /12/accounts/18ce5/campaigns":           map[string]any{"data": []any{map[string]any{"id": "k1", "name": "Launch", "entity_status": "ACTIVE"}}},
		"GET /12/stats/accounts/18ce5": map[string]any{"data": []any{map[string]any{"id": "k1", "id_data": []any{map[string]any{"metrics": map[string]any{
			"billed_charge_local_micro": []any{1_000_000, nil, 2_000_000}, "impressions": []any{100, nil, 50}, "clicks": []any{3, nil, 1}}}}}}},
	})
	creds := c.Creds{"consumer_key": "ck", "consumer_secret": "cs", "access_token": "at", "access_token_secret": "ats"}
	x := XAds{}
	accts, err := x.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].Currency != "GBP" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	days, err := x.AdDays(ctx, cl, creds, accts[0], "2026-10-01", "2026-10-03")
	if err != nil || len(days) != 2 || days[0].Date != "2026-10-01" || days[1].Date != "2026-10-03" || days[1].SpendMicros != 2_000_000 || days[0].CampaignName != "Launch" {
		t.Fatalf("days %+v %v", days, err)
	}
	for _, r := range *reqs {
		if !strings.HasPrefix(r.header.Get("Authorization"), `OAuth oauth_consumer_key="ck"`) {
			t.Fatalf("unsigned request: %q", r.header.Get("Authorization"))
		}
		if strings.Contains(r.path, "/stats/") && !strings.Contains(r.query, "start_time=2026-10-01T00%3A00%3A00-04%3A00") {
			t.Fatalf("stats window not at the account's midnight: %s", r.query)
		}
	}
}

// The OAuth 1.0a header carries the nonce and timestamp, and its signature
// depends on the secrets.
func TestOAuth1Signature(t *testing.T) {
	creds := c.Creds{"consumer_key": "test-consumer-key-not-real", "consumer_secret": "test-consumer-secret-not-real",
		"access_token": "test-access-token-not-real", "access_token_secret": "test-access-secret-not-real"}
	h := oauth1Header("GET", "https://api.x.com/1.1/statuses/user_timeline.json", map[string][]string{"screen_name": {"example"}}, creds,
		"testnonce0001", time.Unix(1318622958, 0))
	for _, want := range []string{`oauth_nonce="testnonce0001"`, `oauth_timestamp="1318622958"`, `oauth_signature="`} {
		if !strings.Contains(h, want) {
			t.Fatalf("header %s lacks %s", h, want)
		}
	}
	// Same inputs, same signature; a different secret, a different one.
	h2 := oauth1Header("GET", "https://api.x.com/1.1/statuses/user_timeline.json", map[string][]string{"screen_name": {"example"}}, creds,
		"testnonce0001", time.Unix(1318622958, 0))
	creds["consumer_secret"] = "other"
	h3 := oauth1Header("GET", "https://api.x.com/1.1/statuses/user_timeline.json", map[string][]string{"screen_name": {"example"}}, creds,
		"testnonce0001", time.Unix(1318622958, 0))
	if h != h2 || h == h3 {
		t.Fatal("signature is not stable or ignores the secret")
	}
	if pct("a b~*") != "a%20b~%2A" {
		t.Fatalf("pct: %s", pct("a b~*"))
	}
}

func TestLinkedInAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /adAccountUsers":                 map[string]any{"elements": []any{map[string]any{"account": "urn:li:sponsoredAccount:501"}}},
		"GET /adAccounts/501":                 map[string]any{"id": 501, "name": "Northpeak", "currency": "USD", "status": "ACTIVE"},
		"GET /adAccounts/501/adCampaigns":     map[string]any{"elements": []any{map[string]any{"id": 61, "name": "ABM", "status": "ACTIVE", "objectiveType": "LEAD_GENERATION", "dailyBudget": map[string]any{"amount": "100.00"}}}},
		"GET /adAccounts/501/adCampaigns/61":  map[string]any{"id": 61, "name": "ABM", "status": "PAUSED", "objectiveType": "LEAD_GENERATION", "dailyBudget": map[string]any{"amount": "100.00"}},
		"POST /adAccounts/501/adCampaigns/61": map[string]any{},
		"GET /adAnalytics": map[string]any{"elements": []any{map[string]any{"pivotValues": []string{"urn:li:sponsoredCampaign:61"},
			"dateRange": map[string]any{"start": map[string]any{"year": 2026, "month": 10, "day": 1}}, "costInLocalCurrency": "88.10", "impressions": 4000, "clicks": 31,
			"externalWebsiteConversions": 2, "conversionValueInLocalCurrency": "0"}}},
	})
	l := LinkedInAds{}
	creds := c.Creds{"access_token": "tok"}
	accts, err := l.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].ID != "501" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := l.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 1 || *camps[0].DailyBudgetMicros != 100_000_000 {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	days, err := l.AdDays(ctx, cl, creds, accts[0], "2026-10-01", "2026-10-07")
	if err != nil || len(days) != 1 || days[0].CampaignID != "61" || days[0].SpendMicros != 88_100_000 || days[0].Date != "2026-10-01" {
		t.Fatalf("days %+v %v", days, err)
	}
	last := (*reqs)[len(*reqs)-1]
	if !strings.Contains(last.query, "accounts=List(urn%3Ali%3AsponsoredAccount%3A501)") || !strings.Contains(last.query, "dateRange=(start:(year:2026,month:10,day:1),end:(year:2026,month:10,day:7))") {
		t.Fatalf("analytics query %s", last.query)
	}
	if last.header.Get("LinkedIn-Version") == "" || last.header.Get("X-Restli-Protocol-Version") != "2.0.0" {
		t.Fatal("missing LinkedIn version headers")
	}
	// Managing: the campaign by id, resumed, and a budget as a decimal in
	// the account's currency, each a Rest.li partial update.
	cp, err := l.AdCampaign(ctx, cl, creds, accts[0], "61")
	if err != nil || cp.Status != c.AdPaused || *cp.DailyBudgetMicros != 100_000_000 {
		t.Fatalf("campaign %+v %v", cp, err)
	}
	if err := l.SetCampaignStatus(ctx, cl, creds, accts[0], "61", true); err != nil {
		t.Fatal(err)
	}
	if err := l.SetCampaignDailyBudget(ctx, cl, creds, accts[0], "61", 120_500_000); err != nil {
		t.Fatal(err)
	}
	var patches []string
	for _, r := range *reqs {
		if r.method == "POST" && r.path == "/adAccounts/501/adCampaigns/61" {
			patches = append(patches, r.header.Get("X-RestLi-Method")+" "+r.body)
		}
	}
	if len(patches) != 2 || !strings.Contains(patches[0], `PARTIAL_UPDATE {"patch":{"$set":{"status":"ACTIVE"}}}`) ||
		!strings.Contains(patches[1], `"dailyBudget":{"amount":"120.50","currencyCode":"USD"}`) {
		t.Fatalf("patches: %v", patches)
	}
}

// conv is a conversion with every key, hashed as adconv hashes them.
func conv(i int, clicks map[string]string) c.AdConversion {
	v := int64(12_500_000)
	return c.AdConversion{EventID: "msg-1", Time: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC), ValueMicros: &v, Currency: "USD",
		ClickIDs: clicks, Emails: []string{c.HashID("Ana@Acme.example ")}, Phones: []string{c.HashID("+15550100")},
		PhoneDigits: []string{c.HashID("15550100")}, ExternalID: c.HashID("rec-1"), ConsentGranted: true, Index: i}
}

func TestHashID(t *testing.T) {
	if c.HashID(" Ana@Acme.example") != c.HashID("ana@acme.example") || len(c.HashID("x")) != 64 {
		t.Fatal("HashID must trim, lowercase and give sha256 hex")
	}
}

func TestGoogleAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": map[string]any{"results": []any{map[string]any{"conversionAction": map[string]any{"id": "42", "name": "Paid order"}}}},
		"POST /v23/customers/111:uploadClickConversions": map[string]any{"partialFailureError": map[string]any{"message": "1 failed", "details": []any{map[string]any{"errors": []any{
			map[string]any{"message": "click too old", "location": map[string]any{"fieldPathElements": []any{map[string]any{"fieldName": "conversions", "index": 1}}}}}}}}},
	})
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "111"}
	ts, err := g.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].ID != "42" || ts[0].Name != "Paid order" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	none := c.AdConversion{EventID: "msg-3", Time: time.Now(), Index: 7}
	res, err := g.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{conv(0, map[string]string{"gclid": "g1"}), conv(1, map[string]string{"wbraid": "w1"}), none})
	if err != nil || len(res) != 3 {
		t.Fatalf("send %+v %v", res, err)
	}
	if !res[0].OK || res[1].OK || res[1].Reason != "click too old" || !res[2].Skipped || res[2].Index != 7 {
		t.Fatalf("results %+v", res)
	}
	body := (*reqs)[len(*reqs)-1].body
	for _, want := range []string{`"gclid":"g1"`, `"wbraid":"w1"`, `"orderId":"msg-1"`, `"conversionAction":"customers/111/conversionActions/42"`,
		`"conversionDateTime":"2026-10-01 09:30:00+00:00"`, `"conversionValue":12.5`, `"adUserData":"GRANTED"`, `"partialFailure":true`, c.HashID("ana@acme.example")} {
		if !strings.Contains(body, want) {
			t.Fatalf("upload body lacks %s: %s", want, body)
		}
	}
}

func TestMetaAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /v24.0/act_55/adspixels": map[string]any{"data": []any{map[string]any{"id": "777", "name": "Pixel"}}},
		"POST /v24.0/777/events":      map[string]any{"events_received": 1},
	})
	m, creds, acct := MetaAds{}, c.Creds{"api_key": "tok"}, c.AdAccount{ID: "55"}
	ts, err := m.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].EventTypes[0] != "Purchase" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"fbclid": "abc"})
	cv.EventType, cv.ClickTime = "Purchase", time.UnixMilli(1_700_000_000_000)
	res, err := m.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv})
	if err != nil || !res[0].OK {
		t.Fatalf("send %+v %v", res, err)
	}
	body := (*reqs)[len(*reqs)-1].body
	for _, want := range []string{`"fbc":"fb.1.1700000000000.abc"`, `"event_id":"msg-1"`, `"event_name":"Purchase"`, `"ph":["` + c.HashID("15550100") + `"]`, `"value":12.5`} {
		if !strings.Contains(body, want) {
			t.Fatalf("events body lacks %s: %s", want, body)
		}
	}
}

func TestMicrosoftAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /ConversionGoals/QueryByIds": map[string]any{"ConversionGoals": []any{map[string]any{"Id": 1, "Name": "Offline sale", "Status": "Active"}, map[string]any{"Id": 2, "Name": "Old", "Status": "Deleted"}}},
		"POST /OfflineConversions/Apply":   map[string]any{"PartialErrors": []any{map[string]any{"Index": 0, "Message": "unknown click"}}},
	})
	ms, creds, acct := MicrosoftAds{}, c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "77", Meta: map[string]string{"customer_id": "8"}}
	ts, err := ms.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].ID != "Offline sale" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	res, err := ms.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{conv(0, map[string]string{"msclkid": "m1"}), conv(1, nil)})
	if err != nil || res[0].OK || res[0].Reason != "unknown click" || !res[1].OK {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	if !strings.Contains(last.body, `"MicrosoftClickId":"m1"`) || !strings.Contains(last.body, `"ConversionName":"Offline sale"`) || last.header.Get("CustomerAccountId") != "77" {
		t.Fatalf("apply %s %v", last.body, last.header)
	}
}

func TestRedditAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{"POST /conversions/events/a1": map[string]any{}})
	r, acct := RedditAds{}, c.AdAccount{ID: "a1"}
	if _, err := r.ConversionTargets(ctx, cl, c.Creds{"access_token": "tok"}, acct); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("without a conversions token: %v", err)
	}
	if got := c.MissingSetup(RedditAds{}, c.Creds{}); len(got) != 0 {
		t.Fatalf("the conversions token is optional: %+v", got)
	}
	creds := c.Creds{"access_token": "tok", "conversions_token": "ct"}
	ts, err := r.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"rdt_cid": "r1"})
	cv.EventType = "Purchase"
	res, err := r.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv})
	if err != nil || !res[0].OK {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	if last.header.Get("Authorization") != "Bearer ct" || !strings.Contains(last.body, `"click_id":"r1"`) || !strings.Contains(last.body, `"tracking_type":"Purchase"`) || !strings.Contains(last.body, `"conversion_id":"msg-1"`) {
		t.Fatalf("reddit %s %v", last.body, last.header)
	}
}

func TestTikTokAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /advertiser/info/": map[string]any{"code": 0, "message": "OK", "data": map[string]any{"list": []any{
			map[string]any{"advertiser_id": "700", "name": "Northpeak", "currency": "usd", "timezone": "Etc/GMT+5", "display_timezone": "America/New_York"}}}},
		"GET /campaign/get/": map[string]any{"code": 0, "data": map[string]any{
			"list": []any{
				map[string]any{"campaign_id": "c1", "campaign_name": "Spark", "operation_status": "ENABLE", "objective_type": "WEB_CONVERSIONS", "budget": 50.5, "budget_mode": "BUDGET_MODE_DAY"},
				map[string]any{"campaign_id": "c2", "campaign_name": "Old", "operation_status": "DISABLE", "secondary_status": "CAMPAIGN_STATUS_DELETE", "budget_mode": "BUDGET_MODE_INFINITE"},
			},
			"page_info": map[string]any{"page": 1, "total_page": 1}}},
		"GET /report/integrated/get/": map[string]any{"code": 0, "data": map[string]any{
			"list": []any{map[string]any{
				"dimensions": map[string]any{"campaign_id": "c1", "stat_time_day": "2026-10-01 00:00:00"},
				"metrics":    map[string]any{"campaign_name": "Spark", "spend": "12.34", "impressions": "1000", "clicks": "25", "conversion": "3"}}},
			"page_info": map[string]any{"page": 1, "total_page": 1}}},
		"POST /campaign/status/update/": map[string]any{"code": 0, "message": "OK"},
		"POST /campaign/update/":        map[string]any{"code": 40002, "message": "budget is below the minimum"},
	})
	tk := TikTokAds{}
	if _, err := tk.AdAccounts(ctx, cl, c.Creds{"access_token": "tok"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no granted accounts: %v", err)
	}
	creds := c.Creds{"access_token": "tok", "advertiser_ids": "700"}
	accts, err := tk.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].Currency != "USD" || accts[0].TimeZone != "America/New_York" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	first := (*reqs)[len(*reqs)-1]
	if first.header.Get("Access-Token") != "tok" || !strings.Contains(first.query, "advertiser_ids=%5B%22700%22%5D") {
		t.Fatalf("advertiser info call %s %v", first.query, first.header)
	}
	camps, err := tk.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 2 || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 50_500_000 ||
		camps[1].Status != c.AdRemoved || camps[1].DailyBudgetMicros != nil {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	// 45 days go in two windows of at most 30.
	n := len(*reqs)
	days, err := tk.AdDays(ctx, cl, creds, accts[0], "2026-09-01", "2026-10-15")
	if err != nil || len(days) != 2 || days[0].SpendMicros != 12_340_000 || days[0].Date != "2026-10-01" || *days[0].Conversions != 3 || days[0].Clicks != 25 {
		t.Fatalf("days %+v %v", days, err)
	}
	if got := (*reqs)[n:]; len(got) != 2 || !strings.Contains(got[0].query, "end_date=2026-09-30") || !strings.Contains(got[1].query, "start_date=2026-10-01") {
		t.Fatalf("report windows %+v", got)
	}
	// Managing: the campaign by id (a filtered get), paused, and a budget
	// the platform refuses inside an HTTP 200.
	cp, err := tk.AdCampaign(ctx, cl, creds, accts[0], "c1")
	if err != nil || cp.Status != c.AdActive || *cp.DailyBudgetMicros != 50_500_000 {
		t.Fatalf("campaign %+v %v", cp, err)
	}
	if q := (*reqs)[len(*reqs)-1].query; !strings.Contains(q, "filtering=") || !strings.Contains(q, "c1") {
		t.Fatalf("campaign get query %s", q)
	}
	if _, err := tk.AdCampaign(ctx, cl, creds, accts[0], "c9"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("missing campaign: %v", err)
	}
	if err := tk.SetCampaignStatus(ctx, cl, creds, accts[0], "c1", false); err != nil {
		t.Fatal(err)
	}
	if last := (*reqs)[len(*reqs)-1]; last.path != "/campaign/status/update/" || !strings.Contains(last.body, `"operation_status":"DISABLE"`) || !strings.Contains(last.body, `"campaign_ids":["c1"]`) {
		t.Fatalf("status update %+v", last)
	}
	if err := tk.SetCampaignDailyBudget(ctx, cl, creds, accts[0], "c1", 1_000_000); err == nil || !strings.Contains(err.Error(), "below the minimum") {
		t.Fatalf("refused budget: %v", err)
	}
}

func TestTikTokAdsRefusals(t *testing.T) {
	cl, _, _ := fake(t, map[string]any{
		"GET /advertiser/info/": map[string]any{"code": 40105, "message": "Access token is incorrect or has been revoked."},
		"GET /campaign/get/":    map[string]any{"code": 40002, "message": "advertiser_id is invalid"},
	})
	tk, creds := TikTokAds{}, c.Creds{"access_token": "tok", "advertiser_ids": "700"}
	if _, err := tk.AdAccounts(ctx, cl, creds); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a revoked token is an auth error: %v", err)
	}
	if _, err := tk.AdCampaigns(ctx, cl, creds, c.AdAccount{ID: "700"}); err == nil || c.KindOf(err) == c.ErrAuth {
		t.Fatalf("a bad request is not an auth error: %v", err)
	}
}

func TestTikTokAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /pixel/list/":   map[string]any{"code": 0, "data": map[string]any{"pixels": []any{map[string]any{"pixel_code": "PX1", "pixel_name": "Site"}}, "page_info": map[string]any{"page": 1, "total_page": 1}}},
		"POST /event/track/": map[string]any{"code": 0, "message": "OK"},
	})
	tk, acct := TikTokAds{}, c.AdAccount{ID: "700"}
	if got := c.MissingSetup(TikTokAds{}, c.Creds{}); len(got) != 0 {
		t.Fatalf("the events token is optional: %+v", got)
	}
	creds := c.Creds{"access_token": "tok"}
	ts, err := tk.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].ID != "PX1" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"ttclid": "tt1", "ttp": "cookie1"})
	cv.EventType = "CompletePayment"
	empty := c.AdConversion{EventID: "msg-2", Index: 1, ClickIDs: map[string]string{"gclid": "g"}}
	res, err := tk.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv, empty})
	if err != nil || !res[0].OK || !res[1].Skipped {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	for _, want := range []string{`"event_source_id":"PX1"`, `"event":"CompletePayment"`, `"ttclid":"tt1"`, `"ttp":"cookie1"`, `"event_id":"msg-1"`, `"value":12.5`, `"email":"` + c.HashID("ana@acme.example") + `"`} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("event body lacks %s: %s", want, last.body)
		}
	}
	if last.header.Get("Access-Token") != "tok" {
		t.Fatalf("sends with the sign-in token: %v", last.header)
	}
	// An Events API token, when given, is used instead.
	if _, err := tk.SendConversions(ctx, cl, c.Creds{"access_token": "tok", "events_token": "ev"}, acct, ts[0], []c.AdConversion{cv}); err != nil {
		t.Fatal(err)
	}
	if got := (*reqs)[len(*reqs)-1].header.Get("Access-Token"); got != "ev" {
		t.Fatalf("events token not used: %q", got)
	}
}

// TikTok's sign-in names the app app_id and trades an auth_code in JSON.
func TestTikTokSignIn(t *testing.T) {
	d := TikTokAds{}.Definition()
	app := c.OAuthApp{ClientID: "app-test-not-real", ClientSecret: "secret-test-not-real"}
	u := c.AuthURL(d, app, "https://app.example.com/v1/integrations/oauth/callback", "st", "verifier")
	if !strings.HasPrefix(u, "https://business-api.tiktok.com/portal/auth?") || !strings.Contains(u, "app_id=app-test-not-real") || !strings.Contains(u, "state=st") || strings.Contains(u, "client_id") {
		t.Fatalf("auth url %s", u)
	}
	cl, reqs, srv := fake(t, map[string]any{
		"POST /oauth2/access_token": map[string]any{"code": 0, "message": "OK", "data": map[string]any{"access_token": "tok", "advertiser_ids": []string{"700", "701"}}},
	})
	cl.BaseURLs["tiktok_ads.token"] = srv + "/oauth2/access_token/"
	creds, err := c.Exchange(ctx, cl, d, app, "code-1", "https://app.example.com/cb", "verifier")
	if err != nil || creds["access_token"] != "tok" || creds["advertiser_ids"] != "700,701" || creds["expires_at"] != "" {
		t.Fatalf("exchange %+v %v", creds, err)
	}
	if body := (*reqs)[0].body; !strings.Contains(body, `"auth_code":"code-1"`) || !strings.Contains(body, `"secret":"secret-test-not-real"`) {
		t.Fatalf("exchange body %s", body)
	}
	// No refresh token and no expiry: the token is kept as it is.
	if same, refreshed, err := c.Refresh(ctx, cl, d, app, creds); err != nil || refreshed || same["access_token"] != "tok" {
		t.Fatalf("refresh %+v %v %v", same, refreshed, err)
	}
	cl2, _, srv2 := fake(t, map[string]any{"POST /oauth2/access_token": map[string]any{"code": 40108, "message": "auth_code expired"}})
	cl2.BaseURLs["tiktok_ads.token"] = srv2 + "/oauth2/access_token/"
	if _, err := c.Exchange(ctx, cl2, d, app, "old", "https://app.example.com/cb", "v"); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a refused code: %v", err)
	}
}

func TestSnapchatAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /me": map[string]any{"me": map[string]any{"id": "u1", "display_name": "Northpeak"}},
		"GET /me/organizations": map[string]any{"organizations": []any{map[string]any{"organization": map[string]any{"id": "o1",
			"ad_accounts": []any{map[string]any{"id": "a1", "name": "Northpeak", "currency": "eur", "timezone": "Europe/Berlin"}}}}}},
		"GET /adaccounts/a1/campaigns": func(r *http.Request) any {
			if r.URL.Query().Get("cursor") == "" {
				return map[string]any{"campaigns": []any{map[string]any{"campaign": map[string]any{"id": "c1", "name": "Lens", "status": "ACTIVE", "objective": "WEB_CONVERSION", "daily_budget_micro": 20_000_000}}},
					"paging": map[string]any{"next_link": "http://" + r.Host + r.URL.Path + "?cursor=2"}}
			}
			return map[string]any{"campaigns": []any{map[string]any{"campaign": map[string]any{"id": "c2", "name": "Old", "status": "PAUSED"}}}}
		},
		"GET /adaccounts/a1/stats": map[string]any{"timeseries_stats": []any{map[string]any{"timeseries_stat": map[string]any{"breakdown_stats": map[string]any{
			"campaign": []any{map[string]any{"id": "c1", "timeseries": []any{
				map[string]any{"start_time": "2026-10-01T00:00:00.000+02:00", "stats": map[string]any{"impressions": 800, "swipes": 12, "spend": 4_500_000, "conversion_purchases": 2}},
				map[string]any{"start_time": "2026-10-02T00:00:00.000+02:00", "stats": map[string]any{"impressions": 0, "swipes": 0, "spend": 0}},
			}}}}}}}},
	})
	sn := SnapchatAds{}
	creds := c.Creds{"access_token": "tok"}
	acct0, err := sn.Test(ctx, cl, creds)
	if err != nil || acct0.Name != "Northpeak" {
		t.Fatalf("test %+v %v", acct0, err)
	}
	accts, err := sn.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].Currency != "EUR" || accts[0].Meta["organization_id"] != "o1" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	// The second page comes from the next link, a full URL on the API host.
	camps, err := sn.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 2 || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 20_000_000 || camps[1].Status != c.AdPaused {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	n := len(*reqs)
	days, err := sn.AdDays(ctx, cl, creds, accts[0], "2026-09-01", "2026-10-02")
	if err != nil || len(days) != 2 || days[0].SpendMicros != 4_500_000 || days[0].Clicks != 12 || days[0].Date != "2026-10-01" || *days[0].Conversions != 2 {
		t.Fatalf("days %+v %v", days, err)
	}
	// 32 days are two calls, each from midnight in Berlin.
	got := (*reqs)[n:]
	if len(got) != 2 || !strings.Contains(got[0].query, "start_time=2026-09-01T00%3A00%3A00%2B02%3A00") || !strings.Contains(got[1].query, "start_time=2026-10-02T00%3A00%3A00%2B02%3A00") {
		t.Fatalf("stats windows %+v", got)
	}
}

func TestSnapchatAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /adaccounts/a1/pixels": map[string]any{"pixels": []any{map[string]any{"pixel": map[string]any{"id": "PX9", "name": "Site"}}}},
		"POST /PX9/events":          map[string]any{"status": "VALID"},
	})
	sn, acct := SnapchatAds{}, c.AdAccount{ID: "a1"}
	if got := c.MissingSetup(SnapchatAds{}, c.Creds{}); len(got) != 0 {
		t.Fatalf("the conversions token is optional: %+v", got)
	}
	creds := c.Creds{"access_token": "tok"}
	ts, err := sn.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].ID != "PX9" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"ScCid": "sc1", "scid": "cookie9"})
	cv.EventType = "PURCHASE"
	empty := c.AdConversion{EventID: "msg-2", Index: 1, ClickIDs: map[string]string{"gclid": "g"}}
	res, err := sn.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv, empty})
	if err != nil || !res[0].OK || !res[1].Skipped {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	for _, want := range []string{`"event_name":"PURCHASE"`, `"sc_click_id":"sc1"`, `"sc_cookie1":"cookie9"`, `"event_id":"msg-1"`, `"action_source":"WEB"`, `"value":12.5`, `"ph":["` + c.HashID("15550100") + `"]`} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("event body lacks %s: %s", want, last.body)
		}
	}
	if last.header.Get("Authorization") != "Bearer tok" {
		t.Fatalf("sends with the sign-in token: %v", last.header)
	}
	if _, err := sn.SendConversions(ctx, cl, c.Creds{"access_token": "tok", "conversions_token": "ct"}, acct, ts[0], []c.AdConversion{cv}); err != nil {
		t.Fatal(err)
	}
	if got := (*reqs)[len(*reqs)-1].header.Get("Authorization"); got != "Bearer ct" {
		t.Fatalf("conversions token not used: %q", got)
	}
}

func TestPinterestAds(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /user_account": map[string]any{"username": "northpeak"},
		"GET /ad_accounts": func(r *http.Request) any {
			if r.URL.Query().Get("bookmark") == "" {
				return map[string]any{"items": []any{map[string]any{"id": "549", "name": "Northpeak", "currency": "usd"}}, "bookmark": "b2"}
			}
			return map[string]any{"items": []any{map[string]any{"id": "550", "name": "Acme", "currency": "gbp"}}, "bookmark": nil}
		},
		"GET /ad_accounts/549/campaigns": map[string]any{"items": []any{
			map[string]any{"id": "626", "name": "Boards", "status": "ACTIVE", "objective_type": "CONSIDERATION", "daily_spend_cap": 15_000_000},
			map[string]any{"id": "627", "name": "Old", "status": "ARCHIVED"},
		}},
		"GET /ad_accounts/549/campaigns/analytics": []any{
			map[string]any{"CAMPAIGN_ID": 626, "DATE": "2026-10-01", "SPEND_IN_MICRO_DOLLAR": 7_250_000, "IMPRESSION_1": 900, "CLICKTHROUGH_1": 30, "TOTAL_CONVERSIONS": 4},
			map[string]any{"CAMPAIGN_ID": "627", "DATE": "2026-10-01", "SPEND_IN_MICRO_DOLLAR": 0, "IMPRESSION_1": 0, "CLICKTHROUGH_1": 0},
		},
	})
	pn := PinterestAds{}
	creds := c.Creds{"access_token": "tok"}
	if a, err := pn.Test(ctx, cl, creds); err != nil || a.Name != "northpeak" || a.Details["accounts"] != "2" {
		t.Fatalf("test %+v %v", a, err)
	}
	accts, err := pn.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 2 || accts[0].Currency != "USD" || accts[1].ID != "550" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := pn.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 2 || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 15_000_000 || camps[1].Status != c.AdRemoved {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	n := len(*reqs)
	days, err := pn.AdDays(ctx, cl, creds, accts[0], "2026-07-01", "2026-10-01")
	if err != nil || len(days) != 2 || days[0].CampaignID != "626" || days[0].CampaignName != "Boards" || days[0].SpendMicros != 7_250_000 || *days[0].Conversions != 4 {
		t.Fatalf("days %+v %v", days, err)
	}
	// 93 days are two analytics calls (after the campaign list).
	var calls []seen
	for _, r := range (*reqs)[n:] {
		if strings.HasSuffix(r.path, "/analytics") {
			calls = append(calls, r)
		}
	}
	if len(calls) != 2 || !strings.Contains(calls[0].query, "end_date=2026-09-28") || !strings.Contains(calls[1].query, "start_date=2026-09-29") || !strings.Contains(calls[0].query, "campaign_ids=626%2C627") {
		t.Fatalf("analytics calls %+v", calls)
	}
}

func TestPinterestAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /ad_accounts/549/events": map[string]any{"num_events_received": 2, "num_events_processed": 1, "events": []any{
			map[string]any{"status": "processed"}, map[string]any{"status": "failed", "error_message": "event_time too old"}}},
	})
	pn, acct := PinterestAds{}, c.AdAccount{ID: "549"}
	if _, err := pn.ConversionTargets(ctx, cl, c.Creds{"access_token": "tok"}, acct); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("without a conversion token: %v", err)
	}
	creds := c.Creds{"access_token": "tok", "conversions_token": "ct"}
	ts, err := pn.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"epik": "ep1"})
	cv.EventType = "checkout"
	cv2 := conv(1, nil)
	empty := c.AdConversion{EventID: "msg-3", Index: 2, ClickIDs: map[string]string{"gclid": "g"}}
	res, err := pn.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv, cv2, empty})
	if err != nil || !res[0].OK || res[1].OK || res[1].Reason != "event_time too old" || !res[2].Skipped {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	for _, want := range []string{`"event_name":"checkout"`, `"click_id":"ep1"`, `"action_source":"web"`, `"value":"12.5"`, `"ph":["` + c.HashID("15550100") + `"]`} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("event body lacks %s: %s", want, last.body)
		}
	}
	if last.header.Get("Authorization") != "Bearer ct" {
		t.Fatalf("sends with the conversion token: %v", last.header)
	}
}

func TestMetaAdItems(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /v24.0/act_55/adsets": map[string]any{"data": []any{map[string]any{"id": "31", "name": "Lookalike 1%", "effective_status": "CAMPAIGN_PAUSED", "campaign_id": "9", "daily_budget": "1500"}}},
		"GET /v24.0/act_55/ads": map[string]any{"data": []any{map[string]any{"id": "41", "name": "Video A", "effective_status": "ACTIVE", "adset_id": "31",
			"creative": map[string]any{"thumbnail_url": "https://cdn.example/a.jpg", "url_tags": "utm_source=facebook&ad_id={{ad.id}}",
				"object_story_spec": map[string]any{"link_data": map[string]any{"link": "https://acme.example/demo", "name": "Book a demo", "message": "See Acme in 20 minutes."}}}}}},
		"GET /v24.0/act_55/insights": func(r *http.Request) any {
			if r.URL.Query().Get("level") == "ad" {
				return map[string]any{"data": []any{map[string]any{"ad_id": "41", "ad_name": "Video A", "adset_id": "31", "date_start": "2026-10-01",
					"spend": "4.20", "impressions": "300", "clicks": "6", "actions": []any{map[string]any{"action_type": "purchase", "value": "1"}},
					"action_values": []any{map[string]any{"action_type": "purchase", "value": "49"}}}}}
			}
			return map[string]any{"data": []any{map[string]any{"adset_id": "31", "adset_name": "Lookalike 1%", "campaign_id": "9", "date_start": "2026-10-01",
				"spend": "8.40", "impressions": "600", "clicks": "9"}}}
		},
	})
	m := MetaAds{}
	creds, acct := c.Creds{"api_key": "tok"}, c.AdAccount{ID: "55", Currency: "USD"}
	sets, err := m.AdItems(ctx, cl, creds, acct, c.AdLevelGroup)
	if err != nil || len(sets) != 1 || sets[0].ParentID != "9" || sets[0].Status != c.AdPaused || *sets[0].DailyBudgetMicros != 15_000_000 {
		t.Fatalf("ad sets %+v %v", sets, err)
	}
	ads, err := m.AdItems(ctx, cl, creds, acct, c.AdLevelAd)
	if err != nil || len(ads) != 1 || ads[0].ParentID != "31" || ads[0].Details[c.AdDetailHeadline] != "Book a demo" ||
		ads[0].Details[c.AdDetailURL] != "https://acme.example/demo" || ads[0].Details[c.AdDetailURLTags] == "" || ads[0].Details[c.AdDetailThumbnail] == "" {
		t.Fatalf("ads %+v %v", ads, err)
	}
	days, err := m.AdItemDays(ctx, cl, creds, acct, c.AdLevelGroup, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].ID != "31" || days[0].ParentID != "9" || days[0].SpendMicros != 8_400_000 {
		t.Fatalf("ad set days %+v %v", days, err)
	}
	days, err = m.AdItemDays(ctx, cl, creds, acct, c.AdLevelAd, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].ID != "41" || days[0].ParentID != "31" || *days[0].Conversions != 1 || *days[0].ConversionValueMicros != 49_000_000 {
		t.Fatalf("ad days %+v %v", days, err)
	}
	for _, r := range *reqs {
		if strings.HasSuffix(r.path, "/insights") && !strings.Contains(r.query, "level=ad") {
			t.Fatalf("insights without a level: %s", r.query)
		}
	}
	if _, err := m.AdItems(ctx, cl, creds, acct, "keyword"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad level: %v", err)
	}
}

func TestGoogleAdItems(t *testing.T) {
	cl, _, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": func(r *http.Request) any {
			b, _ := io.ReadAll(r.Body)
			q := string(b)
			switch {
			case strings.Contains(q, "FROM ad_group_ad WHERE segments.date"):
				return map[string]any{"results": []any{map[string]any{"adGroupAd": map[string]any{"ad": map[string]any{"id": "81"}}, "adGroup": map[string]any{"id": "71"},
					"segments": map[string]any{"date": "2026-10-01"}, "metrics": map[string]any{"costMicros": "2000000", "impressions": "100", "clicks": "4", "conversions": 1.0}}}}
			case strings.Contains(q, "FROM ad_group WHERE segments.date"):
				return map[string]any{"results": []any{map[string]any{"adGroup": map[string]any{"id": "71", "name": "Exact"}, "campaign": map[string]any{"id": "7"},
					"segments": map[string]any{"date": "2026-10-01"}, "metrics": map[string]any{"costMicros": "5000000", "impressions": "400", "clicks": "10"}}}}
			case strings.Contains(q, "FROM ad_group_ad"):
				return map[string]any{"results": []any{map[string]any{"adGroup": map[string]any{"id": "71"}, "adGroupAd": map[string]any{"status": "ENABLED", "ad": map[string]any{
					"id": "81", "type": "RESPONSIVE_SEARCH_AD", "finalUrls": []string{"https://acme.example/pricing"},
					"responsiveSearchAd": map[string]any{"headlines": []any{map[string]any{"text": "Acme pricing"}}, "descriptions": []any{map[string]any{"text": "Plans for every team."}}}}}}}}
			case strings.Contains(q, "FROM ad_group"):
				return map[string]any{"results": []any{map[string]any{"adGroup": map[string]any{"id": "71", "name": "Exact", "status": "PAUSED"}, "campaign": map[string]any{"id": "7"}}}}
			}
			return map[string]any{"results": []any{}}
		},
	})
	g := GoogleAds{}
	creds, acct := c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "111", Currency: "USD"}
	groups, err := g.AdItems(ctx, cl, creds, acct, c.AdLevelGroup)
	if err != nil || len(groups) != 1 || groups[0].ID != "71" || groups[0].ParentID != "7" || groups[0].Status != c.AdPaused {
		t.Fatalf("ad groups %+v %v", groups, err)
	}
	ads, err := g.AdItems(ctx, cl, creds, acct, c.AdLevelAd)
	// An ad without a name of its own takes its first headline.
	if err != nil || len(ads) != 1 || ads[0].ID != "81" || ads[0].ParentID != "71" || ads[0].Name != "Acme pricing" ||
		ads[0].Details[c.AdDetailURL] != "https://acme.example/pricing" || ads[0].Details[c.AdDetailType] != "responsive_search_ad" || ads[0].Details[c.AdDetailText] != "Plans for every team." {
		t.Fatalf("ads %+v %v", ads, err)
	}
	days, err := g.AdItemDays(ctx, cl, creds, acct, c.AdLevelGroup, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].ID != "71" || days[0].ParentID != "7" || days[0].SpendMicros != 5_000_000 {
		t.Fatalf("ad group days %+v %v", days, err)
	}
	days, err = g.AdItemDays(ctx, cl, creds, acct, c.AdLevelAd, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].ID != "81" || days[0].ParentID != "71" || *days[0].Conversions != 1 {
		t.Fatalf("ad days %+v %v", days, err)
	}
}

func TestGoogleAdsAudiences(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": map[string]any{"results": []any{map[string]any{"userList": map[string]any{"id": "5", "name": "Trial users"}}}},
		"POST /v1/audienceMembers:ingest":          map[string]any{"requestId": "req-1"},
		"POST /v1/audienceMembers:remove":          map[string]any{"requestId": "req-2"},
	})
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok"}, c.AdAccount{ID: "111"}
	if !g.Definition().Has(c.CapAdsAudiences) {
		t.Fatal("Google Ads should sync audiences")
	}
	as, err := g.Audiences(ctx, cl, creds, acct)
	if err != nil || len(as) != 1 || as[0].ID != "5" || as[0].Name != "Trial users" {
		t.Fatalf("audiences %+v %v", as, err)
	}
	if q := (*reqs)[0].body; !strings.Contains(q, "CRM_BASED") {
		t.Fatalf("query %s", q)
	}
	em := c.HashID("ana@acme.example")
	m := c.AudienceMember{Emails: []string{em}, Phones: []string{c.HashID("+15550100")}, ConsentGranted: true}
	if err := g.SyncAudience(ctx, cl, creds, acct, "5", c.AudienceAdd, m); err != nil {
		t.Fatal(err)
	}
	got := *reqs
	if len(got) != 2 || got[1].path != "/v1/audienceMembers:ingest" || !strings.Contains(got[1].body, `"productDestinationId":"5"`) ||
		!strings.Contains(got[1].body, `"adPersonalization":"CONSENT_GRANTED"`) || !strings.Contains(got[1].body, `"emailAddress":"`+em+`"`) ||
		got[1].header.Get("Developer-Token") != "" {
		t.Fatalf("add calls %+v", got)
	}
	m.ConsentGranted = false
	if err := g.SyncAudience(ctx, cl, creds, acct, "5", c.AudienceRemove, m); err != nil {
		t.Fatal(err)
	}
	got = *reqs
	if got[2].path != "/v1/audienceMembers:remove" || strings.Contains(got[2].body, "consent") {
		t.Fatalf("remove calls %+v", got[2:])
	}
	// No identifiers: nothing to do, nothing called.
	if err := g.SyncAudience(ctx, cl, creds, acct, "5", c.AudienceAdd, c.AudienceMember{}); err != nil || len(*reqs) != 3 {
		t.Fatalf("empty: %v, %d calls", err, len(*reqs))
	}
}

func TestMetaAdsAudiences(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /v24.0/act_55/customaudiences": map[string]any{"data": []any{
			map[string]any{"id": "801", "name": "Customers", "subtype": "CUSTOM"},
			map[string]any{"id": "802", "name": "Site visitors", "subtype": "WEBSITE"}}},
		"POST /v24.0/801/users":   map[string]any{"audience_id": "801", "num_received": 1},
		"DELETE /v24.0/801/users": map[string]any{"audience_id": "801", "num_received": 1},
	})
	m, creds, acct := MetaAds{}, c.Creds{"api_key": "tok"}, c.AdAccount{ID: "55"}
	as, err := m.Audiences(ctx, cl, creds, acct)
	if err != nil || len(as) != 1 || as[0].ID != "801" {
		t.Fatalf("audiences %+v %v", as, err)
	}
	em, ph := c.HashID("ana@acme.example"), c.HashID("15550100")
	p := c.AudienceMember{Emails: []string{em}, PhoneDigits: []string{ph}}
	if err := m.SyncAudience(ctx, cl, creds, acct, "801", c.AudienceAdd, p); err != nil {
		t.Fatal(err)
	}
	got := *reqs
	if len(got) != 3 || got[1].method != "POST" || got[1].body != `{"payload":{"data":[["`+em+`"]],"schema":["EMAIL_SHA256"]}}` ||
		!strings.Contains(got[2].body, `"PHONE_SHA256"`) || !strings.Contains(got[2].body, ph) {
		t.Fatalf("add calls %+v", got)
	}
	if err := m.SyncAudience(ctx, cl, creds, acct, "801", c.AudienceRemove, c.AudienceMember{Emails: []string{em}}); err != nil {
		t.Fatal(err)
	}
	if got := *reqs; len(got) != 4 || got[3].method != "DELETE" || !strings.Contains(got[3].body, em) {
		t.Fatalf("remove calls %+v", got)
	}
	if err := m.SyncAudience(ctx, cl, c.Creds{"api_key": "bad"}, acct, "801", c.AudienceAdd, p); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("a refused token: %v", err)
	}
}

// Meta reports a purchase as purchase, omni_purchase and the pixel's own:
// it counts once. Custom conversions are listed but not counted, and link
// clicks, landing page views, video views and reach come with the day.
func TestMetaResults(t *testing.T) {
	acts := []metaAction{{"purchase", 3}, {"omni_purchase", 3}, {"offsite_conversion.fb_pixel_purchase", 3}, {"lead", 2},
		{"offsite_conversion.custom.555", 4}, {"link_click", 40}, {"landing_page_view", 31}, {"video_view", 90}, {"post_engagement", 70}}
	vals := []metaAction{{"purchase", 147}, {"omni_purchase", 147}}
	reach := flexInt(500)
	conv, value, x := metaResults(acts, vals, &reach)
	if conv != 5 || value != 147 {
		t.Fatalf("conv %v value %v", conv, value)
	}
	if *x.LinkClicks != 40 || *x.LandingPageViews != 31 || *x.VideoViews != 90 || *x.Reach != 500 {
		t.Fatalf("extra %+v", x)
	}
	got := map[string]float64{}
	for _, a := range x.Actions {
		got[a.Name] = a.Conversions
	}
	if len(got) != 3 || got["purchase"] != 3 || got["lead"] != 2 || got["custom.555"] != 4 || *x.Actions[0].ValueMicros != 147_000_000 {
		t.Fatalf("actions %+v", x.Actions)
	}
}

// A Google ad inherits its final URL suffix from its ad group, campaign or
// account, the most specific that is set; an ad without a link says
// nothing.
func TestGoogleAdTemplate(t *testing.T) {
	cl, _, _ := fake(t, map[string]any{
		"POST /v23/customers/111/googleAds:search": map[string]any{"results": []any{
			map[string]any{"adGroup": map[string]any{"id": "71"}, "campaign": map[string]any{"finalUrlSuffix": "of_campaign={campaignid}"},
				"customer": map[string]any{"finalUrlSuffix": "utm_source=google"}, "adGroupAd": map[string]any{"ad": map[string]any{"id": "81", "finalUrls": []string{"https://acme.example/"}}}},
			map[string]any{"adGroup": map[string]any{"id": "71"}, "customer": map[string]any{"finalUrlSuffix": "utm_source=google"},
				"adGroupAd": map[string]any{"ad": map[string]any{"id": "82", "finalUrls": []string{"https://acme.example/"}}}},
			map[string]any{"adGroup": map[string]any{"id": "71"}, "adGroupAd": map[string]any{"ad": map[string]any{"id": "83"}}},
		}},
	})
	ads, err := GoogleAds{}.AdItems(ctx, cl, c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "111"}, c.AdLevelAd)
	if err != nil || len(ads) != 3 {
		t.Fatalf("ads %+v %v", ads, err)
	}
	if ads[0].Details[c.AdDetailTemplated] != "true" || ads[0].Details[c.AdDetailURLTags] != "of_campaign={campaignid}" ||
		ads[1].Details[c.AdDetailTemplated] != "false" || ads[2].Details[c.AdDetailTemplated] != "" {
		t.Fatalf("templated: %v / %v / %v", ads[0].Details, ads[1].Details, ads[2].Details)
	}
}

// A conversion whose visit kept only the Google tag's _gcl_aw cookie is
// sent with the cookie's gclid, and Gmail addresses are hashed without
// their dots, as Google asks.
func TestGoogleAdsConversionsCookieAndGmail(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /v23/customers/111:uploadClickConversions": map[string]any{},
	})
	g, creds, acct := GoogleAds{}, c.Creds{"access_token": "tok", "developer_token": "dev"}, c.AdAccount{ID: "111"}
	cv := c.AdConversion{EventID: "msg-1", Time: time.Now(), ClickIDs: map[string]string{"gcl_aw": "GCL.1700000000.Cj0KCQ-test"},
		Emails: []string{c.HashID("ana.lee@gmail.com")}, GoogleEmails: []string{c.HashGoogleEmail("Ana.Lee@Gmail.com")}}
	bad := c.AdConversion{EventID: "msg-2", Time: time.Now(), Index: 1, ClickIDs: map[string]string{"gcl_aw": "nonsense"}}
	res, err := g.SendConversions(ctx, cl, creds, acct, c.ConversionTarget{ID: "42"}, []c.AdConversion{cv, bad})
	if err != nil || !res[0].OK || !res[1].Skipped {
		t.Fatalf("send %+v %v", res, err)
	}
	body := (*reqs)[len(*reqs)-1].body
	if !strings.Contains(body, `"gclid":"Cj0KCQ-test"`) || !strings.Contains(body, c.HashID("analee@gmail.com")) || strings.Contains(body, c.HashID("ana.lee@gmail.com")) {
		t.Fatalf("upload body %s", body)
	}
	if c.HashGoogleEmail("ana.lee@acme.example") != c.HashID("ana.lee@acme.example") {
		t.Fatal("only Gmail addresses lose their dots")
	}
}

func TestXAdsManage(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /12/accounts/18ce5/campaigns/k1": map[string]any{"data": map[string]any{"id": "k1", "name": "Launch", "entity_status": "ACTIVE", "daily_budget_amount_local_micro": 50_000_000}},
		"PUT /12/accounts/18ce5/campaigns/k1": map[string]any{"data": map[string]any{"id": "k1"}},
	})
	creds := c.Creds{"consumer_key": "ck", "consumer_secret": "cs", "access_token": "at", "access_token_secret": "ats"}
	x, acct := XAds{}, c.AdAccount{ID: "18ce5"}
	var _ c.AdsManager = x
	camp, err := x.AdCampaign(ctx, cl, creds, acct, "k1")
	if err != nil || camp.Status != c.AdActive || camp.DailyBudgetMicros == nil || *camp.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("campaign %+v %v", camp, err)
	}
	if err := x.SetCampaignStatus(ctx, cl, creds, acct, "k1", false); err != nil {
		t.Fatal(err)
	}
	last := (*reqs)[len(*reqs)-1]
	if last.method != "PUT" || last.query != "entity_status=PAUSED" || !strings.HasPrefix(last.header.Get("Authorization"), "OAuth ") {
		t.Fatalf("pause %+v", last)
	}
	if err := x.SetCampaignDailyBudget(ctx, cl, creds, acct, "k1", 12_345_678); err != nil {
		t.Fatal(err)
	}
	if last = (*reqs)[len(*reqs)-1]; last.query != "daily_budget_amount_local_micro=12350000" {
		t.Fatalf("budget %s", last.query)
	}
	for _, bad := range []string{"", "K1", "k1/../x", strings.Repeat("a", 40)} {
		if err := x.SetCampaignStatus(ctx, cl, creds, acct, bad, true); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("id %q: %v", bad, err)
		}
	}
	if err := x.SetCampaignDailyBudget(ctx, cl, creds, acct, "k1", 0); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("zero budget: %v", err)
	}
	if _, err := x.AdCampaign(ctx, cl, creds, acct, "k2"); err == nil {
		t.Fatal("a missing campaign must fail")
	}
}

func TestXAdsConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{"POST /12/measurement/conversions/o8z6j": map[string]any{"data": map[string]any{"conversions_processed": 1}}})
	x, acct := XAds{}, c.AdAccount{ID: "18ce5"}
	var _ c.ConversionSender = x
	creds := c.Creds{"consumer_key": "ck", "consumer_secret": "cs", "access_token": "at", "access_token_secret": "ats"}
	if got := c.MissingSetup(x, creds); len(got) != 0 {
		t.Fatalf("pixel and events are optional: %+v", got)
	}
	if _, err := x.ConversionTargets(ctx, cl, creds, acct); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("without a pixel: %v", err)
	}
	creds["pixel_id"] = "o8z6j"
	creds["conversion_events"] = "Sign-up = tw-o8z6j-o8z2x\nPurchase=o8z2y, o8z2y\nnot an event"
	ts, err := x.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 2 || ts[0].Name != "Sign-up" || ts[0].ID != "o8z2x" || ts[1].ID != "o8z2y" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	none := conv(1, nil)
	none.Emails, none.Phones = nil, nil
	res, err := x.SendConversions(ctx, cl, creds, acct, ts[1], []c.AdConversion{conv(0, map[string]string{"twclid": "tw1"}), none})
	if err != nil || !res[0].OK || !res[1].Skipped {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	for _, want := range []string{`"event_id":"o8z2y"`, `"twclid":"tw1"`, `"hashed_email":"` + c.HashID("ana@acme.example") + `"`,
		`"conversion_id":"msg-1"`, `"conversion_time":"2026-10-01T09:30:00.000Z"`, `"value":"12.50"`, `"price_currency":"USD"`} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("body %s lacks %s", last.body, want)
		}
	}
	if !strings.HasPrefix(last.header.Get("Authorization"), "OAuth ") || last.header.Get("Content-Type") != "application/json" {
		t.Fatalf("headers %v", last.header)
	}
	// With Events Manager's token, the call carries it instead of a signature.
	creds["conversions_token"] = "test-pixel-token-not-real"
	if _, err := x.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{conv(0, map[string]string{"twclid": "tw1"})}); err != nil {
		t.Fatal(err)
	}
	last = (*reqs)[len(*reqs)-1]
	if last.header.Get("X-Pixel-Token") != "test-pixel-token-not-real" || last.header.Get("Authorization") != "" {
		t.Fatalf("pixel token headers %v", last.header)
	}
}

func TestXAdsPlatformConversions(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /12/accounts/18ce5/campaigns": map[string]any{"data": []any{map[string]any{"id": "k1", "name": "Launch", "entity_status": "ACTIVE"}}},
		"GET /12/stats/accounts/18ce5": map[string]any{"data": []any{map[string]any{"id": "k1", "id_data": []any{map[string]any{"metrics": map[string]any{
			"billed_charge_local_micro": []any{1_000_000}, "impressions": []any{100}, "clicks": []any{3},
			"conversion_sign_ups":  map[string]any{"post_engagement": []any{2}, "post_view": []any{1}},
			"conversion_purchases": map[string]any{"metric": []any{1}, "post_engagement": []any{9}, "sale_amount": []any{40_000_000}}}}}},
			map[string]any{"id": "k2", "id_data": []any{map[string]any{"metrics": map[string]any{
				"billed_charge_local_micro": []any{500_000}, "conversion_purchases": []any{2}}}}}}},
	})
	creds := c.Creds{"consumer_key": "ck", "consumer_secret": "cs", "access_token": "at", "access_token_secret": "ats"}
	days, err := XAds{}.AdDays(ctx, cl, creds, c.AdAccount{ID: "18ce5"}, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 2 || days[0].Conversions == nil || *days[0].Conversions != 4 || len(days[0].Actions) != 2 ||
		days[0].ConversionValueMicros == nil || *days[0].ConversionValueMicros != 40_000_000 || *days[1].Conversions != 2 {
		t.Fatalf("days %+v %v", days, err)
	}
	if last := (*reqs)[len(*reqs)-1]; !strings.Contains(last.query, "WEB_CONVERSION") {
		t.Fatalf("stats query %s", last.query)
	}
}

func TestRedditAdsManage(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /campaigns/c1":   map[string]any{"data": map[string]any{"id": "c1", "ad_account_id": "a1", "name": "CBO", "configured_status": "ACTIVE", "effective_status": "ACTIVE", "goal_type": "DAILY_SPEND", "goal_value": 20_000_000}},
		"GET /campaigns/c2":   map[string]any{"data": map[string]any{"id": "c2", "ad_account_id": "a1", "name": "Ad group budgets", "configured_status": "ACTIVE"}},
		"GET /campaigns/c3":   map[string]any{"data": map[string]any{"id": "c3", "ad_account_id": "a9", "name": "Someone else's"}},
		"GET /campaigns/c4":   map[string]any{"data": map[string]any{"id": "c4", "ad_account_id": "a1", "name": "Lifetime budgets", "configured_status": "ACTIVE"}},
		"PATCH /campaigns/c1": map[string]any{"data": map[string]any{"id": "c1"}},
		"GET /ad_accounts/a1/ad_groups": map[string]any{"data": []any{
			map[string]any{"id": "g1", "campaign_id": "c2", "configured_status": "ACTIVE", "goal_type": "DAILY_SPEND", "goal_value": 10_000_000},
			map[string]any{"id": "g2", "campaign_id": "c2", "configured_status": "PAUSED", "goal_type": "DAILY_SPEND", "goal_value": 30_000_000},
			map[string]any{"id": "g3", "campaign_id": "c2", "configured_status": "ARCHIVED", "goal_type": "DAILY_SPEND", "goal_value": 99_000_000},
			map[string]any{"id": "g4", "campaign_id": "c4", "configured_status": "ACTIVE", "goal_type": "LIFETIME_SPEND", "goal_value": 500_000_000},
		}},
		"PATCH /ad_groups/g1": map[string]any{"data": map[string]any{"id": "g1"}},
		"PATCH /ad_groups/g2": map[string]any{"data": map[string]any{"id": "g2"}},
	})
	r, acct, creds := RedditAds{}, c.AdAccount{ID: "a1"}, c.Creds{"access_token": "tok"}
	var _ c.AdsManager = r
	camp, err := r.AdCampaign(ctx, cl, creds, acct, "c1")
	if err != nil || camp.DailyBudgetMicros == nil || *camp.DailyBudgetMicros != 20_000_000 {
		t.Fatalf("campaign %+v %v", camp, err)
	}
	if err := r.SetCampaignStatus(ctx, cl, creds, acct, "c1", false); err != nil {
		t.Fatal(err)
	}
	last := (*reqs)[len(*reqs)-1]
	if last.method != "PATCH" || last.body != `{"data":{"configured_status":"PAUSED"}}` {
		t.Fatalf("pause %s %s", last.method, last.body)
	}
	if err := r.SetCampaignDailyBudget(ctx, cl, creds, acct, "c1", 25_004_999); err != nil {
		t.Fatal(err)
	}
	if last = (*reqs)[len(*reqs)-1]; last.body != `{"data":{"goal_type":"DAILY_SPEND","goal_value":25000000}}` {
		t.Fatalf("budget %s", last.body)
	}
	// Budgets on ad groups: the campaign shows their sum, and a new
	// budget is shared in the same proportions; archived ones are left.
	camp2, err := r.AdCampaign(ctx, cl, creds, acct, "c2")
	if err != nil || camp2.DailyBudgetMicros == nil || *camp2.DailyBudgetMicros != 40_000_000 {
		t.Fatalf("ad group budgets sum %+v %v", camp2, err)
	}
	n := len(*reqs)
	if err := r.SetCampaignDailyBudget(ctx, cl, creds, acct, "c2", 5_000_000); err != nil {
		t.Fatal(err)
	}
	var patches []string
	for _, q := range (*reqs)[n:] {
		if q.method == "PATCH" {
			patches = append(patches, q.path+" "+q.body)
		}
	}
	if strings.Join(patches, "|") != `/ad_groups/g1 {"data":{"goal_value":1250000}}|/ad_groups/g2 {"data":{"goal_value":3750000}}` {
		t.Fatalf("ad group patches %v", patches)
	}
	n = len(*reqs)
	if err := r.SetCampaignDailyBudget(ctx, cl, creds, acct, "c4", 5_000_000); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "lifetime") {
		t.Fatalf("lifetime budgets: %v", err)
	}
	if err := r.SetCampaignStatus(ctx, cl, creds, acct, "c3", false); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("another account's campaign: %v", err)
	}
	for _, q := range (*reqs)[n:] {
		if q.method == "PATCH" {
			t.Fatalf("refused changes must not write: %+v", q)
		}
	}
	if err := r.SetCampaignStatus(ctx, cl, creds, acct, "c1/../x", true); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad id: %v", err)
	}
	if !slicesContain(r.Definition().OAuth.Scopes, "adsedit") {
		t.Fatal("managing needs the adsedit scope")
	}
}

func slicesContain(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func TestRedditAdsConversionsV3(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"POST /pixels/a2_px1/conversion_events": map[string]any{"data": map[string]any{}},
		"POST /ad_accounts/a1/reports":          map[string]any{"data": map[string]any{"metrics": []any{map[string]any{"campaign_id": "c1", "date": "2026-10-01T00:00:00Z", "spend": 1_000_000, "key_conversion_total_count": 4}}}},
	})
	r, acct := RedditAds{}, c.AdAccount{ID: "a1"}
	creds := c.Creds{"access_token": "tok", "conversions_token": "ct", "pixel_id": "a2_px1"}
	ts, err := r.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(ts) != 1 || ts[0].ID != "a2_px1" {
		t.Fatalf("targets %+v %v", ts, err)
	}
	cv := conv(0, map[string]string{"rdt_cid": "r1"})
	cv.EventType = "Purchase"
	res, err := r.SendConversions(ctx, cl, creds, acct, ts[0], []c.AdConversion{cv})
	if err != nil || !res[0].OK {
		t.Fatalf("send %+v %v", res, err)
	}
	last := (*reqs)[len(*reqs)-1]
	for _, want := range []string{`{"data":{"events":[`, `"click_id":"r1"`, `"tracking_type":"PURCHASE"`, `"action_source":"WEBSITE"`,
		`"event_at":1790847000000`, `"conversion_id":"msg-1"`, `"value":12.5`} {
		if !strings.Contains(last.body, want) {
			t.Fatalf("body %s lacks %s", last.body, want)
		}
	}
	if last.header.Get("Authorization") != "Bearer ct" || strings.Contains(last.body, "test_mode") {
		t.Fatalf("v3 %s %v", last.body, last.header)
	}
	days, err := r.AdDays(ctx, cl, creds, acct, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].Conversions == nil || *days[0].Conversions != 4 {
		t.Fatalf("days %+v %v", days, err)
	}
}

func TestXAdsSignIn(t *testing.T) {
	cl, reqs, base := fake(t, map[string]any{
		"POST /oauth/request_token": []byte("oauth_token=rt1&oauth_token_secret=rs1&oauth_callback_confirmed=true"),
		"POST /oauth/access_token":  []byte("oauth_token=at1&oauth_token_secret=as1&user_id=42&screen_name=northpeak"),
		"GET /12/accounts":          map[string]any{"data": []map[string]any{{"id": "18ce5", "name": "Northpeak", "currency": "USD", "timezone": "UTC"}}},
	})
	x := XAds{}
	var _ c.AccountSignInOAuth1 = x
	app := c.Creds{"consumer_key": "ck", "consumer_secret": "cs"}
	if acct, err := x.Test(ctx, cl, app); err != nil || acct.Details["sign_in"] != "needed" {
		t.Fatalf("app keys alone: %+v %v", acct, err)
	}
	if _, err := x.AdAccounts(ctx, cl, app); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no sign-in must ask for one: %v", err)
	}
	if _, _, _, err := x.SignInRequest(ctx, cl, c.Creds{}, "https://app.example/cb"); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no app keys: %v", err)
	}
	link, tok, sec, err := x.SignInRequest(ctx, cl, app, "https://app.example/cb?state=s1")
	if err != nil || tok != "rt1" || sec != "rs1" || link != base+"/oauth/authorize?oauth_token=rt1" {
		t.Fatalf("request %q %q %q %v", link, tok, sec, err)
	}
	h := (*reqs)[len(*reqs)-1].header.Get("Authorization")
	if !strings.Contains(h, `oauth_callback="https%3A%2F%2Fapp.example%2Fcb%3Fstate%3Ds1"`) || strings.Contains(h, "oauth_token=") {
		t.Fatalf("request header %s", h)
	}
	pending := c.Creds{"consumer_key": "ck", "consumer_secret": "cs", c.OAuth1RequestToken: "rt1", c.OAuth1RequestSecret: "rs1"}
	creds, acct, err := x.SignInAccess(ctx, cl, pending, "rt1", "rs1", "v1")
	if err != nil || acct.ID != "42" || acct.Name != "@northpeak" {
		t.Fatalf("access %+v %v", acct, err)
	}
	if creds["access_token"] != "at1" || creds["access_token_secret"] != "as1" || creds["consumer_key"] != "ck" || creds[c.OAuth1RequestToken] != "" || creds[c.OAuth1RequestSecret] != "" {
		t.Fatalf("creds %v", creds)
	}
	var access seen
	for _, r := range *reqs {
		if r.path == "/oauth/access_token" {
			access = r
		}
	}
	if h := access.header.Get("Authorization"); !strings.Contains(h, `oauth_verifier="v1"`) || !strings.Contains(h, `oauth_token="rt1"`) {
		t.Fatalf("access header %s", h)
	}
	if _, _, err := x.SignInAccess(ctx, cl, pending, "rt1", "rs1", ""); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no verifier: %v", err)
	}
}

func TestXAdsSignInRefused(t *testing.T) {
	cl, _, _ := fake(t, map[string]any{
		"POST /oauth/request_token": []byte("oauth_token=rt1&oauth_token_secret=rs1&oauth_callback_confirmed=false"),
	})
	if _, _, _, err := (XAds{}).SignInRequest(ctx, cl, c.Creds{"consumer_key": "ck", "consumer_secret": "cs"}, "https://app.example/cb"); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("unconfirmed callback: %v", err)
	}
}
