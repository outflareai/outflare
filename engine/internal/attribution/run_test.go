package attribution

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/db"
	"github.com/outflareai/outflare/engine/internal/track"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if _, err := db.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestTickCreditsConversions(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	q := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	ws := q(`INSERT INTO workspaces (name, slug) VALUES ('Attribution', 'at-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING id::text`)
	q(`INSERT INTO brands (workspace_id, name, handle, website) VALUES ($1, 'Northpeak', 'northpeak', 'https://northpeak.example') RETURNING id::text`, ws)
	rec := q(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws)
	camp := q(`INSERT INTO tracking_campaigns (workspace_id, code, slug, name, channel, source, medium)
		VALUES ($1, substr(md5(random()::text), 1, 10), 'spring-launch', 'Spring launch', 'organic_social', 'linkedin', 'social') RETURNING id::text`, ws)
	code := q(`SELECT upper(code) FROM tracking_campaigns WHERE id = $1`, camp)

	now := time.Now()
	day := 24 * time.Hour
	visit := func(at time.Time, cols string, vals ...any) {
		t.Helper()
		args := append([]any{ws, rec, at}, vals...)
		ph := ""
		for i := range vals {
			ph += fmt.Sprintf(", $%d", i+4)
		}
		extra := ""
		if cols != "" {
			extra = ", " + cols
		}
		q(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, landing_url`+extra+`)
			VALUES ($1, $2, 'anon-1', $3, 'https://northpeak.example/pricing'`+ph+`) RETURNING id::text`, args...)
	}
	visit(now.Add(-30*day), "ofc", code)                                          // Outflare link: organic social
	visit(now.Add(-10*day), "referrer_host", "www.google.com")                    // search
	visit(now.Add(-2*day), "click_ids", map[string]string{"gclid": "test-gclid"}) // paid
	visit(now.Add(-1*day), "")                                                    // direct
	visit(now.Add(-200*day), "utm_source, utm_medium", "newsletter", "email")     // outside the lookback

	payment := q(`INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, occurred_at)
		VALUES ($1, $2, 'track', 'payment.succeeded', '{"amount_minor": 10000, "currency": "usd", "payment_id": "ch_test_1"}', $3) RETURNING id::text`, ws, rec, now.Add(-time.Hour))
	refund := q(`INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, occurred_at)
		VALUES ($1, $2, 'track', 'payment.refunded', '{"amount_minor": 2500, "currency": "usd", "payment_id": "ch_test_1"}', $3) RETURNING id::text`, ws, rec, now.Add(-30*time.Minute))
	lonely := q(`INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, occurred_at)
		VALUES ($1, 'track', 'signed_up', 'anon-nobody', $2) RETURNING id::text`, ws, now.Add(-20*time.Minute))

	events := track.Capture(t)
	r := &Runner{Pool: pool, Log: slog.New(slog.DiscardHandler), Settle: time.Minute}
	if _, _, err := r.Tick(ctx); err != nil {
		t.Fatal(err)
	}

	// Every visit has a channel.
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM site_sessions WHERE workspace_id = $1 AND channel = ''`, ws).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d visits not classified", pending)
	}
	var social, campID string
	if err := pool.QueryRow(ctx, `SELECT channel, tracking_campaign_id::text FROM site_sessions WHERE workspace_id = $1 AND ofc = $2`, ws, code).Scan(&social, &campID); err != nil {
		t.Fatal(err)
	}
	if social != "organic_social" || campID != camp {
		t.Fatalf("ofc visit = %s %s, want organic_social %s", social, campID, camp)
	}

	credits := func(event, model string) map[string]float64 {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT channel, weight::float8 FROM attribution_credits WHERE event_id = $1 AND model = $2`, event, model)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]float64{}
		for rows.Next() {
			var ch string
			var w float64
			if err := rows.Scan(&ch, &w); err != nil {
				t.Fatal(err)
			}
			got[ch] += w
		}
		return got
	}
	near := func(a, b float64) bool { return math.Abs(a-b) < 1e-5 }

	// Last non-direct skips the direct visit and lands on the ad click.
	if got := credits(payment, LastNonDirect); len(got) != 1 || got["paid_ads"] != 1 {
		t.Fatalf("last non-direct = %v", got)
	}
	if got := credits(payment, FirstTouch); len(got) != 1 || got["organic_social"] != 1 {
		t.Fatalf("first touch = %v", got)
	}
	if got := credits(payment, Linear); len(got) != 3 || !near(got["seo"], 1.0/3) {
		t.Fatalf("linear = %v", got)
	}
	if got := credits(payment, Position); !near(got["organic_social"], 0.4) || !near(got["seo"], 0.2) || !near(got["paid_ads"], 0.4) {
		t.Fatalf("position = %v", got)
	}
	if got := credits(payment, TimeDecay); got["paid_ads"] <= got["seo"] || got["seo"] <= got["organic_social"] {
		t.Fatalf("time decay = %v", got)
	}

	// The refund nets against the same channels; the payment's value and
	// matching are stored.
	var value int64
	var cur, matched string
	var touches int
	if err := pool.QueryRow(ctx, `SELECT value_minor, currency, matched_by, touches FROM attribution_conversions WHERE event_id = $1`, payment).
		Scan(&value, &cur, &matched, &touches); err != nil {
		t.Fatal(err)
	}
	if value != 10000 || cur != "USD" || matched != "record" || touches != 4 {
		t.Fatalf("payment = %d %s %s %d", value, cur, matched, touches)
	}
	if err := pool.QueryRow(ctx, `SELECT value_minor FROM attribution_conversions WHERE event_id = $1`, refund).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != -2500 {
		t.Fatalf("refund value = %d", value)
	}
	if got := credits(refund, LastNonDirect); got["paid_ads"] != 1 {
		t.Fatalf("refund credits = %v", got)
	}

	// A conversion with no visits goes to direct.
	if got := credits(lonely, LastNonDirect); got[Direct] != 1 {
		t.Fatalf("no-visit conversion = %v", got)
	}

	// A second tick credits nothing again.
	if _, n, err := r.Tick(ctx); err != nil || n != 0 {
		t.Fatalf("second tick credited %d (%v)", n, err)
	}
	var seen bool
	for _, e := range events() {
		if e.Name == "attribution.credited" && e.WorkspaceID == ws {
			seen = e.Props["conversions"] == 3 && e.Props["refunds"] == 1
		}
	}
	if !seen {
		t.Fatal("no attribution.credited event with 3 conversions and 1 refund")
	}
}
