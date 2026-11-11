package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Redirect modes, tracking domain fallbacks, and tracker features only on
// the workspace's own domains (plans/ad-tracker.md, T4c).
func TestRedirectModes(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	const person = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"
	get := func(host, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if host != "" {
			req.Host = host
		}
		req.Header.Set("User-Agent", person)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	refreshTo := regexp.MustCompile(`http-equiv="refresh" content="0;url=([^"]+)"`)
	newLink := func(mode string) string {
		t.Helper()
		c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
			"name": "Mode " + mode, "channel": "paid_ads", "source": "acme", "redirect_links": true, "redirect_mode": mode,
		})
		if c["redirect_mode"] != mode {
			t.Fatalf("campaign: %v", c)
		}
		l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://northpeak.example/page"})
		return regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	}
	owner.must(400, "POST", base+"/tracking-campaigns", map[string]any{"name": "Bad mode", "channel": "paid_ads", "source": "acme", "redirect_mode": "301"})

	// On the shared domain every mode is a 302, and destinations aren't used.
	meta := newLink("meta")
	if rec := get("", "/l/"+meta); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://northpeak.example/page?") {
		t.Fatalf("shared domain: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	l2 := newLink("302")
	links := owner.must(200, "GET", base+"/tracking-campaigns", nil)["data"].([]any)
	var code302 string
	for _, x := range links {
		if m := x.(map[string]any); m["name"] == "Mode 302" {
			code302 = m["code"].(string)
		}
	}
	linkID := owner.must(200, "GET", base+"/tracking-campaigns/"+code302+"/links", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	d := owner.must(200, "PUT", base+"/tracking-campaigns/"+code302+"/links/"+linkID+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"url": "https://northpeak.example/offer"},
	}})
	if d["needs_domain"] != true {
		t.Fatalf("needs_domain: %v", d)
	}
	if loc := get("", "/l/"+l2).Header().Get("Location"); !strings.HasPrefix(loc, "https://northpeak.example/page?") {
		t.Fatalf("destinations ran on the shared domain: %s", loc)
	}

	// On the brand's own domain: destinations and every mode.
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	if loc := get(host, "/l/"+l2).Header().Get("Location"); !strings.HasPrefix(loc, "https://northpeak.example/offer?") {
		t.Fatalf("own domain, destination: %s", loc)
	}
	rec := get(host, "/l/"+meta)
	m := refreshTo.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || rec.Header().Get("Referrer-Policy") != "no-referrer" || m == nil || !strings.HasPrefix(m[1], "https://northpeak.example/page?") {
		t.Fatalf("meta: %d %s", rec.Code, rec.Body)
	}
	if rec := get(host, "/l/"+newLink("307")); rec.Code != http.StatusTemporaryRedirect {
		t.Fatalf("307: %d", rec.Code)
	}
	rec = get(host, "/l/"+newLink("double_meta"))
	m = refreshTo.FindStringSubmatch(rec.Body.String())
	if m == nil || !strings.HasPrefix(m[1], "/l/go?") {
		t.Fatalf("double meta, first page: %s", rec.Body)
	}
	hop := strings.ReplaceAll(m[1], "&amp;", "&")
	rec = get(host, hop)
	m = refreshTo.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil || !strings.HasPrefix(strings.ReplaceAll(m[1], "&amp;", "&"), "https://northpeak.example/page?") {
		t.Fatalf("double meta, second page: %d %s", rec.Code, rec.Body)
	}
	// The second step goes only where this server signed.
	u, _ := url.Parse(hop)
	q := u.Query()
	q.Set("u", "aHR0cHM6Ly9ldmlsLmV4YW1wbGUv")
	if rec := get(host, "/l/go?"+q.Encode()); rec.Code != http.StatusNotFound {
		t.Fatalf("tampered hop: %d", rec.Code)
	}

	// An unknown code: a not found page, or the domain's fallback page.
	if rec := get(host, "/l/zzzzzzzz"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", rec.Code)
	}
	var domainID string
	if err := e.pool.QueryRow(ctx, `SELECT id::text FROM tracking_domains WHERE domain = $1`, host).Scan(&domainID); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "PUT", base+"/tracking-domains/"+domainID+"/fallback", map[string]any{"url": "northpeak"})
	dv := owner.must(200, "PUT", base+"/tracking-domains/"+domainID+"/fallback", map[string]any{"url": "https://northpeak.example/"})
	if dv["fallback_url"] != "https://northpeak.example/" {
		t.Fatalf("domain: %v", dv)
	}
	if rec := get(host, "/l/zzzzzzzz"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://northpeak.example/" {
		t.Fatalf("fallback: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get("", "/l/zzzzzzzz"); rec.Code != http.StatusNotFound {
		t.Fatalf("shared domain, unknown code: %d", rec.Code)
	}
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	owner.must(404, "PUT", "/v1/brands/"+other["id"].(string)+"/tracking-domains/"+domainID+"/fallback", map[string]any{"url": "https://northpeak.example/"})

	found := false
	for _, ev := range events() {
		found = found || ev.Name == "tracking_domain.fallback_set"
	}
	if !found {
		t.Error("no tracking_domain.fallback_set event")
	}
}
