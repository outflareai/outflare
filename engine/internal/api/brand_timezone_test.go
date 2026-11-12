package api_test

import (
	"context"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"
)

// A brand's reporting time zone (plans/ad-tracker.md, B4a): reports bucket
// their days in it, ?timezone= overrides it, and tracker caps count the
// brand's day.
func TestBrandReportingTimezone(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	bpath := "/v1/workspaces/" + ws + "/brands/" + brand
	if b := owner.must(200, "GET", bpath, nil); b["timezone"] != "UTC" {
		t.Fatalf("default time zone: %v", b["timezone"])
	}
	for _, bad := range []string{"", "Local", "Mars/Olympus_Mons"} {
		owner.must(400, "PATCH", bpath, map[string]any{"timezone": bad})
	}
	if b := owner.must(200, "PATCH", bpath, map[string]any{"timezone": " Europe/Berlin "}); b["timezone"] != "Europe/Berlin" {
		t.Fatalf("time zone: %v", b["timezone"])
	}
	var fields []string
	if err := e.pool.QueryRow(ctx, `SELECT ARRAY(SELECT jsonb_array_elements_text(data->'fields')) FROM audit_events
		WHERE workspace_id = $1 AND action = 'brand.updated' ORDER BY created_at DESC LIMIT 1`, ws).Scan(&fields); err != nil ||
		len(fields) != 1 || fields[0] != "timezone" {
		t.Fatalf("brand.updated fields: %v, %v", fields, err)
	}

	// 23:30 UTC on 10 October is 01:30 on 11 October in Berlin.
	at := time.Date(2026, 10, 10, 23, 30, 0, 0, time.UTC)
	var rec string
	if err := e.pool.QueryRow(ctx, `INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws).Scan(&rec); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `WITH ev AS (INSERT INTO customer_events (workspace_id, kind, name, anonymous_id, properties, context, occurred_at)
			VALUES ($1, 'track', 'signed_up', 'ana', '{}', '{}', $3) RETURNING id)
		INSERT INTO attribution_conversions (event_id, workspace_id, brand_id, name, occurred_at) SELECT id, $1, $2, 'signed_up', $3 FROM ev`, ws, brand, at); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, clicked_at, window_days)
		VALUES ($1, $2, $3, 'google_ads', $4, 30)`, ws, brand, rec, at); err != nil {
		t.Fatal(err)
	}
	count := func(path string) int {
		t.Helper()
		return len(owner.must(200, "GET", path, nil)["data"].([]any))
	}
	base := "/v1/brands/" + brand
	for _, c := range []struct {
		query string
		want  int
	}{
		{"from=2026-10-11&to=2026-10-11", 1},
		{"from=2026-10-10&to=2026-10-10", 0},
		{"from=2026-10-10&to=2026-10-10&timezone=UTC", 1},
		{"from=2026-10-11&to=2026-10-11&timezone=UTC", 0},
		{"from=2026-10-10&to=2026-10-10&timezone=America/New_York", 1},
	} {
		if n := count(base + "/attribution/conversions?" + c.query); n != c.want {
			t.Errorf("attribution conversions %s: %d, want %d", c.query, n, c.want)
		}
		if n := count(base + "/ads/acquisitions?" + c.query); n != c.want {
			t.Errorf("ad acquisitions %s: %d, want %d", c.query, n, c.want)
		}
	}
	owner.must(400, "GET", base+"/attribution/conversions?timezone=Nowhere/Land", nil)
	owner.must(400, "GET", base+"/ads/acquisitions?timezone=Nowhere/Land", nil)

	// A cap counts the brand's day: 14 hours ahead of UTC, the day is
	// (nearly always) not UTC's.
	owner.must(200, "PATCH", bpath, map[string]any{"timezone": "Pacific/Kiritimati"})
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	cpath := base + "/tracking-campaigns/" + c["code"].(string)
	link := owner.must(201, "POST", cpath+"/links", map[string]any{"url": "https://northpeak.example/own"})
	owner.must(200, "PUT", cpath+"/links/"+link["id"].(string)+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"url": "https://offers.northpeak.example/a", "daily_click_cap": 5},
	}})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	req := httptest.NewRequest("GET", "/l/"+code, nil)
	req.Host = host
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
	e.h.ServeHTTP(httptest.NewRecorder(), req)
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var day, want string
	if err := e.pool.QueryRow(ctx, `SELECT day::text, (now() AT TIME ZONE 'Pacific/Kiritimati')::date::text FROM tracking_destination_days
		WHERE workspace_id = $1`, ws).Scan(&day, &want); err != nil || day != want {
		t.Fatalf("cap day %s, want the brand's %s (%v)", day, want, err)
	}
	d := owner.must(200, "GET", cpath+"/links/"+link["id"].(string)+"/destinations", nil)["destinations"].([]any)[0].(map[string]any)
	if d["clicks_today"] != float64(1) {
		t.Fatalf("clicks today: %v", d)
	}
}
