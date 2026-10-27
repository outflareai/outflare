package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
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
			map[string]any{"id": "10", "name": "Old test", "effective_status": "PAUSED"},
		}}
	case "/v24.0/act_55/insights":
		var tr struct{ Since, Until string }
		json.Unmarshal([]byte(r.URL.Query().Get("time_range")), &tr)
		rows := []any{}
		from, _ := time.Parse(time.DateOnly, tr.Since)
		to, _ := time.Parse(time.DateOnly, tr.Until)
		for d := from; !d.After(to); d = d.AddDate(0, 0, 1) {
			rows = append(rows, map[string]any{"campaign_id": "9", "campaign_name": "Prospecting", "date_start": d.Format(time.DateOnly),
				"spend": "25.40", "impressions": "1000", "clicks": "20",
				"actions": []any{map[string]any{"action_type": "lead", "value": "2"}}, "action_values": []any{map[string]any{"action_type": "lead", "value": "60"}}})
		}
		out = map[string]any{"data": rows}
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
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
		if strings.Contains(string(b), "FROM customer\"") {
			json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"customer": map[string]any{"id": "123", "descriptiveName": "Acme search", "currencyCode": "EUR", "timeZone": "Europe/Berlin"}}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"results": []any{}})
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
	if len(cat) != 6 {
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
	if res.Accounts != 1 || res.Rows != 90 || res.Campaigns != 2 {
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
	sum := owner.must(200, "GET", base+"/ads/summary?from="+from+"&to="+to, nil)
	rows := sum["data"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["key"] != "meta_ads" || rows[0].(map[string]any)["currency"] != "USD" {
		t.Fatalf("summary: %v", sum)
	}
	days := owner.must(200, "GET", base+"/ads/summary?group=day&from="+from+"&to="+to, nil)["data"].([]any)
	if len(days) != 10 {
		t.Fatalf("by day: %d", len(days))
	}
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
	// Meta needs nothing more.
	meta := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	owner.must(400, "PUT", base+"/ads/connections/"+meta["id"].(string)+"/setup", map[string]any{"values": map[string]string{"developer_token": "x"}})
	owner.must(404, "PUT", base+"/ads/connections/00000000-0000-0000-0000-000000000000/setup", map[string]any{"values": map[string]string{"developer_token": "x"}})
}
