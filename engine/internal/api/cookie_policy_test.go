package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/cookies"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// northpeakSite is a small website with a server cookie, Google's tags,
// a Meta snippet and a script nobody knows.
func northpeakSite(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/robots.txt":
			io.WriteString(w, "User-agent: *\nDisallow: /admin\n")
		case "/":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "s-1", Path: "/", HttpOnly: true})
			http.SetCookie(w, &http.Cookie{Name: "_ga", Value: "GA1.1.1", Path: "/", MaxAge: 63072000})
			w.Header().Set("Content-Type", "text/html")
			io.WriteString(w, `<html><head>
<script async src="https://www.googletagmanager.com/gtm.js?id=GTM-TEST"></script>
<script>!function(){var t=document.createElement('script');t.src='https://connect.facebook.net/en_US/fbevents.js'}();</script>
<script src="https://cdn.tracker.example/t.js"></script>
</head><body><a href="/pricing">Pricing</a></body></html>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCookieScans works cookie scan jobs until the test ends.
func (e *env) runCookieScans(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	(&cookies.Service{Pool: e.pool, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), AllowPrivate: true,
		Wait: func(context.Context, time.Duration) error { return nil }}).Register(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

func TestCookiePolicyKeepsItselfUpToDate(t *testing.T) {
	e := freshEnv(t)
	e.runCookieScans(t)
	site := northpeakSite(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{site.URL}})
	key, id := k["key"].(string), k["id"].(string)
	path := base + "/site-keys/" + id

	if out := owner.must(200, "GET", path+"/cookies", nil); len(out["data"].([]any)) != 0 || out["scan"] != nil || out["policy"] != nil {
		t.Fatalf("before a scan: %v", out)
	}
	owner.must(404, "GET", path+"/cookie-policy", nil)

	scan := owner.must(201, "POST", path+"/cookie-scans", nil)
	if again := owner.must(200, "POST", path+"/cookie-scans", nil); again["id"] != scan["id"] {
		t.Fatalf("a second scan while one waits: %v", again)
	}
	out := waitFor(t, owner, path+"/cookies", func(m map[string]any) bool {
		s, _ := m["scan"].(map[string]any)
		return s != nil && (s["status"] == "done" || s["status"] == "failed")
	})
	if s := out["scan"].(map[string]any); s["status"] != "done" || s["pages"] != 1.0 || s["trigger"] != "person" {
		t.Fatalf("scan: %v", s)
	}
	items := map[string]map[string]any{}
	for _, x := range out["data"].([]any) {
		c := x.(map[string]any)
		items[c["kind"].(string)+" "+c["name"].(string)] = c
	}
	want := map[string]string{
		"cookie sid":                             "unknown",
		"cookie _ga":                             "analytics",
		"script www.googletagmanager.com/gtm.js": "necessary",
		"script connect.facebook.net/en_US":      "marketing",
		"script cdn.tracker.example/t.js":        "unknown",
	}
	for name, cat := range want {
		if items[name] == nil || items[name]["category"] != cat {
			t.Errorf("%s: %v", name, items[name])
		}
	}
	if len(items) != len(want) || items["cookie _ga"]["retention"] != "2 years" || items["cookie _ga"]["vendor_name"] != "Google Analytics" {
		t.Fatalf("found: %v", items)
	}
	counts := out["counts"].(map[string]any)
	if counts["to_review"] != 2.0 || counts["listed"] != 3.0 || out["policy"].(map[string]any)["version"] != 1.0 {
		t.Fatalf("counts and policy: %v %v", counts, out["policy"])
	}

	// The policy lists what the catalogue knows; what it doesn't waits.
	p := owner.must(200, "GET", path+"/cookie-policy", nil)
	md := p["content"].(string)
	if p["version"] != 1.0 || !strings.Contains(md, "| `_ga` | Google Analytics (Google) | Cookie |") || strings.Contains(md, "sid") || strings.Contains(md, "tracker") {
		t.Fatalf("policy v1:\n%s", md)
	}

	// A person says what sid is; the policy follows, without asking anyone
	// again, since necessary cookies need no consent.
	owner.must(400, "PATCH", path+"/cookies/"+items["cookie sid"]["id"].(string), map[string]any{"category": "unknown"})
	owner.must(400, "PATCH", path+"/cookies/"+items["script cdn.tracker.example/t.js"]["id"].(string), map[string]any{"status": "listed"})
	reader := agentToken(t, owner, base, "Reader", "customers.read")
	reader.must(403, "PATCH", path+"/cookies/"+items["cookie sid"]["id"].(string), map[string]any{"category": "necessary"})
	c := owner.must(200, "PATCH", path+"/cookies/"+items["cookie sid"]["id"].(string), map[string]any{"category": "necessary", "purpose": "Keeps you signed in."})
	if c["status"] != "listed" || c["reviewed_at"] == nil {
		t.Fatalf("classified: %v", c)
	}
	owner.must(200, "PATCH", path+"/cookies/"+items["script cdn.tracker.example/t.js"]["id"].(string), map[string]any{"status": "ignored"})
	p = owner.must(200, "GET", path+"/cookie-policy?format=text", nil)
	if p["version"] != 2.0 || !strings.Contains(p["content"].(string), "- sid (cookie, kept for session): Keeps you signed in.") {
		t.Fatalf("policy v2: %v", p["content"])
	}

	// With the banner on, a browser reports TikTok's pixel before the
	// visitor allowed marketing: a new vendor that needs consent, so the
	// policy changes and visitors are asked again.
	owner.must(200, "PUT", path+"/consent-banner", map[string]any{"enabled": true})
	report := func(origin string, body map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/v1/site/cookies", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain")
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, _ := report("https://elsewhere.example", map[string]any{"key": key, "cookies": []string{"_ttp"}}); code != 403 {
		t.Fatalf("other origin: %d", code)
	}
	code, rep := report(site.URL, map[string]any{"key": key, "cookies": []string{"_ttp", "_ga", "sid"}, "local": []string{"of_cb"},
		"scripts": []string{"analytics.tiktok.com/i18n"}, "path": "/pricing?plan=pro", "granted": map[string]any{"analytics": true, "marketing": false}})
	if code != 200 || rep["added"] != 3.0 {
		t.Fatalf("report: %d %v", code, rep)
	}
	// The same names again within the hour are not written again.
	if _, rep := report(site.URL, map[string]any{"key": key, "cookies": []string{"_ttp"}}); rep["added"] != 0.0 {
		t.Fatalf("repeat: %v", rep)
	}
	out = owner.must(200, "GET", path+"/cookies", nil)
	for _, x := range out["data"].([]any) {
		c := x.(map[string]any)
		if c["name"] == "_ttp" && (c["before_consent"] != true || c["category"] != "marketing" || c["pages"].([]any)[0] != "/pricing") {
			t.Fatalf("_ttp: %v", c)
		}
		if c["name"] == "_ga" && c["before_consent"] != false {
			t.Fatalf("_ga was allowed: %v", c)
		}
	}
	versions := owner.must(200, "GET", path+"/cookie-policy/versions", nil)["data"].([]any)
	latest := versions[0].(map[string]any)
	if len(versions) != 3 || latest["reasked"] != true || len(latest["added"].([]any)) != 3 {
		t.Fatalf("versions: %v", versions)
	}
	if b := owner.must(200, "GET", path+"/consent-banner", nil); b["consent_version"] != 2.0 {
		t.Fatalf("not asked again: %v", b["consent_version"])
	}

	// The site's own pages and the hosted page.
	get := func(url string) (int, string, string) {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequest("GET", url, nil))
		return rec.Code, rec.Header().Get("Content-Type"), rec.Body.String()
	}
	if code, ct, body := get("/v1/site/cookie-policy?key=" + key + "&format=markdown"); code != 200 || !strings.HasPrefix(ct, "text/markdown") || !strings.Contains(body, "TikTok Pixel") {
		t.Fatalf("markdown: %d %s\n%s", code, ct, body)
	}
	if code, _, body := get("/v1/site/cookie-policy?key=" + key + "&format=html"); code != 200 || !strings.Contains(body, "data-outflare-consent-open") {
		t.Fatalf("html: %d %s", code, body)
	}
	if code, _, body := get("/v1/site/cookie-policy?key=" + key); code != 200 || !strings.Contains(body, `"version":3`) {
		t.Fatalf("json: %d %s", code, body)
	}
	if code, _, _ := get("/v1/site/cookie-policy?key=ofk_test_not_a_real_key"); code != 404 {
		t.Fatalf("unknown key: %d", code)
	}
	if code, ct, body := get("/cookie-policy/" + id); code != 200 || !strings.HasPrefix(ct, "text/html") || !strings.Contains(body, "<title>Cookie policy for northpeak.example</title>") {
		t.Fatalf("hosted: %d %s", code, ct)
	}
	if code, _, _ := get("/cookie-policy/00000000-0000-0000-0000-000000000000"); code != 404 {
		t.Fatalf("hosted unknown: %d", code)
	}
}
