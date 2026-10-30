package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// helpGet reads a public help centre page as a browser would, on host
// when given.
func helpGet(t *testing.T, h http.Handler, method, path, host, form string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(form))
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh) Safari/605.1.15")
	if form != "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if host != "" {
		req.Host = host
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHelpCentre(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	kb := base + "/knowledge"
	ctx := context.Background()

	owner.must(200, "PATCH", base+"/context", map[string]any{"company": "Northpeak"})
	if _, err := e.pool.Exec(ctx, `UPDATE workspace_context SET logo_url = 'https://northpeak.example/logo.svg', colors = '{"primary": "#0f766e"}' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}

	billing := owner.must(201, "POST", kb+"/collections", map[string]any{"name": "Billing", "description": "Plans, invoices and refunds.", "icon": "card"})
	if billing["icon"] != "card" {
		t.Fatalf("folder icon = %v", billing)
	}
	owner.must(400, "POST", kb+"/collections", map[string]any{"name": "x", "icon": "skull"})
	internal := owner.must(201, "POST", kb+"/collections", map[string]any{"name": "Internal"})
	refunds := owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refunds and cancellations", "collection_id": billing["id"], "visibility": "public",
		"body": "## Refunds\n\nWe refund any plan within 30 days. <script>alert(1)</script>\n\n## How to cancel\n\nOpen **Settings**, then [Billing](javascript:alert(1)).\n\n| Plan | Days |\n| --- | --- |\n| Pro | 30 |"})
	owner.must(201, "POST", kb+"/items", map[string]any{"kind": "snippet", "title": "Do you offer a free trial?", "body": "Yes, 14 days.", "collection_id": billing["id"], "visibility": "public"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Discounts we can offer", "collection_id": billing["id"], "visibility": "team", "body": "Up to 20% off."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Refund drafts", "collection_id": billing["id"], "visibility": "public", "status": "draft", "body": "Not yet."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Margins", "collection_id": internal["id"], "visibility": "customers_ai", "body": "Secret."})

	// A new help centre starts off, from the company's name, logo and colour.
	hc := owner.must(201, "POST", base+"/help-centres", map[string]any{})
	id := hc["id"].(string)
	set := hc["settings"].(map[string]any)
	if hc["name"] != "Northpeak Help Center" || hc["slug"] != "northpeak-help-center" || hc["enabled"] != false || hc["articles"] != 2.0 ||
		set["logo_url"] != "https://northpeak.example/logo.svg" || set["color"] != "#0f766e" || set["hero_title"] != "How can we help?" ||
		!strings.HasSuffix(hc["url"].(string), "/h/northpeak-help-center/") {
		t.Fatalf("help centre = %v", hc)
	}
	owner.must(400, "POST", base+"/help-centres", map[string]any{}) // one for the whole workspace
	owner.must(400, "PATCH", base+"/help-centres/"+id, map[string]any{"settings": map[string]any{"color": "teal"}})
	owner.must(400, "PATCH", base+"/help-centres/"+id, map[string]any{"settings": map[string]any{"logo_url": "http://northpeak.example/x.png"}})
	owner.must(400, "PATCH", base+"/help-centres/"+id, map[string]any{"settings": map[string]any{"header_links": []map[string]string{{"label": "x", "url": "javascript:alert(1)"}}}})
	owner.must(400, "PATCH", base+"/help-centres/"+id, map[string]any{"settings": map[string]any{"font": "comic-sans"}})
	owner.must(400, "PATCH", base+"/help-centres/"+id, map[string]any{"slug": "Not A Slug"})

	// Off, it answers 404; the preview still renders.
	if rec := helpGet(t, e.h, "GET", "/h/northpeak-help-center/", "", ""); rec.Code != 404 {
		t.Fatalf("off help centre = %d", rec.Code)
	}
	pv := httptest.NewRecorder()
	req := httptest.NewRequest("POST", base+"/help-centres/"+id+"/preview", strings.NewReader(`{"settings": {"hero_title": "Hi there"}}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(owner.cookie)
	e.h.ServeHTTP(pv, req)
	if pv.Code != 200 || !strings.Contains(pv.Body.String(), "Hi there") || !strings.Contains(pv.Body.String(), "Billing") || strings.Contains(pv.Body.String(), "<script nonce") {
		t.Fatalf("preview = %d %s", pv.Code, pv.Body.String())
	}

	// Turned on, with a contact block and footer links.
	hc = owner.must(200, "PATCH", base+"/help-centres/"+id, map[string]any{"enabled": true, "slug": "northpeak", "settings": map[string]any{
		"contact_url": "mailto:support@northpeak.example", "footer_links": []map[string]string{{"label": "Status", "url": "https://status.northpeak.example"}},
		"social": map[string]string{"x": "https://x.com/northpeak"},
	}})
	if hc["enabled"] != true || hc["settings"].(map[string]any)["hero_title"] != "How can we help?" || hc["settings"].(map[string]any)["color"] != "#0f766e" {
		t.Fatalf("patched = %v", hc)
	}

	home := helpGet(t, e.h, "GET", "/h/northpeak/", "", "")
	body := home.Body.String()
	if home.Code != 200 || !strings.Contains(body, "Billing") || !strings.Contains(body, "Plans, invoices and refunds.") || !strings.Contains(body, "Do you offer a free trial?") ||
		strings.Contains(body, "Internal") || strings.Contains(body, "Discounts") || !strings.Contains(body, "mailto:support@northpeak.example") ||
		!strings.Contains(body, "https://status.northpeak.example") || !strings.Contains(home.Header().Get("Content-Security-Policy"), "nonce-") {
		t.Fatalf("home = %d %s", home.Code, body)
	}

	// The collection and the article; only what's public and published.
	ref := func(id, slug string) string { return strings.ReplaceAll(id, "-", "")[:8] + "-" + slug }
	coll := helpGet(t, e.h, "GET", "/h/northpeak/collections/"+ref(billing["id"].(string), "billing"), "", "")
	if coll.Code != 200 || !strings.Contains(coll.Body.String(), "Refunds and cancellations") || strings.Contains(coll.Body.String(), "Refund drafts") {
		t.Fatalf("collection = %d %s", coll.Code, coll.Body.String())
	}
	artPath := "/h/northpeak/articles/" + ref(refunds["id"].(string), "refunds-and-cancellations")
	art := helpGet(t, e.h, "GET", artPath, "", "")
	body = art.Body.String()
	if art.Code != 200 || !strings.Contains(body, `<h2 id="refunds">Refunds</h2>`) || !strings.Contains(body, "<table>") || strings.Contains(body, "<script>alert") || strings.Contains(body, `href="javascript`) ||
		!strings.Contains(body, "On this page") || !strings.Contains(body, "Did this answer your question?") || !strings.Contains(body, "Copy for AI") {
		t.Fatalf("article = %d %s", art.Code, body)
	}
	// Any words after the id start find it; a wrong id doesn't.
	if helpGet(t, e.h, "GET", "/h/northpeak/articles/"+ref(refunds["id"].(string), "old-title"), "", "").Code != 200 {
		t.Fatal("renamed article's old address")
	}
	if helpGet(t, e.h, "GET", "/h/northpeak/articles/00000000-nothing", "", "").Code != 404 {
		t.Fatal("unknown article")
	}
	md := helpGet(t, e.h, "GET", artPath+".md", "", "")
	if md.Code != 200 || !strings.HasPrefix(md.Body.String(), "# Refunds and cancellations\n\n## Refunds") {
		t.Fatalf("markdown = %d %s", md.Code, md.Body.String())
	}
	if rec := helpGet(t, e.h, "GET", "/h/northpeak/llms.txt", "", ""); !strings.Contains(rec.Body.String(), "## Billing") || !strings.Contains(rec.Body.String(), ".md)") {
		t.Fatalf("llms.txt = %s", rec.Body.String())
	}
	if rec := helpGet(t, e.h, "GET", "/h/northpeak/sitemap.xml", "", ""); !strings.Contains(rec.Body.String(), "/articles/"+ref(refunds["id"].(string), "refunds-and-cancellations")) {
		t.Fatalf("sitemap = %s", rec.Body.String())
	}

	// Search finds by words; a search that finds nothing becomes an
	// unanswered question.
	if rec := helpGet(t, e.h, "GET", "/h/northpeak/search?q=cancel+plan", "", ""); !strings.Contains(rec.Body.String(), "Refunds and cancellations") {
		t.Fatalf("search = %s", rec.Body.String())
	}
	if rec := helpGet(t, e.h, "GET", "/h/northpeak/search?q=discount", "", ""); !strings.Contains(rec.Body.String(), "No results") {
		t.Fatalf("team-only found = %s", rec.Body.String())
	}
	helpGet(t, e.h, "GET", "/h/northpeak/search?q=office+in+Lisbon", "", "")
	gaps := owner.must(200, "GET", kb+"/gaps", nil)["data"].([]any)
	if len(gaps) != 1 || gaps[0].(map[string]any)["question"] != "office in Lisbon" || gaps[0].(map[string]any)["customers"] != 1.0 {
		t.Fatalf("gaps = %v", gaps)
	}

	// Feedback, then the numbers.
	fb := helpGet(t, e.h, "POST", artPath+"/feedback", "", "helpful=yes")
	if fb.Code != 303 || !strings.Contains(fb.Header().Get("Location"), "thanks=1") {
		t.Fatalf("feedback = %d %v", fb.Code, fb.Header())
	}
	helpGet(t, e.h, "POST", artPath+"/feedback", "", "helpful=no")
	if rec := helpGet(t, e.h, "GET", artPath+"?thanks=1", "", ""); !strings.Contains(rec.Body.String(), "Thanks for letting us know.") {
		t.Fatal("no thanks")
	}
	// Crawlers aren't counted.
	bot := httptest.NewRequest("GET", artPath, nil)
	bot.Header.Set("User-Agent", "Googlebot/2.1")
	e.h.ServeHTTP(httptest.NewRecorder(), bot)
	st := owner.must(200, "GET", base+"/help-centres/"+id+"/stats", nil)
	arts := st["articles"].([]any)
	if st["searches"] != 3.0 || st["empty_searches"] != 2.0 || st["helpful"] != 1.0 || st["unhelpful"] != 1.0 || len(arts) != 1 ||
		arts[0].(map[string]any)["views"] != 3.0 || len(st["by_day"].([]any)) != 30 {
		t.Fatalf("stats = %v", st)
	}

	// Its own domain serves it at the root, once verified.
	dom := owner.must(201, "POST", base+"/tracking-domains", map[string]any{"domain": "help.northpeak.example"})
	owner.must(200, "PATCH", base+"/help-centres/"+id, map[string]any{"domain_id": dom["id"]})
	if rec := helpGet(t, e.h, "GET", "/", "help.northpeak.example", ""); rec.Code == 200 && strings.Contains(rec.Body.String(), "How can we help?") {
		t.Fatal("served before the domain is verified")
	}
	if _, err := e.pool.Exec(ctx, `UPDATE tracking_domains SET status = 'verified', verified_at = now() WHERE id = $1`, dom["id"]); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", base+"/help-centres/"+id, map[string]any{"domain_id": dom["id"]}) // forgets cached hosts
	root := helpGet(t, e.h, "GET", "/", "help.northpeak.example", "")
	if root.Code != 200 || !strings.Contains(root.Body.String(), `href="/collections/`) {
		t.Fatalf("own domain = %d %s", root.Code, root.Body.String())
	}
	if rec := helpGet(t, e.h, "GET", "/v1/me", "help.northpeak.example", ""); rec.Code != 404 {
		t.Fatalf("the app on a help domain = %d", rec.Code)
	}
	hc = owner.must(200, "GET", base+"/help-centres/"+id, nil)
	if hc["domain"] != "help.northpeak.example" {
		t.Fatalf("domain = %v", hc)
	}
	ds := owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any)
	if len(ds) != 1 || ds[0].(map[string]any)["help_centre_id"] != id {
		t.Fatalf("tracking domains = %v", ds)
	}

	// Switched off, and deleted.
	owner.must(200, "PATCH", base+"/help-centres/"+id, map[string]any{"enabled": false})
	if rec := helpGet(t, e.h, "GET", "/h/northpeak/", "", ""); rec.Code != 404 {
		t.Fatalf("switched off = %d", rec.Code)
	}
	owner.must(204, "DELETE", base+"/help-centres/"+id, nil)
	if l := owner.must(200, "GET", base+"/help-centres", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("list after delete = %v", l)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, n := range []string{"help_centre.created", "help_centre.published", "help_centre.updated", "help_centre.unpublished", "help_centre.deleted"} {
		if !seen[n] {
			t.Errorf("no %s event", n)
		}
	}
}

func TestHelpCentreBrands(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	kb := base + "/knowledge"
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "handle": "repocket"})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Proxy setup", "brand_id": "geonode", "visibility": "public", "body": "Use the dashboard."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Payout schedule", "brand_id": "repocket", "visibility": "public", "body": "Monthly."})
	owner.must(201, "POST", kb+"/items", map[string]any{"title": "Account security", "visibility": "public", "body": "Turn on two-factor sign-in."})

	geo := owner.must(201, "POST", base+"/help-centres", map[string]any{"brand_id": "geonode", "enabled": true})
	if geo["name"] != "Geonode Help Center" || geo["articles"] != 2.0 {
		t.Fatalf("brand help centre = %v", geo)
	}
	owner.must(400, "POST", base+"/help-centres", map[string]any{"brand_id": "geonode"})
	owner.must(201, "POST", base+"/help-centres", map[string]any{"brand_id": "repocket", "name": "Repocket Help"})
	owner.must(400, "PATCH", base+"/help-centres/"+geo["id"].(string), map[string]any{"brand_id": "repocket"})

	body := helpGet(t, e.h, "GET", "/h/"+geo["slug"].(string)+"/", "", "").Body.String()
	if !strings.Contains(body, "Proxy setup") || !strings.Contains(body, "Account security") || strings.Contains(body, "Payout schedule") {
		t.Fatalf("brand home = %s", body)
	}
	if l := owner.must(200, "GET", base+"/help-centres?brand=repocket", nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["name"] != "Repocket Help" {
		t.Fatalf("brand list = %v", l)
	}
}
