package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"testing"

	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Rule-based paths on a redirect link (plans/ad-tracker.md, T4b): the first
// path whose conditions all match and that has an open destination takes
// the click, then the default destinations, then the link's own page.
func TestLinkPaths(t *testing.T) {
	geoiptest.Use(t)
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Rules test", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	links := base + "/tracking-campaigns/" + c["code"].(string) + "/links"
	l := owner.must(201, "POST", links, map[string]any{"url": "https://northpeak.example/home"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	linkPath := links + "/" + l["id"].(string)
	owner.must(200, "PUT", linkPath+"/destinations", map[string]any{"destinations": []any{map[string]any{"url": "https://northpeak.example/default"}}})

	dest := func(page string, more ...map[string]any) []any {
		d := map[string]any{"url": "https://northpeak.example/" + page}
		for _, m := range more {
			for k, v := range m {
				d[k] = v
			}
		}
		return []any{d}
	}
	cond := func(field, op string, values ...string) map[string]any {
		return map[string]any{"field": field, "op": op, "values": values}
	}
	paths := []any{
		map[string]any{"name": "Off", "active": false, "conditions": []any{cond("country", "is", "us")}, "destinations": dest("off")},
		map[string]any{"name": "US mobile", "conditions": []any{cond("country", "is", "US", "CA"), cond("device", "is", "Mobile")}, "destinations": dest("us-mobile")},
		map[string]any{"name": "Lab", "conditions": []any{cond("ip", "is", "192.0.2.0/24")}, "destinations": dest("lab")},
		map[string]any{"name": "Zone 9", "conditions": []any{cond("field1", "is", "Zone-9"), cond("country", "is_not", "FR")}, "destinations": dest("zone")},
		map[string]any{"name": "France, stopped", "conditions": []any{cond("country", "is", "FR")}, "destinations": dest("fr", map[string]any{"daily_click_cap": 0})},
		map[string]any{"name": "Back again", "conditions": []any{cond("visitor", "is", "returning")}, "destinations": dest("again")},
	}
	got := owner.must(200, "PUT", linkPath+"/paths", map[string]any{"paths": paths})
	ps := got["paths"].([]any)
	if len(ps) != 6 || ps[1].(map[string]any)["conditions"].([]any)[0].(map[string]any)["values"].([]any)[0] != "US" ||
		ps[1].(map[string]any)["conditions"].([]any)[1].(map[string]any)["values"].([]any)[0] != "mobile" || ps[0].(map[string]any)["active"] != false {
		t.Fatalf("paths: %v", got)
	}

	const (
		iphone = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
		mac    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"
	)
	follow := func(addr, ua, query string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code+"?"+query, nil)
		req.Host = host
		req.Header.Set("User-Agent", ua)
		req.RemoteAddr = addr
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("redirect = %d", rec.Code)
		}
		loc, _ := url.Parse(rec.Header().Get("Location"))
		return loc.Path
	}
	springfield := geoiptest.SpringfieldIP + ":51000"
	lyon := "[" + geoiptest.LyonIP + "]:51000"
	for i, step := range []struct{ addr, ua, query, want string }{
		{springfield, iphone, "", "/us-mobile"},             // country and device; the inactive path is skipped
		{springfield, mac, "", "/again"},                    // desktop: the same visitor, back within a day
		{"192.0.2.7:51000", mac, "", "/lab"},                // the IP range
		{"198.51.100.4:51000", mac, "sub1=zone-9", "/zone"}, // a custom field, case aside, and a negative condition
		{lyon, mac, "", "/default"},                         // France's path is stopped; a first visit; the default destination
		{lyon, mac, "", "/again"},                           // and back again
	} {
		if got := follow(step.addr, step.ua, step.query); got != step.want {
			t.Fatalf("click %d went to %s, want %s", i, got, step.want)
		}
	}
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatal(err)
	}
	var onPaths int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks k JOIN tracking_link_paths p ON p.id = k.path_id
		WHERE k.workspace_id = $1`, ws).Scan(&onPaths); err != nil || onPaths != 5 {
		t.Fatalf("clicks on paths = %d, %v", onPaths, err)
	}

	// Keeping a path by its id, and taking the rest off with their
	// destinations.
	keep := ps[2].(map[string]any)
	got = owner.must(200, "PUT", linkPath+"/paths", map[string]any{"paths": []any{
		map[string]any{"id": keep["id"], "name": "Lab", "conditions": []any{cond("ip", "is", "192.0.2.7")}, "destinations": []any{
			map[string]any{"id": keep["destinations"].([]any)[0].(map[string]any)["id"], "url": "https://northpeak.example/lab"}}},
	}})
	if len(got["paths"].([]any)) != 1 || got["paths"].([]any)[0].(map[string]any)["id"] != keep["id"] {
		t.Fatalf("kept: %v", got)
	}
	var removedPaths, removedDests int
	if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM tracking_link_paths WHERE workspace_id = $1 AND removed_at IS NOT NULL),
		(SELECT count(*) FROM tracking_link_destinations WHERE workspace_id = $1 AND path_id IS NOT NULL AND removed_at IS NOT NULL)`, ws).
		Scan(&removedPaths, &removedDests); err != nil || removedPaths != 5 || removedDests != 5 {
		t.Fatalf("removed %d paths, %d destinations, %v", removedPaths, removedDests, err)
	}
	if got := follow("192.0.2.8:51000", mac, ""); got != "/default" {
		t.Fatalf("outside the new range went to %s", got)
	}
	// The default destinations are untouched.
	if p := owner.must(200, "GET", linkPath+"/paths", nil)["paths"].([]any); len(p) != 1 || p[0].(map[string]any)["destinations"].([]any)[0].(map[string]any)["clicks_today"] != float64(1) {
		t.Fatalf("paths read back: %v", p)
	}
	if d := owner.must(200, "GET", linkPath+"/destinations", nil)["destinations"].([]any); len(d) != 1 {
		t.Fatalf("default destinations: %v", d)
	}

	// Bad paths.
	for name, p := range map[string]map[string]any{
		"field":          {"conditions": []any{cond("planet", "is", "mars")}, "destinations": dest("x")},
		"op":             {"conditions": []any{cond("country", "equals", "US")}, "destinations": dest("x")},
		"country":        {"conditions": []any{cond("country", "is", "USA")}, "destinations": dest("x")},
		"ip":             {"conditions": []any{cond("ip", "is", "300.1.1.1")}, "destinations": dest("x")},
		"hour":           {"conditions": []any{cond("hour", "is", "24")}, "destinations": dest("x")},
		"visitor":        {"conditions": []any{cond("visitor", "is", "loyal")}, "destinations": dest("x")},
		"no conditions":  {"conditions": []any{}, "destinations": dest("x")},
		"no values":      {"conditions": []any{cond("country", "is")}, "destinations": dest("x")},
		"no destination": {"conditions": []any{cond("country", "is", "US")}, "destinations": []any{}},
		"bad page":       {"conditions": []any{cond("country", "is", "US")}, "destinations": []any{map[string]any{"url": "nope"}}},
	} {
		if status, out := owner.do("PUT", linkPath+"/paths", map[string]any{"paths": []any{p}}); status != 400 {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	var eleven []any
	for range 11 {
		eleven = append(eleven, map[string]any{"conditions": []any{cond("country", "is", "US")}, "destinations": dest("x")})
	}
	owner.must(400, "PUT", linkPath+"/paths", map[string]any{"paths": eleven})

	found := false
	for _, ev := range events() {
		found = found || ev.Name == "tracking_link.paths_updated"
	}
	if !found {
		t.Error("no tracking_link.paths_updated event")
	}
}
