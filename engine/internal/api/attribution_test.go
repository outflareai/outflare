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
	if s["model"] != "last_non_direct" || s["lookback_days"] != float64(90) || len(s["conversion_events"].([]any)) != 2 {
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
