package ads

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// Fake values only: no real Amazon token, client id or account.
var amazonCreds = c.Creds{"access_token": "Atza|test-not-a-real-token", "client_id": "amzn1.application-oa2-client.test-not-real"}

func gzipJSON(t *testing.T, v any) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	_ = json.NewEncoder(zw).Encode(v)
	_ = zw.Close()
	return b.Bytes()
}

// amazonRegionsFake points Europe at its own fake, and the Far East at one
// that refuses the sign-in, as Amazon does where the account has nothing.
func amazonRegionsFake(t *testing.T, cl *c.Client, eu []any) {
	t.Helper()
	euSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/profiles" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(eu)
	}))
	feSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	t.Cleanup(euSrv.Close)
	t.Cleanup(feSrv.Close)
	cl.BaseURLs["amazon_ads.eu"] = euSrv.URL
	cl.BaseURLs["amazon_ads.fe"] = feSrv.URL
}

func TestAmazonAds(t *testing.T) {
	old := amazonPoll
	amazonPoll.every, amazonPoll.limit = time.Millisecond, time.Second
	t.Cleanup(func() { amazonPoll = old })

	report := gzipJSON(t, []any{
		map[string]any{"date": "2026-10-01", "campaignId": 52978233888897, "campaignName": "Trail shoes", "impressions": 1200, "clicks": 31, "cost": 18.42, "purchases7d": 3, "sales7d": 89.97},
		map[string]any{"date": "2026-10-02", "campaignId": 52978233888897, "campaignName": "Trail shoes", "impressions": 0, "clicks": 0, "cost": 0},
	})
	polls, created := 0, 0
	var srvURL string
	cl, reqs, u := fake(t, map[string]any{
		"GET /v2/profiles": []any{map[string]any{"profileId": 3311001, "countryCode": "US", "currencyCode": "USD", "timezone": "America/Los_Angeles",
			"accountInfo": map[string]any{"marketplaceStringId": "ATVPDKIKX0DER", "id": "A1TESTSELLER", "type": "seller", "name": "Northpeak"}}},
		"POST /sp/campaigns/list": func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if b["nextToken"] == nil {
				return map[string]any{"campaigns": []any{map[string]any{"campaignId": "52978233888897", "name": "Trail shoes", "state": "ENABLED", "targetingType": "AUTO",
					"budget": map[string]any{"budget": 15.5, "budgetType": "DAILY"}}}, "nextToken": "page2"}
			}
			return map[string]any{"campaigns": []any{map[string]any{"campaignId": "276748589410145", "name": "Old", "state": "ARCHIVED", "budget": map[string]any{"budget": 100, "budgetType": "DAILY"}}}}
		},
		"POST /reporting/reports": func(r *http.Request) any {
			created++
			return map[string]any{"reportId": "06ba6494-8568-4066-949f-abb6a91c0b9c", "status": "PENDING"}
		},
		"GET /reporting/reports/06ba6494-8568-4066-949f-abb6a91c0b9c": func(r *http.Request) any {
			polls++
			if polls == 1 {
				return map[string]any{"reportId": "06ba6494-8568-4066-949f-abb6a91c0b9c", "status": "PROCESSING", "url": nil}
			}
			return map[string]any{"reportId": "06ba6494-8568-4066-949f-abb6a91c0b9c", "status": "COMPLETED", "url": srvURL + "/download/report.json.gz"}
		},
		"GET /download/report.json.gz": report,
	})
	srvURL = u
	amazonRegionsFake(t, cl, []any{map[string]any{"profileId": "4422002", "countryCode": "DE", "currencyCode": "EUR", "timezone": "Europe/Berlin",
		"accountInfo": map[string]any{"marketplaceStringId": "A1PA6795UKMFR9", "id": "A2TESTVENDOR", "type": "vendor", "name": "Acme"}}})
	am := AmazonAds{}

	acct0, err := am.Test(ctx, cl, amazonCreds)
	if err != nil || acct0.Details["accounts"] != "2" {
		t.Fatalf("test %+v %v", acct0, err)
	}
	accts, err := am.AdAccounts(ctx, cl, amazonCreds)
	if err != nil || len(accts) != 2 {
		t.Fatalf("profiles %+v %v", accts, err)
	}
	de, us := accts[0], accts[1]
	if de.ID != "4422002" || de.Currency != "EUR" || de.Meta["region"] != "EU" || us.ID != "3311001" || us.Currency != "USD" ||
		us.TimeZone != "America/Los_Angeles" || us.Meta["region"] != "NA" || us.Name != "Northpeak (US)" {
		t.Fatalf("profiles %+v", accts)
	}
	first := (*reqs)[0]
	if first.header.Get("Authorization") != "Bearer Atza|test-not-a-real-token" || first.header.Get("Amazon-Advertising-API-ClientId") != "amzn1.application-oa2-client.test-not-real" ||
		first.header.Get("Amazon-Advertising-API-Scope") != "" {
		t.Fatalf("profile headers %v", first.header)
	}

	n := len(*reqs)
	camps, err := am.AdCampaigns(ctx, cl, amazonCreds, us)
	if err != nil || len(camps) != 2 || camps[0].ID != "52978233888897" || camps[0].Status != c.AdActive || *camps[0].DailyBudgetMicros != 15_500_000 ||
		camps[1].Status != c.AdRemoved {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	list := (*reqs)[n]
	if list.header.Get("Amazon-Advertising-API-Scope") != "3311001" || list.header.Get("Content-Type") != "application/vnd.spCampaign.v3+json" ||
		list.header.Get("Accept") != "application/vnd.spCampaign.v3+json" || !strings.Contains(list.body, `"ARCHIVED"`) {
		t.Fatalf("campaign list call %+v", list)
	}
	if !strings.Contains((*reqs)[n+1].body, `"nextToken":"page2"`) {
		t.Fatalf("second page %+v", (*reqs)[n+1])
	}

	n = len(*reqs)
	days, err := am.AdDays(ctx, cl, amazonCreds, us, "2026-10-01", "2026-10-02")
	if err != nil || len(days) != 1 {
		t.Fatalf("days %+v %v", days, err)
	}
	d := days[0]
	if d.CampaignID != "52978233888897" || d.CampaignName != "Trail shoes" || d.Date != "2026-10-01" || d.SpendMicros != 18_420_000 ||
		d.Impressions != 1200 || d.Clicks != 31 || *d.Conversions != 3 || *d.ConversionValueMicros != 89_970_000 {
		t.Fatalf("day %+v", d)
	}
	create := (*reqs)[n]
	if create.method != "POST" || create.header.Get("Content-Type") != "application/vnd.createasyncreportrequest.v3+json" || create.header.Get("Amazon-Advertising-API-Scope") != "3311001" {
		t.Fatalf("create %+v", create)
	}
	for _, want := range []string{`"reportTypeId":"spCampaigns"`, `"timeUnit":"DAILY"`, `"format":"GZIP_JSON"`, `"adProduct":"SPONSORED_PRODUCTS"`, `"startDate":"2026-10-01"`, `"endDate":"2026-10-02"`, `"purchases7d"`, `"sales7d"`, `"cost"`} {
		if !strings.Contains(create.body, want) {
			t.Fatalf("report request lacks %s: %s", want, create.body)
		}
	}
	// The download is a signed storage address: no Amazon Ads headers go there.
	dl := (*reqs)[len(*reqs)-1]
	if dl.path != "/download/report.json.gz" || dl.header.Get("Authorization") != "" || dl.header.Get("Amazon-Advertising-API-ClientId") != "" {
		t.Fatalf("download %+v", dl)
	}
	if polls != 2 {
		t.Fatalf("want two polls, got %d", polls)
	}

	// 90 days are three reports of at most 31 days, all asked for first.
	created, polls = 0, 1
	if _, err := am.AdDays(ctx, cl, amazonCreds, us, "2026-07-04", "2026-10-01"); err != nil || created != 3 {
		t.Fatalf("windows: %d reports, %v", created, err)
	}
}

func TestAmazonAdsReportStates(t *testing.T) {
	old := amazonPoll
	amazonPoll.every, amazonPoll.limit = time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { amazonPoll = old })
	const dup = "b09d780d-44d8-425c-a3fc-c6aa51e91b39"
	state := "FAILED"
	cl, reqs, _ := fake(t, map[string]any{
		"POST /reporting/reports": func(r *http.Request) any {
			return map[string]any{"reportId": dup, "status": "PENDING"}
		},
		"GET /reporting/reports/" + dup: func(r *http.Request) any {
			return map[string]any{"reportId": dup, "status": state, "failureReason": "test failure"}
		},
	})
	am := AmazonAds{}
	acct := c.AdAccount{ID: "3311001", Meta: map[string]string{"region": "NA"}}
	if _, err := am.AdDays(ctx, cl, amazonCreds, acct, "2026-10-01", "2026-10-01"); c.KindOf(err) != c.ErrUnavailable || !strings.Contains(err.Error(), "test failure") {
		t.Fatalf("failed report: %v", err)
	}
	state = "PENDING"
	if _, err := am.AdDays(ctx, cl, amazonCreds, acct, "2026-10-01", "2026-10-01"); c.KindOf(err) != c.ErrUnavailable || !strings.Contains(err.Error(), "next sync") {
		t.Fatalf("slow report: %v", err)
	}
	if len(*reqs) < 3 {
		t.Fatalf("want polling, got %d calls", len(*reqs))
	}

	// A duplicate request answers 425 with the report already being built.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/reporting/reports":
			w.WriteHeader(425)
			w.Write([]byte(`{"code":"425","detail":"The Request is a duplicate of : ` + dup + `"}`))
		case r.URL.Path == "/reporting/reports/"+dup:
			json.NewEncoder(w).Encode(map[string]any{"reportId": dup, "status": "COMPLETED", "url": "http://" + r.Host + "/dl"})
		case r.URL.Path == "/dl":
			w.Write(gzipJSON(t, []any{map[string]any{"date": "2026-10-01", "campaignId": "7", "impressions": 10, "clicks": 1, "cost": "0.55"}}))
		}
	}))
	defer srv.Close()
	cl2 := &c.Client{BaseURLs: map[string]string{"amazon_ads.na": srv.URL}}
	days, err := am.AdDays(ctx, cl2, amazonCreds, acct, "2026-10-01", "2026-10-01")
	if err != nil || len(days) != 1 || days[0].SpendMicros != 550_000 || days[0].Conversions != nil {
		t.Fatalf("duplicate report %+v %v", days, err)
	}

	// Download addresses must be https.
	if _, err := am.download(ctx, cl2, "http://example.com/report.json.gz"); err == nil {
		t.Fatal("took a plain http report address")
	}
}

func TestAmazonAdsRefusals(t *testing.T) {
	cl, _, _ := fake(t, map[string]any{"GET /v2/profiles": []any{}})
	amazonRegionsFake(t, cl, []any{})
	am := AmazonAds{}
	if _, err := am.AdAccounts(ctx, cl, c.Creds{"access_token": "tok"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no client id: %v", err)
	}
	if _, err := am.Test(ctx, cl, amazonCreds); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("no profiles: %v", err)
	}
	// Every region refusing the token is an auth error.
	bad := c.Creds{"access_token": "bad", "client_id": "amzn1.application-oa2-client.test-not-real"}
	cl.BaseURLs["amazon_ads.eu"] = cl.BaseURLs["amazon_ads.na"]
	if _, err := am.AdAccounts(ctx, cl, bad); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad token: %v", err)
	}
	// A region that is down fails the read rather than dropping its profiles.
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer down.Close()
	cl.BaseURLs["amazon_ads.eu"] = down.URL
	if _, err := am.AdAccounts(ctx, cl, amazonCreds); c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("region down: %v", err)
	}
}

// The sign-in keeps the app's client id beside the workspace's tokens, and
// a refresh renews both with Login with Amazon's refresh_token grant.
func TestAmazonAdsSignIn(t *testing.T) {
	var forms []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		forms = append(forms, r.PostForm)
		json.NewEncoder(w).Encode(map[string]any{"access_token": "Atza|test-new", "refresh_token": "Atzr|test-refresh", "token_type": "bearer", "expires_in": 3600})
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"amazon_ads.token": srv.URL}}
	d := AmazonAds{}.Definition()
	app := c.OAuthApp{ClientID: "amzn1.application-oa2-client.test-not-real", ClientSecret: "test-not-a-real-secret"}
	if auth := c.AuthURL(d, app, "https://outflare.example/cb", "st", "verifier"); !strings.HasPrefix(auth, "https://www.amazon.com/ap/oa?") ||
		!strings.Contains(auth, "scope=advertising%3A%3Acampaign_management") {
		t.Fatalf("consent screen %s", auth)
	}
	creds, err := c.Exchange(ctx, cl, d, app, "code1", "https://outflare.example/cb", "verifier")
	if err != nil || creds["client_id"] != app.ClientID || creds["refresh_token"] != "Atzr|test-refresh" {
		t.Fatalf("exchange %+v %v", creds, err)
	}
	creds["expires_at"] = "1" // long expired
	creds["client_id"] = "stale"
	fresh, changed, err := c.Refresh(ctx, cl, d, app, creds)
	if err != nil || !changed || fresh["access_token"] != "Atza|test-new" || fresh["client_id"] != app.ClientID {
		t.Fatalf("refresh %+v %v", fresh, err)
	}
	f := forms[len(forms)-1]
	if f.Get("grant_type") != "refresh_token" || f.Get("refresh_token") != "Atzr|test-refresh" || f.Get("client_id") != app.ClientID || f.Get("client_secret") != app.ClientSecret {
		t.Fatalf("refresh form %v", f)
	}
}
