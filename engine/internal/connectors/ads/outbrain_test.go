package ads

import (
	"encoding/base64"
	"encoding/json"
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

func TestOutbrainAdsSendsNoConversions(t *testing.T) {
	if _, ok := any(OutbrainAds{}).(c.ConversionSender); ok || (OutbrainAds{}).Definition().Has(c.CapAdsConversions) {
		t.Fatal("Outbrain has no conversions API; the connector sends none")
	}
	var _ c.CredsRefresher = OutbrainAds{}
	if d := (OutbrainAds{}).Definition(); d.Auth != c.AuthAPIKey || len(d.Fields) != 2 || !d.Fields[1].Secret {
		t.Fatalf("definition %+v", d)
	}
}

// Changes: a campaign is paused and resumed with PUT /campaigns/{id}, its
// own daily budget set with PUT /budgets/{id}, and a publisher or section
// blocked by sending the whole blocked list back with the one change
// (Amplify's "Update an Existing Campaign" and "Update an existing
// Budget").
func TestOutbrainAdsChanges(t *testing.T) {
	enabled := true
	blocked := map[string]any{"blockedPublishers": []any{map[string]any{"id": "pub-old", "name": "Old site", "creationTime": "2026-10-01 00:00:00", "expiration": "2027-01-20"}},
		"blockedSections": []any{}, "marketerBlockedSites": map[string]any{}}
	budget := map[string]any{"id": "b1", "type": "DAILY", "amount": 25.5, "shared": false}
	withBlocks := true
	var puts []string
	cl, reqs, _ := fake(t, map[string]any{
		"GET /campaigns/c1": func(r *http.Request) any {
			out := map[string]any{"id": "c1", "name": "Launch", "enabled": enabled, "objective": "Conversions", "budget": budget}
			if withBlocks && r.URL.Query().Get("extraFields") == "BlockedSites" {
				out["blockedSites"] = blocked
			}
			return out
		},
		"PUT /campaigns/c1": func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			if v, ok := b["enabled"].(bool); ok {
				enabled = v
			}
			if v, ok := b["blockedSites"].(map[string]any); ok {
				blocked = v
			}
			raw, _ := json.Marshal(b)
			puts = append(puts, "campaign "+string(raw))
			return map[string]any{"id": "c1"}
		},
		"PUT /budgets/b1": func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			raw, _ := json.Marshal(b)
			puts = append(puts, "budget "+string(raw))
			return map[string]any{"id": "b1"}
		},
	})
	ob, creds, acct := OutbrainAds{}, c.Creds{"username": "ads@northpeak.example", "password": "pw-test-not-real", "token": "ob_test_not_a_real_token"}, c.AdAccount{ID: "m1", Currency: "EUR"}
	if d := ob.Definition(); !d.Has(c.CapAdsManage) {
		t.Fatalf("capabilities %v", d.Capabilities)
	}
	got, err := ob.AdCampaign(ctx, cl, creds, acct, "c1")
	if err != nil || got.Status != c.AdActive || got.DailyBudgetMicros == nil || *got.DailyBudgetMicros != 25_500_000 {
		t.Fatalf("campaign %+v %v", got, err)
	}
	if err := ob.SetCampaignStatus(ctx, cl, creds, acct, "c1", false); err != nil {
		t.Fatal(err)
	}
	if got, _ := ob.AdCampaign(ctx, cl, creds, acct, "c1"); got.Status != c.AdPaused {
		t.Fatalf("after the pause %+v", got)
	}
	if err := ob.SetCampaignStatus(ctx, cl, creds, acct, "c1", true); err != nil {
		t.Fatal(err)
	}
	if err := ob.SetCampaignDailyBudget(ctx, cl, creds, acct, "c1", 40_000_000); err != nil {
		t.Fatal(err)
	}
	// A block keeps what was blocked before, sends ids only, and a second
	// one of the same changes nothing.
	pub := c.AdExclusion{Kind: c.ExcludePlacement, Value: "pub-17"}
	sec := c.AdExclusion{Kind: c.ExcludePlacement, Value: "sec-9", Detail: "section"}
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", pub); err != nil {
		t.Fatal(err)
	}
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", pub); err != nil {
		t.Fatal(err)
	}
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", sec); err != nil {
		t.Fatal(err)
	}
	if err := ob.RemoveExclusion(ctx, cl, creds, acct, "c1", pub); err != nil {
		t.Fatal(err)
	}
	if err := ob.RemoveExclusion(ctx, cl, creds, acct, "c1", pub); err != nil {
		t.Fatal(err)
	}
	want := []string{
		`campaign {"enabled":false}`, `campaign {"enabled":true}`, `budget {"amount":40}`,
		`campaign {"blockedSites":{"blockedPublishers":[{"id":"pub-old"},{"id":"pub-17"}],"blockedSections":[]}}`,
		`campaign {"blockedSites":{"blockedPublishers":[{"id":"pub-old"},{"id":"pub-17"}],"blockedSections":[{"id":"sec-9"}]}}`,
		`campaign {"blockedSites":{"blockedPublishers":[{"id":"pub-old"}],"blockedSections":[{"id":"sec-9"}]}}`,
	}
	if strings.Join(puts, "\n") != strings.Join(want, "\n") {
		t.Fatalf("sent:\n%s", strings.Join(puts, "\n"))
	}
	for _, r := range *reqs {
		if r.header.Get("OB-TOKEN-V1") != "ob_test_not_a_real_token" {
			t.Fatalf("a call without the token: %+v", r)
		}
	}

	// Outbrain not saying what is blocked now: nothing is sent, or the one
	// new entry would unblock everything else.
	sent := len(puts)
	withBlocks = false
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", c.AdExclusion{Kind: c.ExcludePlacement, Value: "pub-18"}); c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("with no current list: %v", err)
	}
	withBlocks = true
	// An entry with no id means the list wasn't understood: nothing is
	// sent either, and an id of a shape Outflare doesn't expect goes back
	// as it came.
	blocked = map[string]any{"blockedPublishers": []any{map[string]any{"name": "No id"}}, "blockedSections": []any{}}
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", c.AdExclusion{Kind: c.ExcludePlacement, Value: "pub-18"}); c.KindOf(err) != c.ErrUnavailable {
		t.Fatalf("with an entry that has no id: %v", err)
	}
	if len(puts) != sent {
		t.Fatalf("sent with an unreadable list: %v", puts[sent:])
	}
	blocked = map[string]any{"blockedPublishers": []any{map[string]any{"id": "pub.odd:1"}}, "blockedSections": []any{}}
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", c.AdExclusion{Kind: c.ExcludePlacement, Value: "pub-18"}); err != nil {
		t.Fatal(err)
	}
	if got := puts[len(puts)-1]; got != `campaign {"blockedSites":{"blockedPublishers":[{"id":"pub.odd:1"},{"id":"pub-18"}],"blockedSections":[]}}` {
		t.Fatalf("an odd id was not kept: %s", got)
	}
	sent = len(puts)
	// A full list, a shared budget, a budget that isn't daily, bad ids and
	// other kinds are refused too.
	full := []any{}
	for i := range 30 {
		full = append(full, map[string]any{"id": "p" + strconv.Itoa(i)})
	}
	blocked = map[string]any{"blockedPublishers": full, "blockedSections": []any{}}
	if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", c.AdExclusion{Kind: c.ExcludePlacement, Value: "pub-31"}); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "30 publishers") {
		t.Fatalf("a full list: %v", err)
	}
	budget["shared"] = true
	if err := ob.SetCampaignDailyBudget(ctx, cl, creds, acct, "c1", 40_000_000); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "shared") {
		t.Fatalf("a shared budget: %v", err)
	}
	budget["shared"], budget["type"] = false, "MONTHLY"
	if err := ob.SetCampaignDailyBudget(ctx, cl, creds, acct, "c1", 40_000_000); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "monthly") {
		t.Fatalf("a monthly budget: %v", err)
	}
	for _, bad := range []c.AdExclusion{{Kind: c.ExcludePlacement, Value: "a b/c"}, {Kind: c.ExcludePlacement}, {Kind: c.ExcludeDevice, Value: "mobile"}} {
		if err := ob.AddExclusion(ctx, cl, creds, acct, "c1", bad); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	if err := ob.SetCampaignStatus(ctx, cl, creds, acct, "../x", false); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("a bad campaign id: %v", err)
	}
	if len(puts) != sent {
		t.Fatalf("a refused change was sent: %v", puts[sent:])
	}
	// A budget paced by a daily target takes the new figure there.
	budget["pacing"] = "DAILY_TARGET"
	if err := ob.SetCampaignDailyBudget(ctx, cl, creds, acct, "c1", 12_340_000); err != nil || puts[len(puts)-1] != `budget {"dailyTarget":12.34}` {
		t.Fatalf("a daily target: %v %v", err, puts[len(puts)-1])
	}
	if _, err := ob.AdCampaign(ctx, cl, creds, acct, "nope"); err != c.ErrNotFound {
		t.Fatalf("a campaign that isn't there: %v", err)
	}
}

// Ads come from the promoted content report; their days from the periodic
// content report of the campaigns that spent.
func TestOutbrainCampaignAds(t *testing.T) {
	periodic := func(id string, days ...map[string]any) map[string]any {
		var rs []any
		for i, m := range days {
			rs = append(rs, map[string]any{"metadata": map[string]any{"id": "2026-10-0" + strconv.Itoa(i+1), "fromDate": "2026-10-0" + strconv.Itoa(i+1)}, "metrics": m})
		}
		return map[string]any{"promotedLinkId": id, "results": rs}
	}
	cl, reqs, _ := fake(t, map[string]any{
		"GET /reports/marketers/m1/promotedContent": map[string]any{"results": []any{
			map[string]any{"metadata": map[string]any{"id": "p1", "title": "Ten desk lamps", "url": "https://acme.example/lamps", "enabled": true, "approvalStatus": "APPROVED", "campaignId": "c1"},
				"metrics": map[string]any{"impressions": 9000, "clicks": 40, "spend": 12.5}},
			map[string]any{"metadata": map[string]any{"id": "p2", "title": "Chairs", "url": "https://acme.example/chairs", "enabled": false,
				"approvalStatus": map[string]any{"status": "REJECTED", "reasons": []string{"IMAGE"}}, "campaignId": "c1"}, "metrics": map[string]any{"impressions": 100, "clicks": 1, "spend": 0.5}},
			map[string]any{"metadata": map[string]any{"id": "p3", "title": "Rugs", "campaignId": "c2"}, "metrics": map[string]any{"impressions": 50, "clicks": 0, "spend": 0.1}},
			map[string]any{"metadata": map[string]any{"id": "", "title": "No id", "campaignId": "c2"}, "metrics": map[string]any{"spend": 99}},
		}, "totalResults": 4},
		"GET /reports/marketers/m1/campaigns/c1/periodicContent": map[string]any{"promotedLinkResults": []any{
			periodic("p1", map[string]any{"impressions": 5000, "clicks": 25, "spend": 7.5, "conversions": 2, "sumValue": 40}, map[string]any{"impressions": 4000, "clicks": 15, "spend": 5}),
			periodic("p2", map[string]any{"impressions": 100, "clicks": 1, "spend": 0.5}, map[string]any{"impressions": 0, "clicks": 0, "spend": 0}),
		}, "totalPromotedLinks": 2},
		"GET /reports/marketers/m1/campaigns/c2/periodicContent": map[string]any{"promotedLinkResults": []any{
			periodic("p3", map[string]any{"impressions": 50, "clicks": 0, "spend": 0.1}),
		}},
	})
	ob, creds, acct := OutbrainAds{}, c.Creds{"token": "ob_test_not_a_real_token"}, c.AdAccount{ID: "m1", Currency: "EUR"}
	items, days, err := ob.CampaignAds(ctx, cl, creds, acct, "2026-10-01", "2026-10-02")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 3 || items[0].ID != "p1" || items[0].ParentID != "c1" || items[0].Status != c.AdActive || items[0].ProviderStatus != "ENABLED" ||
		items[0].Details[c.AdDetailHeadline] != "Ten desk lamps" || items[0].Details[c.AdDetailURL] != "https://acme.example/lamps" || items[0].Details[c.AdDetailPolicy] != "approved" ||
		items[1].Status != c.AdPaused || items[1].Details[c.AdDetailPolicy] != "disapproved" ||
		items[2].ID != "p3" || items[2].ProviderStatus != "" || items[2].ParentID != "c2" {
		t.Fatalf("items %+v", items)
	}
	if _, ok := items[0].Details[c.AdDetailTemplated]; ok {
		t.Fatal("Outbrain's tracking is on the campaign, so a promoted link says nothing about the template")
	}
	if len(days) != 4 || days[0].ID != "p1" || days[0].ParentID != "c1" || days[0].Date != "2026-10-01" || days[0].SpendMicros != 7_500_000 || *days[0].Conversions != 2 ||
		*days[0].ConversionValueMicros != 40_000_000 || days[1].Date != "2026-10-02" || days[1].Conversions != nil || days[2].ID != "p2" || days[3].ID != "p3" || days[3].ParentID != "c2" {
		t.Fatalf("days %+v", days)
	}
	// One content call, then the campaign that spent most first.
	var calls []string
	for _, r := range *reqs {
		calls = append(calls, r.path)
	}
	if strings.Join(calls, " ") != "/reports/marketers/m1/promotedContent /reports/marketers/m1/campaigns/c1/periodicContent /reports/marketers/m1/campaigns/c2/periodicContent" {
		t.Fatalf("calls %v", calls)
	}
	if q := (*reqs)[0].query; !strings.Contains(q, "from=2026-10-01") || !strings.Contains(q, "to=2026-10-02") || !strings.Contains(q, "limit=500") || !strings.Contains(q, "sort=-spend") {
		t.Fatalf("content call %s", q)
	}
	if q := (*reqs)[1].query; !strings.Contains(q, "breakdown=daily") || !strings.Contains(q, "limit=2") {
		t.Fatalf("periodic call %s", q)
	}
	// A long window reads its last 31 days only.
	n := len(*reqs)
	if _, _, err := ob.CampaignAds(ctx, cl, creds, acct, "2026-07-01", "2026-09-30"); err != nil {
		t.Fatal(err)
	}
	if q := (*reqs)[n].query; !strings.Contains(q, "from=2026-08-31") || !strings.Contains(q, "to=2026-09-30") {
		t.Fatalf("long window %s", q)
	}
}

// One promoted link is switched off and on by its own id.
func TestOutbrainAdStatus(t *testing.T) {
	var bodies []string
	cl, reqs, _ := fake(t, map[string]any{
		"PUT /promotedLinks/p1": func(r *http.Request) any {
			var b map[string]any
			_ = json.NewDecoder(r.Body).Decode(&b)
			raw, _ := json.Marshal(b)
			bodies = append(bodies, string(raw))
			return map[string]any{"id": "p1", "enabled": b["enabled"]}
		},
	})
	ob, creds, acct := OutbrainAds{}, c.Creds{"token": "ob_test_not_a_real_token"}, c.AdAccount{ID: "m1"}
	if err := ob.SetAdStatus(ctx, cl, creds, acct, "c1", "p1", false); err != nil {
		t.Fatal(err)
	}
	if err := ob.SetAdStatus(ctx, cl, creds, acct, "c1", "p1", true); err != nil {
		t.Fatal(err)
	}
	if strings.Join(bodies, " ") != `{"enabled":false} {"enabled":true}` || (*reqs)[0].header.Get("OB-TOKEN-V1") != "ob_test_not_a_real_token" {
		t.Fatalf("bodies %v", bodies)
	}
	n := len(*reqs)
	for _, bad := range []string{"", "p1/../x", "a b"} {
		if err := ob.SetAdStatus(ctx, cl, creds, acct, "c1", bad, false); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("%q: %v", bad, err)
		}
	}
	if len(*reqs) != n {
		t.Fatal("a bad id reached Outbrain")
	}
}
