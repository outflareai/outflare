package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// fakeMetaAds answers the Marketing API for the token "mt": one account
// with one campaign that spends 25.40 USD on every day asked for.
type fakeMetaAds struct{}

func (fakeMetaAds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer mt" {
		w.WriteHeader(401)
		return
	}
	var out any
	switch r.URL.Path {
	case "/v24.0/me":
		out = map[string]any{"id": "1", "name": "Northpeak ads"}
	case "/v24.0/me/adaccounts":
		out = map[string]any{"data": []any{map[string]any{"account_id": "55", "name": "Northpeak", "currency": "USD", "timezone_name": "UTC"}}}
	case "/v24.0/act_55/campaigns":
		out = map[string]any{"data": []any{
			map[string]any{"id": "9", "name": "Prospecting", "effective_status": "ACTIVE", "objective": "OUTCOME_LEADS", "daily_budget": "3000"},
			map[string]any{"id": "10", "name": "Old test", "effective_status": "PAUSED", "daily_budget": "500"},
		}}
	case "/v24.0/9", "/v24.0/10":
		// Managing: a POST changes the campaign's state, which a GET then
		// shows (campaign 9 starts active at $30 a day, 10 paused).
		id := strings.TrimPrefix(r.URL.Path, "/v24.0/")
		metaState.Lock()
		defer metaState.Unlock()
		if metaState.campaigns == nil {
			metaState.campaigns = map[string]map[string]any{
				"9":  {"id": "9", "name": "Prospecting", "effective_status": "ACTIVE", "objective": "OUTCOME_LEADS", "daily_budget": "3000", "account_id": "55"},
				"10": {"id": "10", "name": "Old test", "effective_status": "PAUSED", "objective": "OUTCOME_LEADS", "daily_budget": "500", "account_id": "55"},
			}
		}
		cp := metaState.campaigns[id]
		if r.Method == "POST" {
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			if metaState.refuse {
				w.WriteHeader(400)
				out = map[string]any{"error": map[string]any{"message": "Invalid parameter", "code": 100}}
				break
			}
			if st := body["status"]; st != "" {
				cp["effective_status"] = st
			}
			if b := body["daily_budget"]; b != "" {
				cp["daily_budget"] = b
			}
			metaState.posts = append(metaState.posts, r.Method+" "+id+" "+body["status"]+body["daily_budget"])
			out = map[string]any{"success": true}
			break
		}
		out = cp
	case "/v24.0/act_55/adsets":
		out = map[string]any{"data": []any{map[string]any{"id": "31", "name": "Lookalike", "effective_status": "ACTIVE", "campaign_id": "9"}}}
	case "/v24.0/act_55/ads":
		out = map[string]any{"data": []any{map[string]any{"id": "41", "name": "Demo video", "effective_status": "ACTIVE", "adset_id": "31",
			"creative": map[string]any{"object_story_spec": map[string]any{"link_data": map[string]any{"link": "https://acme.example/demo", "name": "Book a demo"}}}}}}
	case "/v24.0/act_55/adspixels":
		out = map[string]any{"data": []any{map[string]any{"id": "777", "name": "Northpeak pixel"}}}
	case "/v24.0/777/events":
		var body struct {
			Data []map[string]any `json:"data"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		metaSent.Lock()
		metaSent.events = append(metaSent.events, body.Data...)
		metaSent.Unlock()
		out = map[string]any{"events_received": len(body.Data)}
	case "/v24.0/act_55/customaudiences":
		out = map[string]any{"data": []any{map[string]any{"id": "801", "name": "Trial users", "subtype": "CUSTOM"},
			map[string]any{"id": "803", "name": "Site visitors", "subtype": "WEBSITE"}}}
	case "/v24.0/801/users":
		var body struct {
			Payload struct {
				Schema []string   `json:"schema"`
				Data   [][]string `json:"data"`
			} `json:"payload"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		metaAudience.Lock()
		for _, row := range body.Payload.Data {
			metaAudience.calls = append(metaAudience.calls, r.Method+" "+body.Payload.Schema[0]+" "+row[0])
		}
		metaAudience.Unlock()
		out = map[string]any{"audience_id": "801", "num_received": len(body.Payload.Data)}
	case "/v24.0/802/users":
		w.WriteHeader(400)
		out = map[string]any{"error": map[string]any{"message": "Custom audience terms not accepted", "code": 2650}}
	case "/v24.0/act_55/insights":
		var tr struct{ Since, Until string }
		json.Unmarshal([]byte(r.URL.Query().Get("time_range")), &tr)
		rows := []any{}
		from, _ := time.Parse(time.DateOnly, tr.Since)
		to, _ := time.Parse(time.DateOnly, tr.Until)
		// The one ad set, and its one ad, are all of the campaign.
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			row := map[string]any{"campaign_id": "9", "campaign_name": "Prospecting", "date_start": d.Format(time.DateOnly),
				"spend": "25.40", "impressions": "1000", "clicks": "20",
				"reach": "800", "actions": []any{map[string]any{"action_type": "lead", "value": "2"}, map[string]any{"action_type": "offsite_conversion.fb_pixel_lead", "value": "2"},
					map[string]any{"action_type": "offsite_conversion.custom.555", "value": "1"}, map[string]any{"action_type": "link_click", "value": "15"},
					map[string]any{"action_type": "landing_page_view", "value": "12"}, map[string]any{"action_type": "video_view", "value": "40"}},
				"action_values": []any{map[string]any{"action_type": "lead", "value": "60"}}}
			switch r.URL.Query().Get("level") {
			case "adset":
				row["adset_id"], row["adset_name"] = "31", "Lookalike"
			case "ad":
				row["ad_id"], row["ad_name"], row["adset_id"] = "41", "Demo video", "31"
			}
			rows = append(rows, row)
		}
		out = map[string]any{"data": rows}
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

// metaState is what fakeMetaAds' campaigns look like after changes, and
// the changes it took; refuse makes it refuse the next ones.
var metaState struct {
	sync.Mutex
	campaigns map[string]map[string]any
	posts     []string
	refuse    bool
}

// metaAudience collects what fakeMetaAds took into audience 801:
// "METHOD SCHEMA hash".
var metaAudience struct {
	sync.Mutex
	calls []string
}

// metaSent collects the events fakeMetaAds took through the Conversions API.
var metaSent struct {
	sync.Mutex
	events []map[string]any
}

// fakeGoogleAds accepts only the developer token "dev-ok".
type fakeGoogleAds struct{}

func (fakeGoogleAds) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("developer-token") != "dev-ok" {
		w.WriteHeader(403)
		w.Write([]byte(`{"error":{"message":"DEVELOPER_TOKEN_INVALID"}}`))
		return
	}
	switch r.URL.Path {
	case "/v23/customers:listAccessibleCustomers":
		json.NewEncoder(w).Encode(map[string]any{"resourceNames": []string{"customers/123"}})
	case "/v23/customers/123/googleAds:search":
		b, _ := io.ReadAll(r.Body)
		q := string(b)
		if strings.Contains(q, "FROM customer\"") {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"customer": map[string]any{"id": "123", "descriptiveName": "Acme search", "currencyCode": "EUR", "timeZone": "Pacific/Kiritimati"}}}})
			return
		}
		// One campaign, ad group, ad and keyword, with numbers on the last
		// day asked for.
		day := ""
		if m := regexp.MustCompile(`AND '([0-9-]{10})'`).FindStringSubmatch(q); m != nil {
			day = m[1]
		}
		metrics := map[string]any{"costMicros": "9000000", "impressions": "300", "clicks": "12", "conversions": 2.0, "conversionsValue": 80.0}
		row := func(r map[string]any) map[string]any {
			r["segments"], r["metrics"] = map[string]any{"date": day}, metrics
			return r
		}
		camp := map[string]any{"id": "7", "name": "Brand search", "status": "ENABLED", "advertisingChannelType": "SEARCH"}
		group := map[string]any{"id": "71", "name": "Exact brand", "status": "ENABLED"}
		var out []any
		switch {
		case strings.Contains(q, "conversion_action_name"):
			id := map[string]any{"campaign": map[string]any{"id": "7"}, "adGroup": map[string]any{"id": "71"}, "adGroupAd": map[string]any{"ad": map[string]any{"id": "81"}}}
			id["segments"] = map[string]any{"date": day, "conversionActionName": "Demo booked"}
			id["metrics"] = map[string]any{"conversions": 2.0, "conversionsValue": 80.0}
			out = []any{id}
		case strings.Contains(q, "FROM keyword_view"):
			out = []any{row(map[string]any{"adGroup": map[string]any{"id": "71"}, "adGroupCriterion": map[string]any{"criterionId": "91", "status": "ENABLED",
				"keyword": map[string]any{"text": "acme crm", "matchType": "EXACT"}}})}
		case strings.Contains(q, "FROM search_term_view"):
			r1 := row(map[string]any{"adGroup": map[string]any{"id": "71"}, "searchTermView": map[string]any{"searchTerm": "acme crm pricing"}})
			r1["segments"].(map[string]any)["keyword"] = map[string]any{"info": map[string]any{"text": "acme crm", "matchType": "EXACT"}}
			out = []any{r1}
		case strings.Contains(q, "FROM ad_group_ad WHERE segments"):
			out = []any{row(map[string]any{"adGroup": map[string]any{"id": "71"}, "adGroupAd": map[string]any{"ad": map[string]any{"id": "81"}}})}
		case strings.Contains(q, "FROM ad_group_ad"):
			out = []any{map[string]any{"adGroup": map[string]any{"id": "71", "finalUrlSuffix": "utm_source=google&of_campaign={campaignid}&of_ad={creative}"}, "adGroupAd": map[string]any{"status": "ENABLED",
				"ad": map[string]any{"id": "81", "type": "RESPONSIVE_SEARCH_AD", "finalUrls": []string{"https://acme.example/pricing"},
					"responsiveSearchAd": map[string]any{"headlines": []any{map[string]any{"text": "Acme CRM"}}}}}}}
		case strings.Contains(q, "FROM ad_group WHERE segments"):
			out = []any{row(map[string]any{"adGroup": group, "campaign": map[string]any{"id": "7"}})}
		case strings.Contains(q, "FROM ad_group"):
			out = []any{map[string]any{"adGroup": group, "campaign": map[string]any{"id": "7"}}}
		case strings.Contains(q, "FROM campaign WHERE segments"):
			out = []any{row(map[string]any{"campaign": camp})}
		case strings.Contains(q, "FROM campaign"):
			out = []any{map[string]any{"campaign": camp}}
		}
		json.NewEncoder(w).Encode(map[string]any{"results": out})
	default:
		http.NotFound(w, r)
	}
}

func TestAdsSyncAndReports(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Nothing connected is not an error, and every ad platform is listed.
	if got := owner.must(200, "GET", base+"/ads/connections", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	cat := owner.must(200, "GET", base+"/connectors?category=ads", nil)["data"].([]any)
	if len(cat) != 13 {
		t.Fatalf("ads catalog: %d", len(cat))
	}

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	id := i["id"].(string)
	conns := owner.must(200, "GET", base+"/ads/connections", nil)["data"].([]any)
	if len(conns) != 1 || len(conns[0].(map[string]any)["setup_needed"].([]any)) != 0 {
		t.Fatalf("meta needs no setup: %v", conns)
	}

	res, err := e.ads.SyncOnce(ctx, ws, id)
	if err != nil {
		t.Fatal(err)
	}
	// The first run reads 90 days for the one campaign that spent.
	if res.Accounts != 1 || res.Rows != 90 || res.Campaigns != 2 || res.AdGroups != 1 || res.Ads != 1 {
		t.Fatalf("first sync: %+v", res)
	}
	accts := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)
	acct := accts[0].(map[string]any)
	if len(accts) != 1 || acct["external_id"] != "55" || acct["sync_on"] != true || acct["first_day"] == nil || acct["brand_id"] != nil {
		t.Fatalf("accounts: %v", accts)
	}

	// Every campaign-day with spend is in the spend ledger, once.
	var n, cents int64
	var channel string
	if err := e.pool.QueryRow(ctx, `SELECT count(*), sum(amount_cents), min(channel) FROM spend_entries WHERE workspace_id = $1 AND category = 'ads'`, ws).Scan(&n, &cents, &channel); err != nil {
		t.Fatal(err)
	}
	if n != 90 || cents != 90*2540 || channel != "paid_ads" {
		t.Fatalf("ledger: %d rows, %d cents, channel %q", n, cents, channel)
	}
	if res, err := e.ads.SyncOnce(ctx, ws, id); err != nil || res.Rows != 7 {
		t.Fatalf("second sync should re-read 7 days: %+v %v", res, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND category = 'ads'`, ws).Scan(&n); err != nil || n != 90 {
		t.Fatalf("re-reading must not duplicate the ledger: %d %v", n, err)
	}

	today := time.Now().UTC()
	from := today.AddDate(0, 0, -9).Format(time.DateOnly)
	to := today.Format(time.DateOnly)
	camps := owner.must(200, "GET", base+"/ads/campaigns?from="+from+"&to="+to, nil)["data"].([]any)
	if len(camps) != 1 {
		t.Fatalf("campaigns with spend or active: %v", camps)
	}
	c := camps[0].(map[string]any)
	tot := c["totals"].(map[string]any)
	if c["name"] != "Prospecting" || c["status"] != "active" || c["daily_budget_micros"] != 30_000_000.0 || tot["spend_micros"] != 254_000_000.0 ||
		tot["clicks"] != 200.0 || tot["conversions"] != 20.0 || tot["cpc_micros"] != 1_270_000.0 || tot["cost_per_conversion_micros"] != 12_700_000.0 {
		t.Fatalf("campaign: %v", c)
	}
	if roas := tot["platform_roas"].(float64); roas < 2.36 || roas > 2.37 {
		t.Fatalf("platform roas: %v", roas)
	}
	// Meta's delivery numbers; a lead it reports twice (its own and the
	// pixel's) counts once, and a custom conversion isn't in the total.
	if tot["link_clicks"] != 150.0 || tot["landing_page_views"] != 120.0 || tot["video_views"] != 400.0 || tot["daily_reach"] != 8000.0 || tot["frequency"] != 1.25 {
		t.Fatalf("delivery: %v", tot)
	}
	acts := map[string]float64{}
	for _, x := range owner.must(200, "GET", base+"/ads/conversion-actions?from="+from+"&to="+to, nil)["data"].([]any) {
		acts[x.(map[string]any)["action"].(string)] = x.(map[string]any)["conversions"].(float64)
	}
	if len(acts) != 2 || acts["lead"] != 20 || acts["custom.555"] != 10 {
		t.Fatalf("conversions by action: %v", acts)
	}
	// The ad links without the tracking template, so tracking health names
	// it.
	h := owner.must(200, "GET", base+"/ads/tracking-health", nil)
	if bad := h["ads_without_template"].([]any); len(bad) != 1 || bad[0].(map[string]any)["external_id"] != "41" || bad[0].(map[string]any)["campaign_name"] != "Prospecting" {
		t.Fatalf("ads without the template: %v", h["ads_without_template"])
	}
	sum := owner.must(200, "GET", base+"/ads/summary?from="+from+"&to="+to, nil)
	rows := sum["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["key"] != "meta_ads" || rows[0].(map[string]any)["currency"] != "USD" {
		t.Fatalf("summary: %v", sum)
	}
	days := owner.must(200, "GET", base+"/ads/summary?group=day&from="+from+"&to="+to, nil)["data"].([]any)
	if len(days) != 10 {
		t.Fatalf("by day: %d", len(days))
	}
	// Ad sets and ads repeat their campaign's numbers, so totals count
	// campaigns only.
	if tot := sum["totals"].([]any)[0].(map[string]any)["totals"].(map[string]any); tot["spend_micros"] != 254_000_000.0 || tot["clicks"] != 200.0 {
		t.Fatalf("totals count each day once: %v", tot)
	}

	// Below the campaign: its ad set, and the ad in it.
	campID := c["id"].(string)
	sets := owner.must(200, "GET", base+"/ads/items?level=ad_group&from="+from+"&to="+to, nil)["data"].([]any)
	if len(sets) != 1 {
		t.Fatalf("ad sets: %v", sets)
	}
	set := sets[0].(map[string]any)
	if set["external_id"] != "31" || set["name"] != "Lookalike" || set["parent_id"] != campID || set["campaign_id"] != campID || set["campaign_name"] != "Prospecting" ||
		set["totals"].(map[string]any)["spend_micros"] != 254_000_000.0 {
		t.Fatalf("ad set: %v", set)
	}
	ads := owner.must(200, "GET", base+"/ads/items?campaign_id="+campID+"&from="+from+"&to="+to, nil)["data"].([]any)
	if len(ads) != 1 {
		t.Fatalf("ads: %v", ads)
	}
	ad := ads[0].(map[string]any)
	if ad["level"] != "ad" || ad["external_id"] != "41" || ad["parent_id"] != set["id"] || ad["parent_name"] != "Lookalike" || ad["campaign_id"] != campID ||
		ad["details"].(map[string]any)["url"] != "https://acme.example/demo" || ad["details"].(map[string]any)["headline"] != "Book a demo" ||
		ad["totals"].(map[string]any)["conversions"] != 20.0 {
		t.Fatalf("ad: %v", ad)
	}
	if byParent := owner.must(200, "GET", base+"/ads/items?parent_id="+set["id"].(string), nil)["data"].([]any); len(byParent) != 1 {
		t.Fatalf("ads in the ad set: %v", byParent)
	}
	if other := owner.must(200, "GET", base+"/ads/items?campaign_id=00000000-0000-0000-0000-000000000000", nil)["data"].([]any); len(other) != 0 {
		t.Fatalf("ads in another campaign: %v", other)
	}
	// One ad's trend by day.
	if trend := owner.must(200, "GET", base+"/ads/summary?group=day&entity_id="+ad["id"].(string)+"&from="+from+"&to="+to, nil)["data"].([]any); len(trend) != 10 ||
		trend[0].(map[string]any)["totals"].(map[string]any)["spend_micros"] != 25_400_000.0 {
		t.Fatalf("ad by day: %v", trend)
	}
	owner.must(400, "GET", base+"/ads/items?level=campaign", nil)
	owner.must(400, "GET", base+"/ads/items?campaign_id=nope", nil)
	owner.must(400, "GET", base+"/ads/summary?entity_id=nope", nil)
	owner.must(400, "GET", base+"/ads/summary?group=weekday", nil)
	owner.must(400, "GET", base+"/ads/campaigns?from=yesterday", nil)
	owner.must(400, "GET", base+"/ads/campaigns?status=live", nil)

	// An account belongs to a brand; its spend in the ledger follows.
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	acctID := acct["id"].(string)
	got := owner.must(200, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"brand": "geonode"})
	if got["brand_id"] != geo["id"] {
		t.Fatalf("brand: %v", got)
	}
	var branded int64
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM spend_entries WHERE workspace_id = $1 AND brand_id = $2`, ws, geo["id"]).Scan(&branded); err != nil || branded != 90 {
		t.Fatalf("ledger rows with the brand: %d %v", branded, err)
	}
	if b := owner.must(200, "GET", base+"/ads/summary?group=brand&from="+from+"&to="+to, nil)["data"].([]any); b[0].(map[string]any)["key"] != geo["id"] {
		t.Fatalf("by brand: %v", b)
	}
	if none := owner.must(200, "GET", base+"/ads/campaigns?brand=none", nil)["data"].([]any); len(none) != 0 {
		t.Fatalf("no unbranded campaigns left: %v", none)
	}
	owner.must(400, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"brand": "nope"})
	owner.must(404, "PATCH", base+"/ads/accounts/00000000-0000-0000-0000-000000000000", map[string]any{"sync_on": false})
	if got := owner.must(200, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"sync_on": false, "brand": "none"}); got["sync_on"] != false || got["brand_id"] != nil {
		t.Fatalf("switch off: %v", got)
	}
	if res, err := e.ads.SyncOnce(ctx, ws, id); err != nil || res.Accounts != 0 {
		t.Fatalf("an account switched off is not read: %+v %v", res, err)
	}
	owner.must(202, "POST", base+"/ads/sync", nil)

	// Read-only tokens can read but not change accounts.
	reader := agentToken(t, owner, base, "ads reader", "analytics.read")
	reader.must(200, "GET", base+"/ads/summary", nil)
	reader.must(403, "PATCH", base+"/ads/accounts/"+acctID, map[string]any{"sync_on": true})
	agentToken(t, owner, base, "no analytics", "workspace.read").must(403, "GET", base+"/ads/campaigns", nil)
}

// Google Ads signs in with Google, then needs the workspace's own developer
// token before it can read anything.
func TestAdsDeveloperTokenSetup(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var id string
	err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		id = i.ID
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	conns := owner.must(200, "GET", base+"/ads/connections", nil)["data"].([]any)
	need := conns[0].(map[string]any)["setup_needed"].([]any)
	if len(need) != 1 || need[0].(map[string]any)["name"] != "developer_token" {
		t.Fatalf("setup needed: %v", conns)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, id); connectors.KindOf(err) != connectors.ErrAuth || !strings.Contains(err.Error(), "developer token") {
		t.Fatalf("sync without a developer token: %v", err)
	}

	owner.must(400, "PUT", base+"/ads/connections/"+id+"/setup", map[string]any{"values": map[string]string{}})
	owner.must(422, "PUT", base+"/ads/connections/"+id+"/setup", map[string]any{"values": map[string]string{"developer_token": "wrong"}})
	got := owner.must(200, "PUT", base+"/ads/connections/"+id+"/setup", map[string]any{"values": map[string]string{"developer_token": "dev-ok"}})
	if len(got["setup_needed"].([]any)) != 0 || got["status"] != "connected" {
		t.Fatalf("after setup: %v", got)
	}
	// The token is sealed with the credentials, never shown back.
	_, listed := owner.do("GET", base+"/ads/connections", nil)
	if raw, _ := json.Marshal(listed); strings.Contains(string(raw), "dev-ok") {
		t.Fatal("the developer token leaked into a response")
	}
	res, err := e.ads.SyncOnce(ctx, ws, id)
	if err != nil || res.Accounts != 1 {
		t.Fatalf("sync after setup: %+v %v", res, err)
	}
	if a := owner.must(200, "GET", base+"/ads/accounts?connector=google_ads", nil)["data"].([]any); a[0].(map[string]any)["currency"] != "EUR" {
		t.Fatalf("google account: %v", a)
	}
	// Below the campaign: the ad carries the template through its ad
	// group's final URL suffix; the keyword and the search it matched;
	// conversions by Google's conversion action.
	g := owner.must(200, "GET", base+"/ads/items?connector=google_ads", nil)["data"].([]any)
	if len(g) != 1 || g[0].(map[string]any)["details"].(map[string]any)["templated"] != "true" || g[0].(map[string]any)["details"].(map[string]any)["url_tags"] == "" {
		t.Fatalf("google ads: %v", g)
	}
	// The fake's numbers land on the account's own day (UTC+14), which is
	// already tomorrow in UTC for most of the day: the default range still
	// covers it.
	window := "connector=google_ads"
	kw := owner.must(200, "GET", base+"/ads/keywords?"+window, nil)["data"].([]any)
	if len(kw) != 1 || kw[0].(map[string]any)["text"] != "acme crm" || kw[0].(map[string]any)["match_type"] != "exact" || kw[0].(map[string]any)["campaign_name"] != "Brand search" ||
		kw[0].(map[string]any)["totals"].(map[string]any)["spend_micros"] != 9_000_000.0 {
		t.Fatalf("keywords: %v", kw)
	}
	st := owner.must(200, "GET", base+"/ads/search-terms?"+window+"&ad_group_id="+kw[0].(map[string]any)["ad_group_id"].(string), nil)["data"].([]any)
	if len(st) != 1 || st[0].(map[string]any)["term"] != "acme crm pricing" || st[0].(map[string]any)["keyword_text"] != "acme crm" ||
		st[0].(map[string]any)["totals"].(map[string]any)["clicks"] != 12.0 {
		t.Fatalf("search terms: %v", st)
	}
	acts := owner.must(200, "GET", base+"/ads/conversion-actions?"+window+"&connector=google_ads", nil)["data"].([]any)
	if len(acts) != 1 || acts[0].(map[string]any)["action"] != "Demo booked" || acts[0].(map[string]any)["conversions"] != 2.0 || acts[0].(map[string]any)["value_micros"] != 80_000_000.0 {
		t.Fatalf("google actions: %v", acts)
	}
	// Re-reading the window replaces its searches rather than adding them.
	if _, err := e.ads.SyncOnce(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if st := owner.must(200, "GET", base+"/ads/search-terms?"+window, nil)["data"].([]any); len(st) != 1 || st[0].(map[string]any)["totals"].(map[string]any)["clicks"] != 12.0 {
		t.Fatalf("search terms after a second sync: %v", st)
	}
	owner.must(400, "GET", base+"/ads/keywords?campaign_id=nope", nil)
	owner.must(400, "GET", base+"/ads/search-terms?limit=0", nil)
	owner.must(400, "GET", base+"/ads/conversion-actions?entity_id=nope", nil)

	// Meta needs nothing more.
	meta := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	owner.must(400, "PUT", base+"/ads/connections/"+meta["id"].(string)+"/setup", map[string]any{"values": map[string]string{"developer_token": "x"}})
	owner.must(404, "PUT", base+"/ads/connections/00000000-0000-0000-0000-000000000000/setup", map[string]any{"values": map[string]string{"developer_token": "x"}})
}
