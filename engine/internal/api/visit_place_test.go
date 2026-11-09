package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/geoip/geoiptest"
)

// Visits and redirect clicks keep where they came from (region, city, the
// network) and the device's maker, model and versions, worked out when the
// request arrives. The address and user agent themselves aren't kept.
func TestVisitPlaceAndDevice(t *testing.T) {
	geoiptest.Use(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)

	const (
		samsungUA = "Mozilla/5.0 (Linux; Android 14; SM-S918B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36"
		iphoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1"
	)
	n := 0
	visit := func(ip, ua, tz, locale string, header map[string]string) string {
		t.Helper()
		n++
		anon := "visitor-place-" + string(rune('a'+n))
		ev := map[string]any{"type": "page", "anonymousId": anon, "messageId": "m-" + anon, "timestamp": time.Now().UTC().Format(time.RFC3339),
			"context": map[string]any{"timezone": tz, "locale": locale, "page": map[string]any{"url": "https://northpeak.example/pricing", "path": "/pricing"}}}
		b, _ := json.Marshal(map[string]any{"key": key, "batch": []any{ev}})
		req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", ua)
		req.Header.Set("Origin", "https://northpeak.example")
		for k, v := range header {
			req.Header.Set(k, v)
		}
		req.RemoteAddr = ip + ":51000"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
		return anon
	}
	type place struct {
		country, source, region, city, isp                                     string
		asn                                                                    int64
		device, brand, model, os, osVersion, browser, browserVersion, language string
	}
	session := func(anon string) place {
		t.Helper()
		var p place
		if err := e.pool.QueryRow(ctx, `SELECT country, country_source, region, city, isp, asn, device, device_brand, device_model, os, os_version,
				browser, browser_version, language
			FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = $2`, ws, anon).
			Scan(&p.country, &p.source, &p.region, &p.city, &p.isp, &p.asn, &p.device, &p.brand, &p.model, &p.os, &p.osVersion,
				&p.browser, &p.browserVersion, &p.language); err != nil {
			t.Fatal(err)
		}
		return p
	}

	// A visitor in Springfield on a Samsung phone.
	got := session(visit(geoiptest.SpringfieldIP, samsungUA, "America/Chicago", "de-DE", nil))
	want := place{"US", "ip", "Illinois", "Springfield", "Northpeak Fiber", 64500,
		"mobile", "Samsung", "SM-S918B", "android", "14", "samsung", "25", "de"}
	if got != want {
		t.Fatalf("Springfield visit:\n got %+v\nwant %+v", got, want)
	}

	// A data centre's address has a network and no city; the country comes
	// from the browser's time zone, as before.
	got = session(visit(geoiptest.HostIP, iphoneUA, "Europe/Paris", "fr-FR", nil))
	if got.country != "FR" || got.source != "timezone" || got.city != "" || got.region != "" || got.isp != "Acme Cloud Hosting" || got.asn != 64502 ||
		got.brand != "Apple" || got.model != "iPhone" || got.osVersion != "17.5" || got.language != "fr" {
		t.Fatalf("data centre visit: %+v", got)
	}

	// Through the site's own domain without the proxy secret, the address
	// is the proxy's, so its city and network aren't the visitor's.
	got = session(visit(geoiptest.SpringfieldIP, samsungUA, "Europe/Berlin", "", map[string]string{"Sec-Fetch-Site": "same-origin"}))
	if got.city != "" || got.isp != "" || got.asn != 0 || got.country != "DE" || got.source != "timezone" || got.language != "" {
		t.Fatalf("own domain without a secret: %+v", got)
	}

	// Query reads the new columns.
	out := owner.must(200, "POST", base+"/query", map[string]any{"sql": "select city, isp, asn, device_brand, language from visits where city <> '' order by city"})
	rows := queryRows(t, out)
	if len(rows) != 1 || rows[0][0] != "Springfield" || rows[0][1] != "Northpeak Fiber" || rows[0][2] != float64(64500) || rows[0][3] != "Samsung" || rows[0][4] != "de" {
		t.Fatalf("query: %v", out)
	}

	// Insights break visits down by network.
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Format(time.DateOnly)
	funnel := owner.must(200, "POST", "/v1/brands/"+brand+"/insights/funnel", map[string]any{
		"date_range": map[string]any{"from": day, "to": day},
		"steps": []map[string]any{
			{"kind": "visit", "filters": []map[string]any{{"property": "city", "op": "eq", "value": "Springfield"}}},
			{"kind": "visit", "within": map[string]any{"value": 1, "unit": "day"}},
		},
		"breakdown": "isp",
	})
	groups, _ := funnel["breakdown"].([]any)
	if len(groups) != 1 || groups[0].(map[string]any)["value"] != "Northpeak Fiber" {
		t.Fatalf("funnel by network: %v", funnel)
	}

	// A redirect click keeps the same details.
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Podcast autumn", "channel": "referral", "source": "podcast", "landing_url": "https://northpeak.example/listen",
	})
	link := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"content": "show-notes", "redirect": true})
	m := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))
	if m == nil {
		t.Fatalf("link = %v", link)
	}
	req := httptest.NewRequest("GET", "/l/"+m[1], nil)
	req.Header.Set("User-Agent", iphoneUA)
	req.Header.Set("Accept-Language", "fr-FR,fr;q=0.9,en;q=0.8")
	req.RemoteAddr = "[" + geoiptest.LyonIP + "]:51000"
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("redirect = %d", rec.Code)
	}
	var p place
	if err := e.pool.QueryRow(ctx, `SELECT country, region, city, isp, asn, device, device_brand, device_model, os, os_version, browser, browser_version, language
		FROM tracking_link_clicks WHERE workspace_id = $1`, ws).
		Scan(&p.country, &p.region, &p.city, &p.isp, &p.asn, &p.device, &p.brand, &p.model, &p.os, &p.osVersion, &p.browser, &p.browserVersion, &p.language); err != nil {
		t.Fatal(err)
	}
	want = place{country: "FR", region: "Auvergne-Rhone-Alpes", city: "Lyon", isp: "Acme Mobile", asn: 64501,
		device: "mobile", brand: "Apple", model: "iPhone", os: "ios", osVersion: "17.5", browser: "safari", browserVersion: "17", language: "fr"}
	if p != want {
		t.Fatalf("redirect click:\n got %+v\nwant %+v", p, want)
	}
}
