package ads

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestOutbrainAds(t *testing.T) {
	logins := 0
	cl, reqs, _ := fake(t, map[string]any{
		"GET /login": func(r *http.Request) any {
			logins++
			if r.Header.Get("Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("ads@northpeak.example:pw-test-not-real")) {
				return map[string]any{}
			}
			return map[string]any{"OB-TOKEN-V1": "ob_test_not_a_real_token"}
		},
		"GET /marketers": map[string]any{"marketers": []any{
			map[string]any{"id": "m1", "name": "Northpeak", "enabled": true},
			map[string]any{"id": "m2", "name": "Acme", "enabled": false},
		}, "count": 2},
		"GET /marketers/m1/budgets": map[string]any{"budgets": []any{map[string]any{"id": "b1", "currency": "eur"}}},
		"GET /marketers/m2/budgets": map[string]any{"budgets": []any{}},
		"GET /marketers/m1/campaigns": func(r *http.Request) any {
			if r.URL.Query().Get("offset") == "0" {
				page := []any{map[string]any{"id": "c1", "name": "Launch", "enabled": true, "objective": "Conversions",
					"budget": map[string]any{"type": "DAILY", "amount": 25.5, "currency": "EUR"}}}
				for i := 1; i < 50; i++ {
					page = append(page, map[string]any{"id": "f" + strconv.Itoa(i), "name": "Filler", "enabled": false,
						"budget": map[string]any{"type": "MONTHLY", "pacing": "DAILY_TARGET", "amount": 900, "dailyTarget": 30}})
				}
				return map[string]any{"campaigns": page, "totalCount": 51}
			}
			return map[string]any{"campaigns": []any{map[string]any{"id": "c2", "name": "Old", "enabled": true, "autoArchived": true}}, "totalCount": 51}
		},
		"GET /reports/marketers/m1/campaigns/periodic": func(r *http.Request) any {
			if r.URL.Query().Get("from") != "2026-09-01" {
				return map[string]any{"campaignResults": []any{}, "totalCampaigns": 0}
			}
			return map[string]any{"campaignResults": []any{map[string]any{"campaignId": "c1", "results": []any{
				map[string]any{"metadata": map[string]any{"id": "2026-09-02", "fromDate": "2026-09-02", "toDate": "2026-09-02"},
					"metrics": map[string]any{"impressions": 93072, "clicks": 101, "spend": 85.96, "conversions": 2, "totalConversions": 3, "sumValue": 97.98}},
				map[string]any{"metadata": map[string]any{"id": "2026-09-01", "fromDate": "2026-09-01", "toDate": "2026-09-01"},
					"metrics": map[string]any{"impressions": 0, "clicks": 0, "spend": 0}},
			}, "totalResults": 2}}, "totalCampaigns": 1}
		},
	})
	ob := OutbrainAds{}
	login := c.Creds{"username": "ads@northpeak.example", "password": "pw-test-not-real"}

	// Testing a new connection logs in once and reads with that token.
	acct0, err := ob.Test(ctx, cl, login)
	if err != nil || acct0.Details["accounts"] != "2" || logins != 1 {
		t.Fatalf("test %+v %v (logins %d)", acct0, err, logins)
	}
	if h := (*reqs)[1].header; h.Get("OB-TOKEN-V1") != "ob_test_not_a_real_token" || h.Get("Authorization") != "" {
		t.Fatalf("marketers call headers %v", h)
	}

	// The token is stored with the credentials and renewed only when old.
	if !ob.RefreshDue(login) {
		t.Fatal("no token yet: refresh is due")
	}
	creds, err := ob.RefreshCreds(ctx, cl, login)
	if err != nil || creds["token"] != "ob_test_not_a_real_token" || creds["username"] != "ads@northpeak.example" || ob.RefreshDue(creds) {
		t.Fatalf("refresh %v %v", creds, err)
	}
	old := c.Creds{"token": "t", "token_at": strconv.FormatInt(time.Now().Add(-26*24*time.Hour).Unix(), 10)}
	if !ob.RefreshDue(old) {
		t.Fatal("a 26-day-old token should be renewed")
	}

	before := logins
	accts, err := ob.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 2 || accts[0].Currency != "EUR" || accts[1].Currency != "USD" || accts[0].Name != "Northpeak" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	camps, err := ob.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 51 {
		t.Fatalf("campaigns %d %v", len(camps), err)
	}
	if cp := camps[0]; cp.Status != c.AdActive || cp.Objective != "conversions" || *cp.DailyBudgetMicros != 25_500_000 {
		t.Fatalf("campaign %+v", cp)
	}
	if cp := camps[1]; cp.Status != c.AdPaused || *cp.DailyBudgetMicros != 30_000_000 {
		t.Fatalf("paused campaign with a daily target %+v", cp)
	}
	if cp := camps[50]; cp.ID != "c2" || cp.Status != c.AdRemoved || cp.ProviderStatus != "ARCHIVED" {
		t.Fatalf("archived campaign %+v", cp)
	}

	n := len(*reqs)
	days, err := ob.AdDays(ctx, cl, creds, accts[0], "2026-09-01", "2026-10-02")
	if err != nil || len(days) != 1 {
		t.Fatalf("days %+v %v", days, err)
	}
	d := days[0]
	if d.CampaignID != "c1" || d.Date != "2026-09-02" || d.SpendMicros != 85_960_000 || d.Impressions != 93072 || d.Clicks != 101 ||
		*d.Conversions != 2 || *d.ConversionValueMicros != 97_980_000 {
		t.Fatalf("day %+v", d)
	}
	// 32 days are two report calls: 31 days, then one.
	got := (*reqs)[n:]
	if len(got) != 2 || !strings.Contains(got[0].query, "from=2026-09-01") || !strings.Contains(got[0].query, "to=2026-10-01") ||
		!strings.Contains(got[0].query, "limit=31") || !strings.Contains(got[0].query, "breakdown=daily") || !strings.Contains(got[1].query, "from=2026-10-02") {
		t.Fatalf("report windows %+v", got)
	}
	if logins != before {
		t.Fatalf("a stored token must not log in again (%d logins)", logins-before)
	}
}

func TestOutbrainAdsRefusals(t *testing.T) {
	status := 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(`{"moreInfo":"login","errorMessage":"access denied"}`))
	}))
	t.Cleanup(srv.Close)
	cl := &c.Client{BaseURLs: map[string]string{"outbrain_ads": srv.URL}}
	ob, login := OutbrainAds{}, c.Creds{"username": "ads@acme.example", "password": "pw-test-not-real"}
	if _, err := ob.Test(ctx, cl, login); c.KindOf(err) != c.ErrAuth || !strings.Contains(err.Error(), "username or password") {
		t.Fatalf("a wrong password is an auth error: %v", err)
	}
	status = 429
	if _, err := ob.RefreshCreds(ctx, cl, login); c.KindOf(err) != c.ErrQuota || !strings.Contains(err.Error(), "two token requests an hour") {
		t.Fatalf("too many logins is a quota error: %v", err)
	}
	if _, err := ob.Test(ctx, cl, c.Creds{"username": "ads@acme.example"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no password: %v", err)
	}
	// A stored token Outbrain no longer takes (the password changed) is an auth error too.
	status = 401
	if _, err := ob.AdAccounts(ctx, cl, c.Creds{"token": "ob_test_not_a_real_token"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("revoked token: %v", err)
	}
	status = 400
	if _, err := ob.AdCampaigns(ctx, cl, c.Creds{"token": "ob_test_not_a_real_token"}, c.AdAccount{ID: "m1"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a bad request is invalid: %v", err)
	}
	status = 200
	if _, err := ob.Test(ctx, cl, login); c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("no token in the answer: %v", err)
	}
}

func TestOutbrainAdsReadOnly(t *testing.T) {
	if _, ok := any(OutbrainAds{}).(c.ConversionSender); ok || (OutbrainAds{}).Definition().Has(c.CapAdsConversions) {
		t.Fatal("Outbrain has no conversions API; the connector only reads")
	}
	var _ c.CredsRefresher = OutbrainAds{}
	if d := (OutbrainAds{}).Definition(); d.Auth != c.AuthAPIKey || len(d.Fields) != 2 || !d.Fields[1].Secret {
		t.Fatalf("definition %+v", d)
	}
}
