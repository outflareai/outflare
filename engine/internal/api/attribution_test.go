package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/digests"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAttribution(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	s := owner.must(200, "GET", base+"/attribution/settings", nil)
	if s["model"] != "last_non_direct" || s["lookback_days"] != float64(30) || len(s["conversion_events"].([]any)) != 2 {
		t.Fatalf("settings = %v", s)
	}
	owner.must(400, "PATCH", base+"/attribution/settings", map[string]any{"model": "most_recent"})
	owner.must(400, "PATCH", base+"/attribution/settings", map[string]any{"lookback_days": 400})
	owner.must(400, "PATCH", base+"/attribution/settings", map[string]any{"conversion_events": []string{"payment.refunded"}})
	owner.must(400, "PATCH", base+"/attribution/settings", map[string]any{"conversion_events": []string{}})

	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	rec := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws)
	now := time.Now()
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, referrer_host) VALUES ($1, $2, 'anon-1', $3, 'www.google.com') RETURNING id::text`,
		ws, rec, now.Add(-5*24*time.Hour))
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium, utm_campaign)
		VALUES ($1, $2, 'anon-1', $3, 'google', 'cpc', 'Spring') RETURNING id::text`, ws, rec, now.Add(-2*24*time.Hour))
	exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, started_at) VALUES ($1, 'anon-2', $2) RETURNING id::text`, ws, now.Add(-time.Hour))
	exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'signed_up', $3) RETURNING id::text`,
		ws, rec, now.Add(-24*time.Hour))
	exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, occurred_at)
		VALUES ($1, $2, 'track', 'payment.succeeded', '{"amount_minor": 4900, "currency": "usd", "payment_id": "ch_test_2"}', $3) RETURNING id::text`,
		ws, rec, now.Add(-time.Hour))
	exec(`INSERT INTO spend_entries (workspace_id, category, channel, amount_cents, currency, occurred_at) VALUES ($1, 'ads', 'paid_ads', 12000, 'USD', $2) RETURNING id::text`,
		ws, now.Add(-24*time.Hour))
	exec(`INSERT INTO spend_entries (workspace_id, category, amount_cents, currency, occurred_at) VALUES ($1, 'software', 3000, 'USD', $2) RETURNING id::text`,
		ws, now.Add(-24*time.Hour))

	run := &attribution.Runner{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Minute}
	if _, _, err := run.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// The weekly digest has the channels in brief, under the default model.
	secs, err := digests.Build(ctx, e.pool, ws, digests.Period{Start: now.AddDate(0, 0, -7), End: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	var sec *digests.Section
	for i := range secs {
		if secs[i].ID == "channels" {
			sec = &secs[i]
		}
	}
	if sec == nil {
		t.Fatalf("no channels section in %v", secs)
	}
	metrics := map[string]float64{}
	for _, m := range sec.Metrics {
		metrics[m.Key] = m.Value
	}
	if metrics["channel_visits"] != 3 || metrics["channel_conversions"] != 2 || metrics["channel_revenue_usd"] != 49 || metrics["channel_cost_usd"] != 150 {
		t.Fatalf("channel metrics = %v", metrics)
	}
	if len(sec.Items) != 1 || sec.Items[0].Text != "Paid ads: 2 conversions, 49 USD" || sec.Items[0].Tab != "channels" {
		t.Fatalf("channel items = %v", sec.Items)
	}

	byChannel := func(model string) map[string]map[string]any {
		t.Helper()
		path := base + "/attribution/channels"
		if model != "" {
			path += "?model=" + model
		}
		out := map[string]map[string]any{}
		for _, x := range owner.must(200, "GET", path, nil)["data"].([]any) {
			m := x.(map[string]any)
			out[m["channel"].(string)] = m
		}
		return out
	}
	ch := byChannel("")
	paid, seo, direct := ch["paid_ads"], ch["seo"], ch["direct"]
	if paid["visits"] != float64(1) || paid["conversions"] != float64(2) || seo["conversions"] != float64(0) || direct["visits"] != float64(1) {
		t.Fatalf("channels = paid %v seo %v direct %v", paid, seo, direct)
	}
	if rev := paid["revenue"].([]any); len(rev) != 1 || rev[0].(map[string]any)["amount_cents"] != float64(4900) || rev[0].(map[string]any)["currency"] != "USD" {
		t.Fatalf("paid revenue = %v", paid["revenue"])
	}
	if cost := paid["cost"].([]any); len(cost) != 1 || cost[0].(map[string]any)["amount_cents"] != float64(12000) {
		t.Fatalf("paid cost = %v", paid["cost"])
	}
	// First touch credits search instead.
	if ch := byChannel("first_touch"); ch["seo"]["conversions"] != float64(2) {
		t.Fatalf("first touch seo = %v", ch["seo"])
	}
	owner.must(400, "GET", base+"/attribution/channels?model=nope", nil)
	all := owner.must(200, "GET", base+"/attribution/channels", nil)
	if u := all["unallocated_cost"].([]any); len(u) != 1 || u[0].(map[string]any)["amount_cents"] != float64(3000) {
		t.Fatalf("unallocated = %v", all["unallocated_cost"])
	}

	camps := owner.must(200, "GET", base+"/attribution/campaigns?channel=paid_ads", nil)["data"].([]any)
	if len(camps) != 1 || camps[0].(map[string]any)["campaign"] != "spring" || camps[0].(map[string]any)["source"] != "google" {
		t.Fatalf("campaigns = %v", camps)
	}
	owner.must(400, "GET", base+"/attribution/campaigns?channel=tv", nil)

	conv := owner.must(200, "GET", base+"/attribution/conversions?name=payment.succeeded", nil)["data"].([]any)
	if len(conv) != 1 {
		t.Fatalf("conversions = %v", conv)
	}
	c := conv[0].(map[string]any)
	credits := c["credits"].([]any)
	if c["value_cents"] != float64(4900) || c["matched_by"] != "record" || len(credits) != 1 || credits[0].(map[string]any)["channel"] != "paid_ads" {
		t.Fatalf("conversion = %v", c)
	}

	// Changing what counts credits everything again.
	s = owner.must(200, "PATCH", base+"/attribution/settings", map[string]any{"model": "linear", "conversion_events": []string{"signed_up"}})
	if s["model"] != "linear" || s["backfilled_at"] != nil {
		t.Fatalf("patched = %v", s)
	}
	if _, _, err := run.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if ch := byChannel(""); ch["paid_ads"]["conversions"] != float64(0.5) || ch["seo"]["conversions"] != float64(0.5) {
		t.Fatalf("linear after change = paid %v seo %v", ch["paid_ads"], ch["seo"])
	}

	var updated bool
	for _, ev := range got() {
		if ev.Name == "attribution.settings_updated" && ev.WorkspaceID == ws {
			updated = true
		}
	}
	if !updated {
		t.Fatal("no attribution.settings_updated event")
	}
}

// A campaign splits by utm_content, such as the sequence step a link went
// out in: visits from the sessions, credit from the credited visit's session.
func TestAttributionCampaignsByContent(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	camp := exec(`INSERT INTO tracking_campaigns (workspace_id, name, slug, code, channel, source, medium, object_kind, object_id)
		VALUES ($1, 'Outbound', 'outbound', 'code000001', 'outbound_email', 'outflare', 'email', 'sequence', 'seq') RETURNING id::text`, ws)
	now := time.Now()
	session := func(content string) string {
		return exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, channel, source, campaign, tracking_campaign_id, utm_content)
			VALUES ($1, 'anon-'||$2, $3, 'outbound_email', 'outflare', 'outbound', $4, $2) RETURNING id::text`, ws, content, now.Add(-time.Hour), camp)
	}
	s1, s2 := session("step-1"), session("step-2")
	session("step-2")
	ev := exec(`INSERT INTO customer_events (workspace_id, kind, name, occurred_at) VALUES ($1, 'track', 'signed_up', $2) RETURNING id::text`, ws, now)
	exec(`INSERT INTO attribution_conversions (event_id, workspace_id, name, occurred_at) VALUES ($1, $2, 'signed_up', $3) RETURNING event_id::text`, ev, ws, now)
	exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, channel, source, campaign, tracking_campaign_id, weight)
		VALUES ($1, $2, 'last_non_direct', 0, $3, 'outbound_email', 'outflare', 'outbound', $4, 1) RETURNING event_id::text`, ev, ws, s2, camp)
	_ = s1

	rows := owner.must(200, "GET", base+"/attribution/campaigns?split=content&tracking_campaign="+camp, nil)["data"].([]any)
	got := map[string][2]float64{}
	for _, r := range rows {
		m := r.(map[string]any)
		got[m["content"].(string)] = [2]float64{m["visits"].(float64), m["conversions"].(float64)}
	}
	if len(got) != 2 || got["step-1"] != [2]float64{1, 0} || got["step-2"] != [2]float64{2, 1} {
		t.Fatalf("by content = %v", rows)
	}
	// Without the split, one row for the campaign.
	if rows := owner.must(200, "GET", base+"/attribution/campaigns?tracking_campaign="+camp, nil)["data"].([]any); len(rows) != 1 ||
		rows[0].(map[string]any)["visits"] != 3.0 || rows[0].(map[string]any)["content"] != "" {
		t.Fatalf("whole campaign = %v", rows)
	}
	owner.must(400, "GET", base+"/attribution/campaigns?split=step", nil)
	owner.must(400, "GET", base+"/attribution/campaigns?tracking_campaign=nope", nil)
}

// match=identified keeps only visits the person was identified on, and the
// credit on them; each credit says how its visit was matched.
func TestAttributionIdentifiedOnly(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	rec := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws)
	now := time.Now()
	known := exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, channel, source) VALUES ($1, $2, 'anon-1', $3, 'seo', 'google') RETURNING id::text`,
		ws, rec, now.Add(-2*time.Hour))
	cookie := exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, started_at, channel, source) VALUES ($1, 'anon-2', $2, 'paid_ads', 'google') RETURNING id::text`,
		ws, now.Add(-time.Hour))
	conv := func(matched string) string {
		ev := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'signed_up', $3) RETURNING id::text`, ws, rec, now)
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, touches, matched_by) VALUES ($1, $2, $3, 'signed_up', $4, 1, $5) RETURNING event_id::text`,
			ev, ws, rec, now, matched)
		return ev
	}
	credit := func(ev string, pos int, session, channel string, weight float64) {
		exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, channel, source, weight)
			VALUES ($1, $2, 'last_non_direct', $3, $4, $5, 'google', $6) RETURNING event_id::text`, ev, ws, pos, session, channel, weight)
	}
	both := conv("record")
	credit(both, 0, known, "seo", 0.5)
	credit(both, 1, cookie, "paid_ads", 0.5)
	credit(conv("anonymous"), 0, cookie, "paid_ads", 1)

	channels := func(q string) map[string][2]float64 {
		t.Helper()
		out := map[string][2]float64{}
		for _, r := range owner.must(200, "GET", base+"/attribution/channels"+q, nil)["data"].([]any) {
			m := r.(map[string]any)
			out[m["channel"].(string)] = [2]float64{m["visits"].(float64), m["conversions"].(float64)}
		}
		return out
	}
	if all := channels(""); all["seo"] != [2]float64{1, 0.5} || all["paid_ads"] != [2]float64{1, 1.5} {
		t.Fatalf("all = %v", all)
	}
	if id := channels("?match=identified"); id["seo"] != [2]float64{1, 0.5} || id["paid_ads"] != [2]float64{0, 0} {
		t.Fatalf("identified = %v", id)
	}
	owner.must(400, "GET", base+"/attribution/channels?match=probable", nil)

	list := owner.must(200, "GET", base+"/attribution/conversions?match=identified", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["event_id"] != both {
		t.Fatalf("identified conversions = %v", list)
	}
	how := map[string]string{}
	for _, c := range list[0].(map[string]any)["credits"].([]any) {
		m := c.(map[string]any)
		how[m["channel"].(string)] = m["matched_by"].(string)
	}
	if how["seo"] != "record" || how["paid_ads"] != "browser" {
		t.Fatalf("credit matches = %v", how)
	}
	if n := len(owner.must(200, "GET", base+"/attribution/conversions", nil)["data"].([]any)); n != 2 {
		t.Fatalf("all conversions = %d", n)
	}
}

// Warm or cold, new or repeat, and lifetime value by first channel.
func TestAttributionCustomersAndCohorts(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	now := time.Now()
	// Wes was a lead long before his visit from search; Cy first came from an
	// ad and became a record when he signed up.
	wes := exec(`INSERT INTO records (workspace_id, record_type, name, created_at) VALUES ($1, 'person', 'Wes', $2) RETURNING id::text`, ws, now.AddDate(0, 0, -10))
	cy := exec(`INSERT INTO records (workspace_id, record_type, name, created_at) VALUES ($1, 'person', 'Cy', $2) RETURNING id::text`, ws, now.Add(-30*time.Minute))
	visit := func(rec, channel string, at time.Time) string {
		return exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, channel, source) VALUES ($1, $2::uuid, 'anon-'||$2::text, $3, $4, 'google') RETURNING id::text`,
			ws, rec, at, channel)
	}
	wesVisit, cyVisit := visit(wes, "seo", now.Add(-48*time.Hour)), visit(cy, "paid_ads", now.Add(-time.Hour))
	conv := func(rec, name string, at time.Time, value any, session string, touch time.Time, channel string) {
		t.Helper()
		ev := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', $3, $4) RETURNING id::text`, ws, rec, name, at)
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, value_minor, currency, touches, matched_by)
			VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $6::bigint IS NULL THEN '' ELSE 'USD' END, 1, 'record') RETURNING event_id::text`, ev, ws, rec, name, at, value)
		for _, m := range []string{"last_non_direct", "first_touch"} {
			exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, touch_at, channel, source, weight)
				VALUES ($1, $2, $3, 0, $4, $5, $6, 'google', 1) RETURNING event_id::text`, ev, ws, m, session, touch, channel)
		}
	}
	conv(wes, "payment.succeeded", now.Add(-24*time.Hour), 5000, wesVisit, now.Add(-48*time.Hour), "seo")
	conv(cy, "signed_up", now.Add(-30*time.Minute), nil, cyVisit, now.Add(-time.Hour), "paid_ads")
	conv(cy, "payment.succeeded", now.Add(-20*time.Minute), 3000, cyVisit, now.Add(-time.Hour), "paid_ads")
	conv(cy, "payment.succeeded", now.Add(-10*time.Minute), 2000, cyVisit, now.Add(-time.Hour), "paid_ads")

	usd := func(v any) float64 {
		for _, m := range v.([]any) {
			if m.(map[string]any)["currency"] == "USD" {
				return m.(map[string]any)["amount_cents"].(float64)
			}
		}
		return 0
	}
	byChannel := func(path string) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, r := range owner.must(200, "GET", base+path, nil)["data"].([]any) {
			out[r.(map[string]any)["channel"].(string)] = r.(map[string]any)
		}
		return out
	}
	c := byChannel("/attribution/customers")
	if seo := c["seo"]; seo["warm_conversions"] != 1.0 || seo["cold_conversions"] != 0.0 || usd(seo["new_revenue"]) != 5000 || usd(seo["repeat_revenue"]) != 0 {
		t.Fatalf("seo = %v", seo)
	}
	if ads := c["paid_ads"]; ads["warm_conversions"] != 0.0 || ads["cold_conversions"] != 3.0 || usd(ads["new_revenue"]) != 3000 || usd(ads["repeat_revenue"]) != 2000 {
		t.Fatalf("paid ads = %v", ads)
	}
	co := byChannel("/attribution/cohorts")
	if seo := co["seo"]; seo["people"] != 1.0 || seo["customers"] != 1.0 || usd(seo["revenue"]) != 5000 || usd(seo["revenue_per_person"]) != 5000 {
		t.Fatalf("seo cohort = %v", seo)
	}
	if ads := co["paid_ads"]; ads["people"] != 1.0 || usd(ads["revenue"]) != 5000 || len(co) != 2 {
		t.Fatalf("cohorts = %v", co)
	}
	// A cohort is people whose first conversion was in the period.
	old := now.AddDate(0, 0, -40).Format(time.DateOnly)
	if got := owner.must(200, "GET", base+"/attribution/cohorts?from="+old+"&to="+old, nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("empty period = %v", got)
	}
}

// Credit rolls up from people to their company, and a won deal is spread
// across channels by the credit its people earned before it closed.
func TestAttributionAccountsAndDeals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	now := time.Now()
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	northpeak := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	person := func(name string) string {
		id := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', $2) RETURNING id::text`, ws, name)
		owner.must(200, "PATCH", base+"/records/"+id, map[string]any{"attributes": map[string]any{"companies": []string{acme}}})
		return id
	}
	wes, cy := person("Wes"), person("Cy")
	visit := func(rec, channel string, at time.Time) string {
		return exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, channel, source) VALUES ($1, $2::uuid, 'anon-'||$2::text, $3, $4, 'google') RETURNING id::text`,
			ws, rec, at, channel)
	}
	conv := func(rec, name string, at time.Time, value any, session, channel string) {
		t.Helper()
		ev := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', $3, $4) RETURNING id::text`, ws, rec, name, at)
		exec(`INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, value_minor, currency, touches, matched_by)
			VALUES ($1, $2, $3, $4, $5, $6, CASE WHEN $6::bigint IS NULL THEN '' ELSE 'USD' END, 1, 'record') RETURNING event_id::text`, ev, ws, rec, name, at, value)
		exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, touch_at, channel, source, weight)
			VALUES ($1, $2, 'last_non_direct', 0, $3, $4, $5, 'google', 1) RETURNING event_id::text`, ev, ws, session, at.Add(-time.Hour), channel)
	}
	wesVisit, cyVisit := visit(wes, "seo", now.Add(-48*time.Hour)), visit(cy, "paid_ads", now.Add(-3*time.Hour))
	conv(wes, "payment.succeeded", now.Add(-24*time.Hour), 4000, wesVisit, "seo")
	conv(cy, "signed_up", now.Add(-2*time.Hour), nil, cyVisit, "paid_ads")
	conv(cy, "payment.succeeded", now.Add(-90*time.Minute), 3000, cyVisit, "paid_ads")
	conv(cy, "payment.succeeded", now.Add(-80*time.Minute), 3000, cyVisit, "paid_ads")
	// After the deal closed: it doesn't count toward the deal.
	conv(wes, "payment.succeeded", now.Add(10*time.Minute), 1000, wesVisit, "seo")

	won := func(name, company string, cents int) {
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": name,
			"attributes": map[string]any{"stage": "qualified", "associated_company": company}})["id"].(string)
		// A CRM deal's state and amount come from the CRM.
		exec(`UPDATE deals SET state = 'won', closed_at = $2, amount_cents = $3, currency = 'USD' WHERE id = $1 RETURNING id::text`, id, now, cents)
	}
	won("Acme pilot", acme, 100000)
	won("Northpeak pilot", northpeak, 40000)

	usd := func(v any) float64 {
		for _, m := range v.([]any) {
			if m.(map[string]any)["currency"] == "USD" {
				return m.(map[string]any)["amount_cents"].(float64)
			}
		}
		return 0
	}
	to := now.AddDate(0, 0, 1).Format(time.DateOnly)
	accounts := owner.must(200, "GET", base+"/attribution/accounts?to="+to, nil)["data"].([]any)
	if len(accounts) != 2 {
		t.Fatalf("accounts = %v", accounts)
	}
	a := accounts[0].(map[string]any)
	if a["record_id"] != acme || a["name"] != "Acme" || a["people"] != 2.0 || a["conversions"] != 5.0 || usd(a["revenue"]) != 11000 ||
		a["top_channel"] != "paid_ads" || a["deals_won"] != 1.0 || usd(a["deal_value"]) != 100000 {
		t.Fatalf("acme = %v", a)
	}
	if n := accounts[1].(map[string]any); n["record_id"] != northpeak || n["conversions"] != 0.0 || n["deals_won"] != 1.0 || n["top_channel"] != "" {
		t.Fatalf("northpeak = %v", n)
	}

	// Acme's deal: one credited conversion from search and three from ads
	// before it closed, so a quarter and three quarters. Northpeak's has no
	// people.
	d := owner.must(200, "GET", base+"/attribution/deals?to="+to, nil)
	rows := map[string]map[string]any{}
	for _, r := range d["data"].([]any) {
		rows[r.(map[string]any)["channel"].(string)] = r.(map[string]any)
	}
	if ads := rows["paid_ads"]; len(rows) != 2 || ads["deals"] != 0.75 || usd(ads["value"]) != 75000 || ads["label"] == "" {
		t.Fatalf("deals = %v", d)
	}
	if seo := rows["seo"]; seo["deals"] != 0.25 || usd(seo["value"]) != 25000 {
		t.Fatalf("seo = %v", seo)
	}
	if u := d["unattributed"].(map[string]any); u["deals"] != 1.0 || usd(u["value"]) != 40000 {
		t.Fatalf("unattributed = %v", u)
	}
	// Identified visits only: the same here.
	if d := owner.must(200, "GET", base+"/attribution/deals?to="+to+"&match=identified", nil); len(d["data"].([]any)) != 2 {
		t.Fatalf("identified deals = %v", d)
	}
	if a := owner.must(200, "GET", base+"/attribution/accounts?to="+to+"&match=identified", nil)["data"].([]any); len(a) != 2 {
		t.Fatalf("identified accounts = %v", a)
	}
	// The deals are the workspace brand's; the conversions belong to no
	// brand, so under the deals' brand nothing is credited.
	brand := exec(`SELECT brand_id::text FROM records WHERE workspace_id = $1 AND name = 'Acme pilot'`, ws)
	if d := owner.must(200, "GET", base+"/attribution/deals?to="+to+"&brand="+brand, nil); len(d["data"].([]any)) != 0 ||
		d["unattributed"].(map[string]any)["deals"] != 2.0 {
		t.Fatalf("deals by brand = %v", d)
	}
	if d := owner.must(200, "GET", base+"/attribution/deals?to="+to+"&brand=none", nil); d["unattributed"].(map[string]any)["deals"] != 0.0 {
		t.Fatalf("deals with no brand = %v", d)
	}
	// Outside the period: nothing won.
	old := now.AddDate(0, 0, -40).Format(time.DateOnly)
	if d := owner.must(200, "GET", base+"/attribution/deals?from="+old+"&to="+old, nil); len(d["data"].([]any)) != 0 || d["unattributed"].(map[string]any)["deals"] != 0.0 {
		t.Fatalf("empty period = %v", d)
	}
}

// How a visit was identified: the person's own sign-in on the product, or a
// click on an email link. signed_in keeps only the first.
func TestAttributionSignedInOnly(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	rec := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws)
	now := time.Now()
	visit := func(anon, channel, by string, at time.Time) string {
		t.Helper()
		exec(`INSERT INTO site_visitors (workspace_id, anonymous_id, record_id, identified_by, first_seen_at, last_seen_at) VALUES ($1, $2, $3, $4, $5, $5) RETURNING anonymous_id`,
			ws, anon, rec, by, at)
		return exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, channel, source) VALUES ($1, $2, $3, $4, $5, 'google') RETURNING id::text`,
			ws, rec, anon, at, channel)
	}
	laptop := visit("anon-laptop", "seo", "product", now.Add(-3*time.Hour))
	phone := visit("anon-phone", "lifecycle_email", "email_click", now.Add(-2*time.Hour))
	ev := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'signed_up', $3) RETURNING id::text`, ws, rec, now)
	exec(`INSERT INTO attribution_conversions (event_id, workspace_id, record_id, name, occurred_at, touches, matched_by) VALUES ($1, $2, $3, 'signed_up', $4, 2, 'record') RETURNING event_id::text`,
		ev, ws, rec, now)
	for i, s := range []struct{ session, channel string }{{laptop, "seo"}, {phone, "lifecycle_email"}} {
		exec(`INSERT INTO attribution_credits (event_id, workspace_id, model, position, session_id, channel, source, weight)
			VALUES ($1, $2, 'last_non_direct', $3, $4, $5, 'google', 0.5) RETURNING event_id::text`, ev, ws, i, s.session, s.channel)
	}

	channels := func(q string) map[string][2]float64 {
		t.Helper()
		out := map[string][2]float64{}
		for _, r := range owner.must(200, "GET", base+"/attribution/channels"+q, nil)["data"].([]any) {
			m := r.(map[string]any)
			out[m["channel"].(string)] = [2]float64{m["visits"].(float64), m["conversions"].(float64)}
		}
		return out
	}
	if id := channels("?match=identified"); id["seo"] != [2]float64{1, 0.5} || id["lifecycle_email"] != [2]float64{1, 0.5} {
		t.Fatalf("identified = %v", id)
	}
	if own := channels("?match=signed_in"); own["seo"] != [2]float64{1, 0.5} || own["lifecycle_email"] != [2]float64{0, 0} {
		t.Fatalf("signed in = %v", own)
	}
	how := map[string]string{}
	for _, c := range owner.must(200, "GET", base+"/attribution/conversions", nil)["data"].([]any)[0].(map[string]any)["credits"].([]any) {
		m := c.(map[string]any)
		how[m["channel"].(string)] = m["matched_by"].(string) + "/" + m["identified_by"].(string)
	}
	if how["seo"] != "record/product" || how["lifecycle_email"] != "record/email_click" {
		t.Fatalf("credits = %v", how)
	}
	// The other reports take the level too.
	for _, p := range []string{"campaigns", "customers", "accounts", "deals", "ads"} {
		owner.must(200, "GET", base+"/attribution/"+p+"?match=signed_in", nil)
	}
}
