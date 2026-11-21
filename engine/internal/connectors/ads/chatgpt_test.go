package ads

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func chatgptFake(t *testing.T) (*c.Client, *[]seen) {
	t.Helper()
	cl, reqs, _ := fake(t, map[string]any{
		"GET /ad_account": map[string]any{"id": "adacct_1", "name": "Northpeak", "status": "active", "timezone": "America/New_York", "currency_code": "usd"},
		"GET /campaigns": func(r *http.Request) any {
			if r.URL.Query().Get("after") == "" {
				return map[string]any{"object": "list", "data": []any{
					map[string]any{"id": "cmpn_1", "name": "Launch", "status": "active", "bidding_type": "clicks", "budget": map[string]any{"daily_spend_limit_micros": 50_000_000}},
				}, "first_id": "cmpn_1", "last_id": "cmpn_1", "has_more": true}
			}
			return map[string]any{"object": "list", "data": []any{
				map[string]any{"id": "cmpn_2", "name": "Old", "status": "paused", "bidding_type": "conversions", "budget": map[string]any{"lifetime_spend_limit_micros": 25_000_000}},
			}, "first_id": "cmpn_2", "last_id": "cmpn_2", "has_more": false}
		},
		"GET /campaigns/cmpn_1":           map[string]any{"id": "cmpn_1", "name": "Launch", "status": "active", "budget": map[string]any{"daily_spend_limit_micros": 50_000_000}},
		"GET /campaigns/cmpn_2":           map[string]any{"id": "cmpn_2", "name": "Old", "status": "paused", "budget": map[string]any{"lifetime_spend_limit_micros": 25_000_000}},
		"POST /campaigns/cmpn_1":          map[string]any{"id": "cmpn_1"},
		"POST /campaigns/cmpn_1/pause":    map[string]any{"id": "cmpn_1", "status": "paused"},
		"POST /campaigns/cmpn_1/activate": map[string]any{"id": "cmpn_1", "status": "active"},
		"GET /ad_account/insights": map[string]any{"object": "list", "data": []any{
			map[string]any{"readable_time": "2026-11-10", "campaign_id": "cmpn_1", "campaign_name": "Launch", "impressions": 1200, "clicks": 36, "spend": 18.42},
			map[string]any{"readable_time": "2026-11-11", "campaign_id": "cmpn_1", "campaign_name": "Launch", "impressions": 0, "clicks": 0, "spend": 0},
		}, "count": 2, "has_more": false},
		"POST /conversions/insights": map[string]any{"object": "list", "account_currency": "USD", "data": []any{
			map[string]any{"entity_id": "cmpn_1", "date": "2026-11-10", "conversions": 4, "order_created_attributed_sales": "120.5", "order_created_attributed_sales_currency": "USD"},
		}, "count": 1},
		"POST /events": map[string]any{},
	})
	return cl, reqs
}

func TestChatGPTAdsRead(t *testing.T) {
	cl, reqs := chatgptFake(t)
	g, creds := ChatGPTAds{}, c.Creds{"api_key": "oai_ads_test_not_a_real_key"}
	if _, err := g.Test(ctx, cl, c.Creds{}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no key: %v", err)
	}
	acct, err := g.Test(ctx, cl, creds)
	if err != nil || acct.ID != "adacct_1" || acct.Name != "Northpeak" {
		t.Fatalf("test %+v %v", acct, err)
	}
	accts, err := g.AdAccounts(ctx, cl, creds)
	if err != nil || len(accts) != 1 || accts[0].Currency != "USD" || accts[0].TimeZone != "America/New_York" {
		t.Fatalf("accounts %+v %v", accts, err)
	}
	if h := (*reqs)[0].header.Get("Authorization"); h != "Bearer oai_ads_test_not_a_real_key" {
		t.Fatalf("auth %q", h)
	}
	camps, err := g.AdCampaigns(ctx, cl, creds, accts[0])
	if err != nil || len(camps) != 2 || camps[0].DailyBudgetMicros == nil || *camps[0].DailyBudgetMicros != 50_000_000 ||
		camps[1].Status != c.AdPaused || camps[1].DailyBudgetMicros != nil {
		t.Fatalf("campaigns %+v %v", camps, err)
	}
	if _, err := g.AdCampaigns(ctx, cl, creds, c.AdAccount{ID: "adacct_other"}); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("another account: %v", err)
	}
	days, err := g.AdDays(ctx, cl, creds, accts[0], "2026-11-10", "2026-11-11")
	if err != nil || len(days) != 1 {
		t.Fatalf("days %+v %v", days, err)
	}
	d := days[0]
	if d.SpendMicros != 18_420_000 || d.Clicks != 36 || d.Conversions == nil || *d.Conversions != 4 || d.ConversionValueMicros == nil || *d.ConversionValueMicros != 120_500_000 {
		t.Fatalf("day %+v", d)
	}
	var insights, convs seen
	for _, r := range *reqs {
		switch r.path {
		case "/ad_account/insights":
			insights = r
		case "/conversions/insights":
			convs = r
		}
	}
	if !strings.Contains(insights.query, "aggregation_level=campaign") || !strings.Contains(insights.query, "time_granularity=daily") ||
		!strings.Contains(insights.query, "2026-11-10") {
		t.Fatalf("insights query %s", insights.query)
	}
	var body map[string]any
	_ = json.Unmarshal([]byte(convs.body), &body)
	if ids, _ := body["entity_ids"].([]any); len(ids) != 1 || ids[0] != "cmpn_1" || body["time_granularity"] != "daily" {
		t.Fatalf("conversions body %s", convs.body)
	}
}

func TestChatGPTAdsConversionsReportRefused(t *testing.T) {
	cl, _, _ := fake(t, map[string]any{
		"GET /ad_account/insights": map[string]any{"data": []any{
			map[string]any{"readable_time": "2026-11-10", "campaign_id": "cmpn_1", "impressions": 10, "clicks": 1, "spend": 1.5},
		}},
	})
	days, err := (ChatGPTAds{}).AdDays(ctx, cl, c.Creds{"api_key": "k"}, c.AdAccount{ID: "adacct_1"}, "2026-11-10", "2026-11-10")
	if err != nil || len(days) != 1 || days[0].Conversions != nil || days[0].SpendMicros != 1_500_000 {
		t.Fatalf("spend must survive a refused conversions report: %+v %v", days, err)
	}
}

func TestChatGPTAdsManage(t *testing.T) {
	cl, reqs := chatgptFake(t)
	g, creds, acct := ChatGPTAds{}, c.Creds{"api_key": "k"}, c.AdAccount{ID: "adacct_1", Currency: "USD"}
	camp, err := g.AdCampaign(ctx, cl, creds, acct, "cmpn_1")
	if err != nil || camp.Status != c.AdActive || *camp.DailyBudgetMicros != 50_000_000 {
		t.Fatalf("campaign %+v %v", camp, err)
	}
	if err := g.SetCampaignStatus(ctx, cl, creds, acct, "cmpn_1", false); err != nil {
		t.Fatal(err)
	}
	if last := (*reqs)[len(*reqs)-1]; last.method != "POST" || last.path != "/campaigns/cmpn_1/pause" {
		t.Fatalf("pause %+v", last)
	}
	if err := g.SetCampaignStatus(ctx, cl, creds, acct, "cmpn_1", true); err != nil || (*reqs)[len(*reqs)-1].path != "/campaigns/cmpn_1/activate" {
		t.Fatalf("activate %v", err)
	}
	if err := g.SetCampaignDailyBudget(ctx, cl, creds, acct, "cmpn_1", 12_345_678); err != nil {
		t.Fatal(err)
	}
	if last := (*reqs)[len(*reqs)-1]; last.path != "/campaigns/cmpn_1" || last.body != `{"budget":{"daily_spend_limit_micros":12350000}}` {
		t.Fatalf("budget %+v", last)
	}
	if err := g.SetCampaignDailyBudget(ctx, cl, creds, acct, "cmpn_2", 10_000_000); c.KindOf(err) != c.ErrInvalid || !strings.Contains(err.Error(), "lifetime") {
		t.Fatalf("lifetime budget: %v", err)
	}
	if err := g.SetCampaignDailyBudget(ctx, cl, creds, acct, "cmpn_1", 500_000); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("below 1: %v", err)
	}
	for _, bad := range []string{"", "cmpn/../x", strings.Repeat("a", 90)} {
		if err := g.SetCampaignStatus(ctx, cl, creds, acct, bad, true); c.KindOf(err) != c.ErrInvalid {
			t.Fatalf("id %q: %v", bad, err)
		}
	}
	if err := g.SetCampaignStatus(ctx, cl, creds, c.AdAccount{ID: "adacct_other"}, "cmpn_1", true); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("another account: %v", err)
	}
}

func TestChatGPTAdsSendConversions(t *testing.T) {
	cl, reqs := chatgptFake(t)
	g, acct := ChatGPTAds{}, c.AdAccount{ID: "adacct_1"}
	if _, err := g.ConversionTargets(ctx, cl, c.Creds{"api_key": "k"}, acct); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("no pixel: %v", err)
	}
	creds := c.Creds{"api_key": "k", "pixel_id": "px_9", "conversions_key": "conv_test_not_a_real_key", "website": "https://northpeak.example/"}
	targets, err := g.ConversionTargets(ctx, cl, creds, acct)
	if err != nil || len(targets) != 1 || targets[0].ID != "px_9" || targets[0].EventTypes[0] != "registration_completed" {
		t.Fatalf("targets %+v %v", targets, err)
	}
	now := time.Now().Add(-time.Hour)
	buy := conv(0, map[string]string{"oppref": "oppref_abc", "obref": "ob-1"})
	buy.Time, buy.EventType = now, "order_created"
	buy.IPAddress, buy.UserAgent, buy.Country = "203.0.113.7", "Mozilla/5.0 (test)", "PT"
	old := conv(1, map[string]string{"oppref": "oppref_old"})
	none := conv(2, nil)
	none.Time, none.Emails, none.PhoneDigits, none.ExternalID = now, nil, nil, ""
	odd := conv(3, nil)
	odd.Time, odd.EventType = now, "Purchase"
	res, err := g.SendConversions(ctx, cl, creds, acct, targets[0], []c.AdConversion{buy, old, none, odd})
	if err != nil {
		t.Fatal(err)
	}
	if !res[0].OK || !res[1].Skipped || !res[2].Skipped || !res[3].Skipped {
		t.Fatalf("results %+v", res)
	}
	last := (*reqs)[len(*reqs)-1]
	if last.path != "/events" || last.query != "pid=px_9" || last.header.Get("Authorization") != "Bearer conv_test_not_a_real_key" {
		t.Fatalf("send %+v", last)
	}
	var body struct {
		Source string           `json:"integration_source"`
		Events []map[string]any `json:"events"`
	}
	_ = json.Unmarshal([]byte(last.body), &body)
	if body.Source != "outflare" || len(body.Events) != 1 {
		t.Fatalf("body %s", last.body)
	}
	ev := body.Events[0]
	user, _ := ev["user"].(map[string]any)
	data, _ := ev["data"].(map[string]any)
	if ev["type"] != "order_created" || ev["oppref"] != "oppref_abc" || ev["action_source"] != "web" || ev["source_url"] != "https://northpeak.example" ||
		ev["timestamp_ms"] != float64(now.UnixMilli()) || user["obref"] != "ob-1" || data["type"] != "contents" || data["amount"] != float64(1250) || data["currency"] != "USD" {
		t.Fatalf("event %v", ev)
	}
	if ems, _ := user["emails_sha256"].([]any); len(ems) != 1 || ems[0] != c.HashID("ana@acme.example") {
		t.Fatalf("emails %v", user)
	}
	if cs, _ := user["countries"].([]any); user["ip_address"] != "203.0.113.7" || user["user_agent"] != "Mozilla/5.0 (test)" || len(cs) != 1 || cs[0] != "PT" {
		t.Fatalf("where the person was: %v", user)
	}
	// Without a website the events are sent as "other".
	delete(creds, "website")
	if _, err := g.SendConversions(ctx, cl, creds, acct, targets[0], []c.AdConversion{buy}); err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(strings.NewReader((*reqs)[len(*reqs)-1].body))
	if !strings.Contains(string(b), `"action_source":"other"`) || strings.Contains(string(b), "source_url") {
		t.Fatalf("no website: %s", b)
	}
}

// Custom audiences: the list is found by its name or made, and people go
// in and come out as hashed emails and phones, each request with an
// Idempotency-Key that is the same for the same people.
func TestChatGPTAdsAudiences(t *testing.T) {
	cl, reqs, _ := fake(t, map[string]any{
		"GET /ad_account":       map[string]any{"id": "adacct_1", "name": "Northpeak", "status": "active", "timezone": "UTC", "currency_code": "usd"},
		"GET /custom_audiences": map[string]any{"object": "list", "data": []any{map[string]any{"id": "caud_1", "name": "Refunded (Outflare)"}}, "has_more": false},
		"POST /custom_audiences": func(r *http.Request) any {
			return map[string]any{"id": "caud_2", "status": "active", "membership_revision": 0}
		},
		"POST /custom_audiences/caud_1/add":    map[string]any{"operation_id": "op_1", "status": "processing"},
		"POST /custom_audiences/caud_1/remove": map[string]any{"operation_id": "op_2", "status": "processing"},
	})
	g, creds, acct := ChatGPTAds{}, c.Creds{"api_key": "oai_ads_test_not_a_real_key"}, c.AdAccount{ID: "adacct_1"}
	if !g.Definition().Has(c.CapAdsAudiences) {
		t.Fatal("no audiences capability")
	}
	id, err := g.CreateAudience(ctx, cl, creds, acct, "Refunded (Outflare)", "Kept in step with a list in Outflare.")
	if err != nil || id != "caud_1" {
		t.Fatalf("found: %q %v", id, err)
	}
	id, err = g.CreateAudience(ctx, cl, creds, acct, "Payers (Outflare)", "Kept in step with a list in Outflare.")
	if err != nil || id != "caud_2" {
		t.Fatalf("made: %q %v", id, err)
	}
	if last := (*reqs)[len(*reqs)-1]; last.method != "POST" || !strings.Contains(last.body, `"name":"Payers (Outflare)"`) {
		t.Fatalf("create: %+v", last)
	}
	people := []c.AudienceMember{{Emails: []string{c.HashID("ana@acme.example")}, Phones: []string{c.HashID("+15550100")}}, {Emails: []string{c.HashID("ben@northpeak.example")}}}
	keys := func(path string) (out []string, body string) {
		for _, r := range *reqs {
			if r.method == "POST" && r.path == path {
				out, body = append(out, r.header.Get("Idempotency-Key")), r.body
			}
		}
		return out, body
	}
	op, err := g.IngestAudience(ctx, cl, creds, acct, "caud_1", c.AudienceAdd, people)
	if err != nil || op != "op_1" {
		t.Fatalf("add: %q %v", op, err)
	}
	if _, err := g.IngestAudience(ctx, cl, creds, acct, "caud_1", c.AudienceAdd, people); err != nil {
		t.Fatal(err)
	}
	k, body := keys("/custom_audiences/caud_1/add")
	if len(k) != 2 || k[0] == "" || k[0] != k[1] {
		t.Fatalf("idempotency keys: %v", k)
	}
	var sent struct {
		Identifiers []map[string]string `json:"identifiers"`
	}
	if err := json.Unmarshal([]byte(body), &sent); err != nil || len(sent.Identifiers) != 3 ||
		sent.Identifiers[0]["identifier_type"] != "email_sha256" || sent.Identifiers[0]["identifier"] != c.HashID("ana@acme.example") ||
		sent.Identifiers[1]["identifier_type"] != "phone_number_sha256" || strings.Contains(body, "acme.example") {
		t.Fatalf("identifiers: %s", body)
	}
	if op, err := g.IngestAudience(ctx, cl, creds, acct, "caud_1", c.AudienceRemove, people[:1]); err != nil || op != "op_2" {
		t.Fatalf("remove: %q %v", op, err)
	}
	if rk, _ := keys("/custom_audiences/caud_1/remove"); len(rk) != 1 || rk[0] == k[0] {
		t.Fatalf("remove key: %v", rk)
	}
	if _, err := g.IngestAudience(ctx, cl, creds, acct, "not an id", c.AudienceAdd, people); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("bad id: %v", err)
	}
	if _, err := g.IngestAudience(ctx, cl, creds, c.AdAccount{ID: "adacct_9"}, "caud_1", c.AudienceAdd, people); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("another account's key: %v", err)
	}
}
