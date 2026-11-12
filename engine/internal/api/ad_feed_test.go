package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The conversion feed: one call makes the three conversion actions, their
// rules and the payers list on a Google Ads account; a person's first
// payment goes to Google as a NEW customer and later ones as RETURNING,
// through the Data Manager API; the payers list is refreshed; the new
// customer goal is set (new_only only with a big enough list, and never by
// an agent alone); and an account the Data Manager refuses falls back to
// the Ads API upload.
func TestAdFeed(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	named := func(name string) (out []track.Event) {
		for _, ev := range got() {
			if ev.Name == name {
				out = append(out, ev)
			}
		}
		return out
	}
	googleSuffix.Lock()
	googleSuffix.mutates, googleSuffix.uploads, googleSuffix.refuseFeed, googleSuffix.goal = nil, nil, "", ""
	googleSuffix.Unlock()
	mutates := func() string {
		googleSuffix.Lock()
		defer googleSuffix.Unlock()
		return strings.Join(googleSuffix.mutates, "\n")
	}
	uploads := func() []string {
		googleSuffix.Lock()
		defer googleSuffix.Unlock()
		return append([]string(nil), googleSuffix.uploads...)
	}

	scope := "https://www.googleapis.com/auth/adwords https://www.googleapis.com/auth/datamanager"
	var integ string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt", "scope": scope}, connectors.Account{}, "")
		integ = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, integ); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts?connector=google_ads", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	// The feed is the brand's: the account took the workspace's only brand.
	brand := brandOf(t, e, `SELECT brand_id::text FROM ad_accounts WHERE id = $1`, acct)
	if brand == "" {
		t.Fatal("the new ad account has no brand")
	}
	brandBase := "/v1/brands/" + brand
	feedPath := brandBase + "/ads/accounts/" + acct + "/feed"
	// There is no workspace route for it.
	owner.must(404, "GET", base+"/ads/accounts/"+acct+"/feed", nil)

	// Not set up yet; a sign-in with the Data Manager scope needs nothing.
	before := owner.must(200, "GET", feedPath, nil)
	if before["ready"] != false || len(before["actions"].([]any)) != 0 || len(before["setup_needed"].([]any)) != 0 || before["transport"] != "data_manager" {
		t.Fatalf("before: %v", before)
	}
	owner.must(404, "GET", brandBase+"/ads/accounts/00000000-0000-0000-0000-000000000000/feed", nil)
	owner.must(409, "PUT", feedPath, map[string]any{"nca": "bid_higher"})
	// A sign-in from before the scope is asked to connect again.
	if err := e.integrations.UpdateCreds(ctx, e.pool, ws, integ, connectors.Creds{"access_token": "gt"}); err != nil {
		t.Fatal(err)
	}
	if need := owner.must(200, "GET", feedPath, nil)["setup_needed"].([]any); len(need) != 1 || need[0].(map[string]any)["name"] != "reconnect" {
		t.Fatalf("reconnect: %v", need)
	}
	if need := owner.must(200, "GET", base+"/ads/conversion-targets?account_id="+acct, nil)["setup_needed"].([]any); len(need) != 1 || need[0].(map[string]any)["name"] != "reconnect" {
		t.Fatalf("targets reconnect: %v", need)
	}
	if err := e.integrations.UpdateCreds(ctx, e.pool, ws, integ, connectors.Creds{"access_token": "gt", "scope": scope}); err != nil {
		t.Fatal(err)
	}

	// Set up: three actions, a payers list, three rules.
	feed := owner.must(201, "POST", feedPath, nil)
	actions := feed["actions"].([]any)
	rules := feed["rules"].([]any)
	if feed["ready"] != true || len(actions) != 3 || len(rules) != 3 || feed["payers_list"].(map[string]any)["id"] != "901" || feed["nca"].(map[string]any)["mode"] != "off" {
		t.Fatalf("feed: %v", feed)
	}
	byRole := map[string]map[string]any{}
	for _, r := range rules {
		r := r.(map[string]any)
		byRole[r["feed_role"].(string)] = r
	}
	if byRole["signup"]["event_name"] != "signed_up" || byRole["signup"]["customer_type"] != "" ||
		byRole["first_payment"]["event_name"] != "payment.succeeded" || byRole["first_payment"]["customer_type"] != "new" || byRole["first_payment"]["value_property"] != "amount_minor" ||
		byRole["repeat_payment"]["customer_type"] != "returning" || byRole["first_payment"]["target_id"] == byRole["repeat_payment"]["target_id"] {
		t.Fatalf("rules: %v", rules)
	}
	m := mutates()
	for _, want := range []string{`"category":"SIGNUP"`, `"category":"PURCHASE"`, `"countingType":"ONE_PER_CLICK"`, `"clickThroughLookbackWindowDays":"90"`, `"primaryForGoal":true`,
		`"name":"Outflare paying customers"`, `"uploadKeyType":"CONTACT_INFO"`, `"customerTypeCategory":"PURCHASERS"`} {
		if !strings.Contains(m, want) {
			t.Errorf("setup lacks %s:\n%s", want, m)
		}
	}
	if strings.Contains(m, "SUBSCRIBE_PAID") {
		t.Fatal("no subscriptions: the first payment is a purchase")
	}
	owner.must(409, "POST", feedPath, nil)
	if ev := named("ad_feed.created"); len(ev) != 1 || ev[0].Props["first_payment_category"] != "purchase" {
		t.Fatalf("created event: %v", ev)
	}
	if list := owner.must(200, "GET", base+"/ads/conversion-rules?account_id="+acct, nil)["data"].([]any); len(list) != 3 {
		t.Fatalf("rules listed: %d", len(list))
	}

	// Ana signs up and pays twice; Ben pays once but has no consent.
	person := func(name, email string) string {
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name,
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
		exec(`INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, click_ids, country, started_at, last_seen_at)
			VALUES ($1, outflare_first_brand($1), $2, $3, $4, 'US', now() - interval '2 hours', now() - interval '2 hours')`, ws, "anon-"+name, id, map[string]string{"gclid": "g-" + name})
		return id
	}
	ana, ben := person("Ana", "ana@acme.example"), person("Ben", "ben@northpeak.example")
	owner.must(201, "POST", base+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "granted"})
	owner.must(201, "POST", base+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_personalization", "status": "granted"})
	owner.must(201, "POST", base+"/records/"+ben+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "withdrawn"})
	event := func(record, name, msg, props string, minutesAgo int) {
		exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', $3, $4::jsonb, $5, now() - make_interval(mins => $6))`, ws, record, name, props, msg, minutesAgo)
	}
	event(ana, "signed_up", "su-ana", `{}`, 90)
	event(ana, "payment.succeeded", "pay-ana-1", `{"amount_minor": 4900, "currency": "EUR", "payment_id": "in_1"}`, 60)
	event(ana, "payment.succeeded", "pay-ana-2", `{"amount_minor": 4900, "currency": "EUR", "payment_id": "in_2"}`, 30)
	event(ben, "payment.succeeded", "pay-ben-1", `{"amount_minor": 1000, "currency": "EUR", "payment_id": "in_3"}`, 20)
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 4 {
		t.Fatalf("match: %d %v", n, err)
	}
	var firstRule, repeatRule string
	for _, q := range []struct {
		role string
		dst  *string
	}{{"first_payment", &firstRule}, {"repeat_payment", &repeatRule}} {
		*q.dst = byRole[q.role]["id"].(string)
	}
	var firstEvents, repeatEvents []string
	rows, err := e.pool.Query(ctx, `SELECT r.feed_role, q.event_id FROM ad_conversion_sends q JOIN ad_conversion_rules r ON r.id = q.rule_id WHERE q.workspace_id = $1 ORDER BY 1, 2`, ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var role, id string
		rows.Scan(&role, &id)
		if role == "first_payment" {
			firstEvents = append(firstEvents, id)
		} else if role == "repeat_payment" {
			repeatEvents = append(repeatEvents, id)
		}
	}
	rows.Close()
	if strings.Join(firstEvents, ",") != "pay-ana-1,pay-ben-1" || strings.Join(repeatEvents, ",") != "pay-ana-2" {
		t.Fatalf("first %v repeat %v", firstEvents, repeatEvents)
	}
	if res, err := sender.SendRule(ctx, ws, firstRule); err != nil || res.Sent != 1 || res.Skipped != 1 {
		t.Fatalf("first payments: %+v %v", res, err)
	}
	if res, err := sender.SendRule(ctx, ws, repeatRule); err != nil || res.Sent != 1 {
		t.Fatalf("repeat payments: %+v %v", res, err)
	}
	up := uploads()
	if len(up) != 2 || !strings.HasPrefix(up[0], "/v1/events:ingest") {
		t.Fatalf("uploads: %v", up)
	}
	for _, want := range []string{`"customerType":"NEW"`, `"transactionId":"pay-ana-1"`, `"adIdentifiers":{"gclid":"g-Ana"}`, `"conversionValue":49`, `"currency":"EUR"`,
		`"emailAddress":"` + connectors.HashGoogleEmail("ana@acme.example") + `"`, `"adUserData":"CONSENT_GRANTED"`, `"productDestinationId":"` + byRole["first_payment"]["target_id"].(string) + `"`} {
		if !strings.Contains(up[0], want) {
			t.Errorf("first payment lacks %s: %s", want, up[0])
		}
	}
	if !strings.Contains(up[1], `"customerType":"RETURNING"`) || !strings.Contains(up[1], `"transactionId":"pay-ana-2"`) {
		t.Fatalf("repeat payment: %s", up[1])
	}
	if strings.Contains(strings.Join(up, ""), "@") {
		t.Fatal("an email left in the clear")
	}
	sends := owner.must(200, "GET", base+"/ads/conversion-sends?rule_id="+firstRule, nil)["data"].([]any)
	for _, s := range sends {
		s := s.(map[string]any)
		if s["status"] == "sent" && (s["transport"] != "data_manager" || s["request_id"] == "") {
			t.Fatalf("send: %v", s)
		}
	}

	// The payers list: Ana goes in; Ben, who withdrew, is taken out.
	res, err := sender.RefreshPayers(ctx, ws, acct)
	if err != nil || res.Members != 1 || res.Removed != 1 {
		t.Fatalf("payers: %+v %v", res, err)
	}
	up = uploads()
	add, remove := up[len(up)-2], up[len(up)-1]
	if !strings.HasPrefix(add, "/v1/audienceMembers:ingest") || !strings.Contains(add, `"productDestinationId":"901"`) || !strings.Contains(add, connectors.HashGoogleEmail("ana@acme.example")) ||
		!strings.Contains(add, `"adPersonalization":"CONSENT_GRANTED"`) || !strings.HasPrefix(remove, "/v1/audienceMembers:remove") || !strings.Contains(remove, connectors.HashGoogleEmail("ben@northpeak.example")) {
		t.Fatalf("payers uploads:\n%s\n%s", add, remove)
	}
	if pl := owner.must(200, "GET", feedPath, nil)["payers_list"].(map[string]any); pl["members"] != 1.0 || pl["refreshed_at"] == nil || pl["request_id"] == "" {
		t.Fatalf("payers list: %v", pl)
	}

	// The goal: new_only needs a thousand payers; bid_higher sets the goal
	// and a config per campaign.
	owner.must(400, "PUT", feedPath, map[string]any{"nca": "sometimes"})
	owner.must(400, "PUT", feedPath, map[string]any{})
	owner.must(422, "PUT", feedPath, map[string]any{"nca": "new_only"})
	googleSuffix.Lock()
	googleSuffix.mutates = nil
	googleSuffix.Unlock()
	set := owner.must(200, "PUT", feedPath, map[string]any{"nca": "bid_higher", "additional_value_micros": 25_000_000})
	if n := set["nca"].(map[string]any); n["mode"] != "bid_higher" || n["additional_value_micros"] != 25_000_000.0 || n["set_at"] == nil {
		t.Fatalf("nca: %v", set)
	}
	m = mutates()
	for _, want := range []string{`goals:mutate {"operations":[{"create":{"newCustomerAcquisitionGoalSettings":{"valueSettings":{"additionalValue":25}}}}]}`,
		`"campaign":"customers/123/campaigns/7"`, `"targetOption":"TARGET_ALL"`, `"goal":"customers/123/goals/31"`} {
		if !strings.Contains(m, want) {
			t.Errorf("goal calls lack %s:\n%s", want, m)
		}
	}
	exec(`UPDATE ad_accounts SET feed = jsonb_set(feed, '{payers_list,members}', '1200') WHERE id = $1`, acct)
	googleSuffix.Lock()
	googleSuffix.mutates = nil
	googleSuffix.Unlock()
	owner.must(200, "PUT", feedPath, map[string]any{"nca": "new_only"})
	if m = mutates(); !strings.Contains(m, `"targetOption":"TARGET_SPECIFIC"`) || !strings.Contains(m, `"updateMask":"campaign_new_customer_acquisition_settings.target_option"`) {
		t.Fatalf("new only:\n%s", m)
	}
	googleSuffix.Lock()
	googleSuffix.mutates = nil
	googleSuffix.Unlock()
	if off := owner.must(200, "PUT", feedPath, map[string]any{"nca": "off"}); off["nca"].(map[string]any)["mode"] != "off" {
		t.Fatalf("off: %v", off)
	}
	if m = mutates(); !strings.Contains(m, `{"remove":"customers/123/campaignGoalConfigs/7~31"}`) || !strings.Contains(m, `{"remove":"customers/123/goals/31"}`) {
		t.Fatalf("off calls:\n%s", m)
	}
	if ev := named("ad_feed.updated"); len(ev) != 3 {
		t.Fatalf("updated events: %d", len(ev))
	}

	// An agent reads the feed, but its goal change always waits for a
	// person.
	agent, agentID := gatedAgent(t, owner, base, "Ads agent", "analytics.read", "ads.manage")
	owner.must(201, "POST", base+"/action-grants", map[string]any{"token_id": agentID, "path": "ads/**", "mode": "run"})
	agent.must(200, "GET", feedPath, nil)
	if st, held := agent.do("PUT", feedPath, map[string]any{"nca": "bid_higher"}); st != 202 || held["approval_required"] != true ||
		held["action"].(map[string]any)["decided_by_rule"] != "always_person" {
		t.Fatalf("agent: %d %v", st, held)
	}

	// The Data Manager refuses the destination: the account falls back to
	// the Ads API upload, and says so; a person puts it back.
	googleSuffix.Lock()
	googleSuffix.refuseFeed, googleSuffix.uploads = "NOT_ALLOWLISTED", nil
	googleSuffix.Unlock()
	event(ana, "payment.succeeded", "pay-ana-3", `{"amount_minor": 4900, "currency": "EUR", "payment_id": "in_4"}`, 5)
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("match 3: %d %v", n, err)
	}
	if res, err := sender.SendRule(ctx, ws, repeatRule); err != nil || res.Sent != 1 {
		t.Fatalf("fallback send: %+v %v", res, err)
	}
	up = uploads()
	if len(up) != 2 || !strings.HasPrefix(up[0], "/v1/events:ingest") || !strings.HasPrefix(up[1], "/v23/customers/123:uploadClickConversions") || !strings.Contains(up[1], `"customerType":"RETURNING"`) {
		t.Fatalf("fallback uploads: %v", up)
	}
	var transport string
	if err := e.pool.QueryRow(ctx, `SELECT transport FROM ad_conversion_sends WHERE event_id = 'pay-ana-3'`).Scan(&transport); err != nil || transport != "ads_api" {
		t.Fatalf("fallback transport: %s %v", transport, err)
	}
	after := owner.must(200, "GET", feedPath, nil)
	if after["transport"] != "ads_api" || after["transport_reason"] != "NOT_ALLOWLISTED" {
		t.Fatalf("after fallback: %v", after)
	}
	if ev := named("ad_feed.transport_changed"); len(ev) != 1 || ev[0].Props["reason"] != "NOT_ALLOWLISTED" {
		t.Fatalf("transport event: %v", ev)
	}
	googleSuffix.Lock()
	googleSuffix.refuseFeed = ""
	googleSuffix.Unlock()
	if back := owner.must(200, "PUT", feedPath, map[string]any{"transport": "data_manager"}); back["transport"] != "data_manager" || back["transport_reason"] != "" {
		t.Fatalf("back: %v", back)
	}

	// Reconciliation: the days, the campaigns, and nothing to compare yet.
	owner.must(400, "GET", brandBase+"/ads/feed-health", nil)
	h := owner.must(200, "GET", brandBase+"/ads/feed-health?account_id="+acct, nil)
	if len(h["days"].([]any)) != 36 || len(h["campaigns"].([]any)) < 1 || h["drifted"] != false {
		t.Fatalf("health: %v", h)
	}
	raw, _ := json.Marshal(h)
	if !strings.Contains(string(raw), `"sends_accepted":2`) && !strings.Contains(string(raw), `"sends_accepted":3`) {
		t.Fatalf("health sends: %s", raw)
	}

	// Another brand of the workspace doesn't see the account's feed.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/ads/accounts/"+acct+"/feed", nil)
	owner.must(404, "PUT", "/v1/brands/"+other+"/ads/accounts/"+acct+"/feed", map[string]any{"nca": "off"})
	owner.must(404, "GET", "/v1/brands/"+other+"/ads/feed-health?account_id="+acct, nil)
}

// The customer type goes with the Ads API upload too, and a rule made by
// hand can ask for it to be worked out per event.
func TestAdConversionCustomerTypeAuto(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	googleSuffix.Lock()
	googleSuffix.uploads, googleSuffix.refuseFeed = nil, ""
	googleSuffix.Unlock()
	var integ string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		integ = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, integ); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts?connector=google_ads", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "payment.succeeded", "target_id": "42"})
	if rule["feed_role"] != "custom" || rule["customer_type"] != "" {
		t.Fatalf("hand-made rule: %v", rule)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET customer_type = 'auto' WHERE id = $1`, rule["id"]); err != nil {
		t.Fatal(err)
	}
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, click_ids, country, started_at, last_seen_at)
		VALUES ($1, outflare_first_brand($1), 'anon-ana', $2, '{"gclid": "g-1"}', 'US', now() - interval '2 hours', now() - interval '2 hours')`, ws, ana); err != nil {
		t.Fatal(err)
	}
	for i, msg := range []string{"p1", "p2"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', 'payment.succeeded', '{"amount_minor": 100}', $3, now() - make_interval(mins => $4))`, ws, ana, msg, 60-i*10); err != nil {
			t.Fatal(err)
		}
	}
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 2 {
		t.Fatalf("match: %d %v", n, err)
	}
	if res, err := sender.SendRule(ctx, ws, rule["id"].(string)); err != nil || res.Sent != 2 {
		t.Fatalf("send: %+v %v", res, err)
	}
	googleSuffix.Lock()
	up := strings.Join(googleSuffix.uploads, "\n")
	googleSuffix.Unlock()
	if !strings.Contains(up, `"customerType":"NEW"`) || !strings.Contains(up, `"customerType":"RETURNING"`) {
		t.Fatalf("auto customer type:\n%s", up)
	}
}
