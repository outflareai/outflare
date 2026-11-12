package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/track"
)

// lookSite is a brand's site with an SVG icon that answers and a
// structured-data logo that is gone.
type lookSite struct{}

func (lookSite) Fetch(_ context.Context, u string) (*crawl.Response, error) {
	h := func(ct string) http.Header { return http.Header{"Content-Type": []string{ct}} }
	switch {
	case strings.HasSuffix(u, "/robots.txt"):
		return &crawl.Response{URL: u, Status: 200, Header: h("text/plain"), Body: []byte("User-agent: *\nAllow: /")}, nil
	case strings.HasSuffix(u, "/icon.svg"):
		return &crawl.Response{URL: u, Status: 200, Header: h("image/svg+xml")}, nil
	case strings.HasSuffix(u, "/gone.png"):
		return &crawl.Response{URL: u, Status: 404, Header: h("text/html")}, nil
	}
	return &crawl.Response{URL: u, Status: 200, Header: h("text/html"), Body: []byte(`<html><head>
		<script type="application/ld+json">{"logo":"/gone.png"}</script>
		<link rel="icon" type="image/svg+xml" href="/icon.svg"><meta name="theme-color" content="#0f766e"></head><body></body></html>`)}, nil
}

// A brand's empty logo and main colour are filled in from its own site,
// and never replace what someone set.
func TestBrandLook(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	e.crawl.Fetcher = lookSite{}
	id := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	var n int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind = $2`, ws, "brand.look_read").Scan(&n)
	if n != 1 {
		t.Fatalf("creating a brand with a website queues %d look reads", n)
	}
	if err := e.srv.ReadBrandLook(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	b := owner.must(200, "GET", base+"/brands/northpeak", nil)
	if b["logo_url"] != "https://northpeak.example/icon.svg" || b["colors"].(map[string]any)["primary"] != "#0f766e" {
		t.Fatalf("found look: %v %v", b["logo_url"], b["colors"])
	}
	read := false
	for _, ev := range events() {
		read = read || ev.Name == "brand.look_read" && ev.Props["logo_found"] == true && ev.Props["color_found"] == true
	}
	var audited int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'brand.look_found'`, ws).Scan(&audited)
	if !read || audited != 1 {
		t.Fatalf("look event %v, audit %d", read, audited)
	}
	// Someone's own logo stays; the read changes nothing.
	owner.must(200, "PATCH", base+"/brands/northpeak", map[string]any{"logo_url": "https://northpeak.example/mine.png", "colors": map[string]string{"primary": "", "background": "", "text": ""}})
	owner.must(202, "POST", base+"/brands/northpeak/look/find", nil)
	if err := e.srv.ReadBrandLook(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	b = owner.must(200, "GET", base+"/brands/northpeak", nil)
	if b["logo_url"] != "https://northpeak.example/mine.png" || b["colors"].(map[string]any)["primary"] != "#0f766e" {
		t.Fatalf("kept look: %v %v", b["logo_url"], b["colors"])
	}
	// No website, nothing to read.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Trailcast", "handle": "trailcast"})
	owner.must(400, "POST", base+"/brands/trailcast/look/find", nil)
}
