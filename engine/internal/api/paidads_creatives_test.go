package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/paidads"
)

// Creative results: each ad's steps graded against the brand's usual on
// the network, and one verdict from the grades and the target.
func TestPaidAdsCreativeResults(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	brand := exec(`SELECT outflare_first_brand($1)::text`, ws)
	base := "/v1/brands/" + brand + "/ads"
	now := time.Now().UTC().Truncate(time.Second)
	owner.must(200, "PUT", base+"/economics", map[string]any{"currency": "USD", "target_cost_per_payer_micros": 50_000_000})

	integ := exec(`INSERT INTO integrations (brand_id, workspace_id, connector, name, credentials, status) VALUES ($1, $2, 'meta_ads', 'Meta Ads', '\x00', 'connected') RETURNING id::text`, brand, ws)
	acct := exec(`INSERT INTO ad_accounts (brand_id, workspace_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'meta_ads', 'act_1', 'Northpeak', 'USD') RETURNING id::text`, brand, ws, integ)
	camp := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, external_id, name, status) VALUES ($1, $2, $3, 'c1', 'Prospecting', 'active') RETURNING id::text`, brand, ws, acct)
	set := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status) VALUES ($1, $2, $3, 'ad_group', $4, 's1', 'Broad', 'active') RETURNING id::text`, brand, ws, acct, camp)
	n := 0
	// ad makes an ad with yesterday's delivery, and people who clicked it
	// two days ago, signed up and (the first paid of them) paid $100.
	ad := func(ext, name, details string, spend, impressions, clicks, views, through int64, signups, paid int) string {
		t.Helper()
		id := exec(`INSERT INTO ad_entities (brand_id, workspace_id, ad_account_id, level, parent_id, external_id, name, status, details) VALUES ($1, $2, $3, 'ad', $4, $5, $6, 'active', $7) RETURNING id::text`, brand, ws, acct, set, ext, name, details)
		exec(`INSERT INTO ad_metrics_daily (ad_entity_id, day, workspace_id, currency, spend_micros, impressions, clicks, video_views, video_thruplays, brand_id) VALUES ($1, current_date - 1, $2, 'USD', $3, $4, $5, NULLIF($6, 0), NULLIF($7, 0), $8) RETURNING 'x'`, id, ws, spend, impressions, clicks, views, through, brand)
		for i := range signups {
			n++
			rec := exec(`INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', $3) RETURNING id::text`, brand, ws, fmt.Sprintf("Person %d", n))
			exec(`INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, last_seen_at, utm_source, channel, click_ids, ad) VALUES ($1, $2, $3, $4, $5, $5, 'facebook', 'paid_ads', '{"fbclid": "fbtest"}', $6::jsonb) RETURNING id::text`,
				brand, ws, rec, fmt.Sprintf("anon-%d", n), now.Add(-48*time.Hour), fmt.Sprintf(`{"campaign": "c1", "adset": "s1", "ad": %q}`, ext))
			exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'signed_up', $4, gen_random_uuid()::text) RETURNING id::text`, brand, ws, rec, now.Add(-36*time.Hour))
			if i < paid {
				exec(`INSERT INTO customer_events (brand_id, workspace_id, record_id, kind, name, properties, occurred_at, message_id) VALUES ($1, $2, $3, 'track', 'payment.succeeded', $4::jsonb, $5, gen_random_uuid()::text) RETURNING id::text`,
					brand, ws, rec, fmt.Sprintf(`{"amount_minor": 10000, "currency": "USD", "payment_id": "p%d"}`, n), now.Add(-30*time.Hour))
			}
		}
		return id
	}
	// The map ad does well at every step and under the target.
	winner := ad("a1", "Countries map", `{"type": "image", "headline": "190+ countries. One API.", "thumbnail_url": "https://cdn.example.com/a1.png"}`, 100_000_000, 20000, 600, 0, 0, 60, 6)
	// The video loses people in its opening but signs up those who click.
	video := ad("a2", "First request video", `{"type": "video"}`, 100_000_000, 20000, 200, 2000, 900, 20, 2)
	// The price table gets clicks that don't sign up, and no payer for
	// twice the target.
	loser := ad("a3", "Price table", `{"type": "image"}`, 120_000_000, 20000, 400, 0, 0, 2, 0)
	// A new ad has hardly been seen.
	fresh := ad("a4", "Just launched", `{}`, 5_000_000, 300, 4, 0, 0, 0, 0)
	if _, err := (&paidads.Ledger{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Millisecond}).Tick(ctx); err != nil {
		t.Fatal(err)
	}

	got := owner.must(200, "GET", base+"/creatives/results", nil)
	sum := got["summary"].(map[string]any)
	if sum["total"] != 4.0 || sum["scale"] != 1.0 || sum["fix"] != 1.0 || sum["stop"] != 1.0 || sum["too_early"] != 1.0 || sum["keep"] != 0.0 {
		t.Fatalf("summary: %v", sum)
	}
	by := map[string]map[string]any{}
	for _, r := range got["data"].([]any) {
		by[r.(map[string]any)["id"].(string)] = r.(map[string]any)
	}
	step := func(id, name string) map[string]any {
		t.Helper()
		for _, s := range by[id]["steps"].([]any) {
			if s.(map[string]any)["id"] == name {
				return s.(map[string]any)
			}
		}
		t.Fatalf("%s has no step %s: %v", id, name, by[id]["steps"])
		return nil
	}
	w := by[winner]
	if w["verdict"] != "scale" || w["weakest_step"] != "" || w["campaign_name"] != "Prospecting" || w["headline"] != "190+ countries. One API." ||
		w["thumbnail_url"] != "https://cdn.example.com/a1.png" || w["video"] != false || w["signups"] != 60.0 || w["new_payers"] != 6.0 || len(w["steps"].([]any)) != 3 {
		t.Fatalf("winner: %v", w)
	}
	// 600 clicks of 20,000 against 1,204 of 60,300 on the network: A.
	if s := step(winner, "click"); s["grade"] != "A" || s["count"] != 600.0 || s["base"] != 20000.0 || s["rate"] != 0.03 {
		t.Fatalf("winner's click: %v", s)
	}
	v := by[video]
	if v["verdict"] != "fix" || v["weakest_step"] != "click" || v["video"] != true || len(v["steps"].([]any)) != 5 {
		t.Fatalf("video: %v", v)
	}
	if step(video, "hook")["grade"] != "B" || step(video, "hold")["grade"] != "B" || step(video, "click")["grade"] != "D" || step(video, "sign_up")["grade"] != "A" {
		t.Fatalf("video's steps: %v", v["steps"])
	}
	l := by[loser]
	if l["verdict"] != "stop" || l["weakest_step"] != "sign_up" || step(loser, "sign_up")["grade"] != "D" || step(loser, "pay")["grade"] != "" {
		t.Fatalf("loser: %v", l)
	}
	if f := by[fresh]; f["verdict"] != "too_early" || step(fresh, "click")["grade"] != "" {
		t.Fatalf("fresh: %v", f)
	}
	// Most spend first.
	if first := got["data"].([]any)[0].(map[string]any)["id"]; first != loser {
		t.Fatalf("first is %v", first)
	}
	// Another network has none of them, and a bad date is refused.
	if other := owner.must(200, "GET", base+"/creatives/results?connector=google_ads", nil); len(other["data"].([]any)) != 0 {
		t.Fatalf("google: %v", other)
	}
	owner.must(400, "GET", base+"/creatives/results?from=yesterday", nil)
}
