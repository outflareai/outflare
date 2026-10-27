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
	for _, id := range []string{"google_ads", "meta_ads", "microsoft_ads.customer", "microsoft_ads.campaign", "microsoft_ads.reporting",
		"reddit_ads", "x_ads", "linkedin_ads"} {
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
	if len(ids) != 6 {
		t.Fatalf("want six ad platforms, got %d", len(ids))
	}
}

func TestMissingSetup(t *testing.T) {
	if got := c.MissingSetup(GoogleAds{}, c.Creds{"access_token": "t"}); len(got) != 1 || got[0].Name != "developer_token" {
		t.Fatalf("google ads without a developer token: %+v", got)
	}
	if got := c.MissingSetup(GoogleAds{}, c.Creds{"developer_token": "d"}); len(got) != 0 {
		t.Fatalf("with the token: %+v", got)
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
				return map[string]any{"results": []any{map[string]any{"campaign": map[string]any{"id": "7", "name": "Brand search", "status": "ENABLED", "advertisingChannelType": "SEARCH"},
					"campaignBudget": map[string]any{"amountMicros": "50000000"}}}}
			}
		},
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
	for _, r := range *reqs {
		if r.header.Get("developer-token") != "dev" {
			t.Fatalf("%s %s without the developer token", r.method, r.path)
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
	if _, err := g.AdAccounts(ctx, cl, c.Creds{"access_token": "tok"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("without a developer token: %v", err)
	}
}

func TestMetaAds(t *testing.T) {
	var base string
	cl, _, u := fake(t, map[string]any{
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
		"GET /adAccountUsers":             map[string]any{"elements": []any{map[string]any{"account": "urn:li:sponsoredAccount:501"}}},
		"GET /adAccounts/501":             map[string]any{"id": 501, "name": "Northpeak", "currency": "USD", "status": "ACTIVE"},
		"GET /adAccounts/501/adCampaigns": map[string]any{"elements": []any{map[string]any{"id": 61, "name": "ABM", "status": "ACTIVE", "objectiveType": "LEAD_GENERATION", "dailyBudget": map[string]any{"amount": "100.00"}}}},
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
}
