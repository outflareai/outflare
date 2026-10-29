package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestBrandsContextAndFilters(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	// A workspace starts with no brands, and its context says nothing about them.
	if n := len(owner.must(200, "GET", base+"/brands", nil)["data"].([]any)); n != 0 {
		t.Fatalf("new workspace has %d brands", n)
	}
	if c := owner.must(200, "GET", base+"/context", nil); len(c["brands"].([]any)) != 0 || c["brand"] != nil {
		t.Fatalf("context without brands: %v", c)
	}

	// Adding brands: the website becomes a domain and handles come from names.
	owner.must(400, "POST", base+"/brands", map[string]any{"website": "geonode.com"})
	owner.must(400, "POST", base+"/brands", map[string]any{"name": "X", "platforms": []string{"fax"}})
	owner.must(400, "POST", base+"/brands", map[string]any{"name": "X", "handle": "None"})
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "https://www.geonode.com/pricing"})
	if geo["handle"] != "geonode" || geo["website"] != "geonode.com" || geo["domains"].([]any)[0] != "geonode.com" || geo["context_version"] != 1.0 {
		t.Fatalf("geonode: %v", geo)
	}
	rp := owner.must(201, "POST", base+"/brands", map[string]any{
		"name": "Repocket", "website": "repocket.com", "domains": []string{"repocket.co"}, "platforms": []string{"web", "ios", "android"},
	})
	rid := rp["id"].(string)
	if d := rp["domains"].([]any); len(d) != 2 || d[0] != "repocket.com" {
		t.Fatalf("repocket domains: %v", d)
	}
	// Links: kinds are guessed from the address, bad addresses refused.
	links := owner.must(200, "PATCH", base+"/brands/repocket", map[string]any{"links": []map[string]any{
		{"url": "https://apps.apple.com/app/repocket/id1"}, {"url": "play.google.com/store/apps/details?id=com.repocket"},
		{"url": "https://www.amazon.com/dp/B0", "kind": "tv_app", "label": "Fire TV"}, {"url": "https://help.repocket.com"},
	}})["links"].([]any)
	if len(links) != 4 || links[0].(map[string]any)["kind"] != "app_store" || links[1].(map[string]any)["kind"] != "google_play" ||
		links[1].(map[string]any)["url"] != "https://play.google.com/store/apps/details?id=com.repocket" || links[3].(map[string]any)["kind"] != "other" {
		t.Fatalf("links: %v", links)
	}
	owner.must(400, "PATCH", base+"/brands/repocket", map[string]any{"links": []map[string]any{{"url": "javascript:alert(1)"}}})
	owner.must(400, "PATCH", base+"/brands/repocket", map[string]any{"links": []map[string]any{{"url": "https://x.com/r", "kind": "myspace"}}})
	owner.must(400, "POST", base+"/brands", map[string]any{"name": "repocket"})
	owner.must(400, "PATCH", base+"/brands/geonode", map[string]any{"handle": "repocket"})
	if b := owner.must(200, "GET", base+"/brands/repocket", nil); b["id"] != rid {
		t.Fatalf("by handle: %v", b)
	}
	owner.must(404, "GET", base+"/brands/nope", nil)

	// Brand context: versioned like the company's, and layered on it.
	owner.must(200, "PATCH", base+"/context", map[string]any{
		"company": "Geonode Group", "rules": "Never promise uptime numbers.",
		"brands": "Repocket pays people for spare bandwidth that Geonode resells.",
	})
	bc := owner.must(200, "PATCH", base+"/brands/repocket/context", map[string]any{
		"about": "Earn passive income by sharing unused internet.\nMore detail here.", "voice": "Friendly and casual.",
		"rules": "Never mention Geonode.", "version": 1,
	})
	if bc["version"] != 2.0 || bc["voice"] != "Friendly and casual." {
		t.Fatalf("brand context: %v", bc)
	}
	e := owner.must(409, "PATCH", base+"/brands/repocket/context", map[string]any{"goals": "Stale", "version": 1})
	if e["error"].(map[string]any)["code"] != "context_changed" {
		t.Fatalf("stale brand context: %v", e)
	}

	// Memories at two levels: company and brand.
	owner.must(201, "POST", base+"/memories", map[string]any{"text": "Jack approves new domains", "pinned": true})
	rm := owner.must(201, "POST", base+"/memories", map[string]any{"text": "No discounts over 20%", "brand_id": "repocket"})
	if rm["brand_id"] != rid {
		t.Fatalf("brand memory: %v", rm)
	}
	gm := owner.must(201, "POST", base+"/memories", map[string]any{"text": "Proxy buyers want SOCKS5", "brand_id": "geonode"})
	if gm["brand_id"] != geo["id"] {
		t.Fatalf("geonode memory: %v", gm)
	}

	// Without a brand: the company layer and a line per brand.
	c := owner.must(200, "GET", base+"/context", nil)
	md := c["markdown"].(string)
	if c["memory_count"] != 1.0 || len(c["brands"].([]any)) != 2 || !strings.Contains(md, "Repocket (brand repocket, repocket.com): Earn passive income") ||
		!strings.Contains(md, "How our brands relate") || c["projects"] != nil {
		t.Fatalf("company context: %v\n%s", c, md)
	}
	// With a brand: company and brand together, other brands' memories left out.
	c = owner.must(200, "GET", base+"/context?brand=repocket", nil)
	md = c["markdown"].(string)
	if c["brand"].(map[string]any)["brand_id"] != rid || c["memory_count"] != 2.0 ||
		!strings.Contains(md, "Never promise uptime") || !strings.Contains(md, "# Brand: Repocket") || !strings.Contains(md, "- Fire TV: https://www.amazon.com/dp/B0") || !strings.Contains(md, "Never mention Geonode") {
		t.Fatalf("repocket context: %v\n%s", c, md)
	}
	if c := owner.must(200, "GET", base+"/context?brand=geonode", nil); c["memory_count"] != 2.0 {
		t.Fatalf("geonode context: %v", c)
	}
	owner.must(404, "GET", base+"/context?brand=nope", nil)
	if n := owner.must(200, "GET", base+"/memories?brand=none", nil)["total"]; n != 1.0 {
		t.Fatalf("company memories: %v", n)
	}

	// Moving records: lists filter by brand.
	list := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Gamers"})
	other := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Proxy leads"})
	owner.must(400, "POST", base+"/brands/repocket/add", map[string]any{})
	moved := owner.must(200, "POST", base+"/brands/repocket/add", map[string]any{
		"lead_list_ids": []string{list["id"].(string), other["id"].(string)},
	})["moved"].(map[string]any)
	if moved["lead_lists"] != 2.0 || moved["in_project"] != nil {
		t.Fatalf("moved: %v", moved)
	}
	if n := len(owner.must(200, "GET", base+"/lead-lists?brand=repocket", nil)["data"].([]any)); n != 2 {
		t.Fatalf("repocket lists: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/lead-lists?brand=none", nil)["data"].([]any)); n != 0 {
		t.Fatalf("shared lists: %d", n)
	}
	owner.must(404, "GET", base+"/lead-lists?brand=nope", nil)
	b := owner.must(200, "GET", base+"/brands/repocket", nil)
	if c := b["counts"].(map[string]any); c["projects"] != nil || c["lead_lists"] != 2.0 {
		t.Fatalf("counts: %v", c)
	}
	owner.must(200, "POST", base+"/brands/repocket/remove", map[string]any{"lead_list_ids": []string{list["id"].(string)}})
	if n := len(owner.must(200, "GET", base+"/lead-lists?brand=none", nil)["data"].([]any)); n != 1 {
		t.Fatalf("shared again: %d", n)
	}

	// Archiving hides a brand from the default list but keeps it.
	owner.must(200, "PATCH", base+"/brands/geonode", map[string]any{"status": "archived"})
	if n := len(owner.must(200, "GET", base+"/brands", nil)["data"].([]any)); n != 1 {
		t.Fatalf("active brands: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/brands?status=all", nil)["data"].([]any)); n != 2 {
		t.Fatalf("all brands: %d", n)
	}

	// Reading alone cannot add brands or write their context; every brand action is an event.
	reader := &client{t: t, h: h, bearer: owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "r", "scopes": []string{"workspace.read"}})["token"].(string)}
	reader.must(403, "POST", base+"/brands", map[string]any{"name": "Games"})
	reader.must(403, "PATCH", base+"/brands/repocket/context", map[string]any{"about": "x"})
	time.Sleep(10 * time.Millisecond)
	ev := got()
	for _, name := range []string{"brand.created", "brand.context_updated", "brand.records_added", "brand.records_removed", "brand.archived"} {
		if len(named(ev, name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}

func TestBrandMailboxesSendOnlyForTheirBrand(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, _ := sendingSetup(t, e, clock)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode"})
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket"})
	moved := owner.must(200, "POST", base+"/brands/geonode/add", map[string]any{"domains": []string{"acme.test"}})["moved"].(map[string]any)
	if moved["domains"] != 1.0 || moved["mailboxes"] != 2.0 {
		t.Fatalf("moved: %v", moved)
	}
	if n := len(owner.must(200, "GET", base+"/mailboxes?brand=geonode", nil)["data"].([]any)); n != 2 {
		t.Fatalf("geonode mailboxes: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/sending-domains?brand=repocket", nil)["data"].([]any)); n != 0 {
		t.Fatalf("repocket domains: %d", n)
	}
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Repocket launch", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}},
	})["id"].(string)
	owner.must(200, "POST", base+"/brands/repocket/add", map[string]any{"sequence_ids": []string{seq}})
	if s := owner.must(200, "GET", base+"/sequences/"+seq, nil); s["brand_id"] == nil {
		t.Fatalf("sequence brand: %v", s["brand_id"])
	}
	boxes := mailboxIDs(t, owner, base)
	res := owner.must(200, "POST", base+"/mailboxes/assign", map[string]any{"mailbox_ids": []string{boxes["ana@acme.test"]}, "sequence_id": seq})
	r0 := res["data"].([]any)[0].(map[string]any)
	if r0["result"] != "refused" || !strings.Contains(r0["message"].(string), "brand Geonode") {
		t.Fatalf("cross-brand assign: %v", res)
	}
}

func TestAnalyticsSitesByBrand(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)

	// One tool reads three sites: two match a brand's domain, one no brand's.
	ga := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "GA4", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	day := time.Now().UTC().AddDate(0, 0, -2).Format(time.DateOnly)
	for site, host := range map[string]string{"properties/1": "geonode.com", "properties/2": "app.repocket.com", "properties/3": "games.io"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key, visitors)
			VALUES ($1, $2, 'plausible', $3, $4, 'traffic', $5, '', 10)`, ws, ga, site, host, day); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, site_name) VALUES ($1, $2, $3, $4, $3)`, ga, ws, site, host); err != nil {
			t.Fatal(err)
		}
	}
	// Home lists the games site as having no brand, and every brand has analytics.
	blockers := func() map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, b := range owner.must(200, "GET", base+"/status", nil)["blockers"].([]any) {
			out[b.(map[string]any)["code"].(string)] = b.(map[string]any)
		}
		return out
	}
	if b := blockers(); b["analytics_site_unmapped"] == nil || b["analytics_site_unmapped"]["count"] != 1.0 || b["brand_without_analytics"] != nil {
		t.Fatalf("status: %v", b)
	}
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Sudokumax", "website": "sudokumax.com"})
	if b := blockers()["brand_without_analytics"]; b == nil || b["count"] != 1.0 {
		t.Fatalf("brand without analytics: %v", b)
	}
	visitors := func(q string) float64 {
		t.Helper()
		var n float64
		for _, row := range owner.must(200, "GET", base+"/analytics/history?group=tool"+q, nil)["data"].([]any) {
			n += row.(map[string]any)["visitors"].(float64)
		}
		return n
	}
	if visitors("") != 30 || visitors("&brand=geonode") != 10 || visitors("&brand=repocket") != 10 || visitors("&brand=none") != 10 {
		t.Fatalf("by domain: %v %v %v %v", visitors(""), visitors("&brand=geonode"), visitors("&brand=repocket"), visitors("&brand=none"))
	}
	// A day stored before the site's domain was known still lists it once.
	if _, err := e.pool.Exec(ctx, `INSERT INTO analytics_daily (workspace_id, integration_id, connector, site, host, report, day, key, visitors)
		VALUES ($1, $2, 'plausible', 'properties/1', '', 'traffic', $3, '', 0)`, ws, ga, time.Now().UTC().AddDate(0, 0, -20).Format(time.DateOnly)); err != nil {
		t.Fatal(err)
	}
	sites := owner.must(200, "GET", base+"/analytics/site-brands", nil)["data"].([]any)
	if len(sites) != 3 {
		t.Fatalf("sites: %v", sites)
	}

	// Picking a brand for the games site, then making the Geonode one shared.
	owner.must(400, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "properties/3", "brand": "nope"})
	owner.must(404, "PUT", base+"/analytics/00000000-0000-0000-0000-000000000000/site-brand", map[string]any{"site": "x", "brand": "repocket"})
	sb := owner.must(200, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "properties/3", "brand": "repocket"})
	if sb["brand_id"] != rp || sb["source"] != "set" || sb["host"] != "games.io" {
		t.Fatalf("set: %v", sb)
	}
	if sb := owner.must(200, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "properties/1", "brand": "none"}); sb["brand_id"] != nil || sb["source"] != "set" {
		t.Fatalf("shared: %v", sb)
	}
	if visitors("&brand=repocket") != 20 || visitors("&brand=geonode") != 0 || visitors("&brand=none") != 10 {
		t.Fatal("after mapping")
	}
	if b := blockers(); b["analytics_site_unmapped"] != nil {
		t.Fatalf("mapped sites still listed: %v", b["analytics_site_unmapped"])
	}
	if n := len(owner.must(200, "GET", base+"/analytics/site-brands?brand=repocket", nil)["data"].([]any)); n != 2 {
		t.Fatalf("repocket sites: %d", n)
	}
	// auto goes back to the domain match.
	if sb := owner.must(200, "PUT", base+"/analytics/"+ga+"/site-brand", map[string]any{"site": "properties/1", "brand": "auto"}); sb["source"] != "domain" {
		t.Fatalf("auto: %v", sb)
	}
	if o := owner.must(200, "GET", base+"/analytics/overview?brand=geonode", nil)["data"].([]any); len(o) != 1 {
		t.Fatalf("overview: %v", o)
	}
	time.Sleep(10 * time.Millisecond)
	if len(named(got(), "analytics.site_brand_set")) != 3 {
		t.Error("no analytics.site_brand_set events")
	}
}

func TestBrandAccess(t *testing.T) {
	got := track.Capture(t)
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	geoSeq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Proxy buyers"})["id"].(string)
	rpSeq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Bandwidth sharers"})["id"].(string)
	shared := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Partners"})["id"].(string)
	owner.must(201, "POST", base+"/memories", map[string]any{"text": "Geonode pricing is secret", "brand_id": "geonode"})
	owner.must(201, "POST", base+"/memories", map[string]any{"text": "Company fact"})
	geoList := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Geonode buyers"})["id"].(string)
	sharedList := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Everyone"})["id"].(string)
	owner.must(200, "POST", base+"/brands/geonode/add", map[string]any{"sequence_ids": []string{geoSeq}, "lead_list_ids": []string{geoList}})
	owner.must(200, "POST", base+"/brands/repocket/add", map[string]any{"sequence_ids": []string{rpSeq}})

	// A brand token: one brand. Projects were removed, so project_id is refused.
	scopes := []string{"workspace.read", "projects.manage", "leads.manage", "context.manage", "sequences.manage", "inbox.read", "analytics.read", "integrations.read"}
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": scopes, "brand": "nope"})
	owner.must(400, "POST", base+"/api-tokens", map[string]any{"name": "x", "scopes": scopes, "brand": "repocket", "project_id": rpSeq})
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket agent", "scopes": scopes, "brand": "repocket"})
	if tok["api_token"].(map[string]any)["brand_id"] != rp {
		t.Fatalf("token view: %v", tok["api_token"])
	}
	agent := &client{t: t, h: h, bearer: tok["token"].(string)}
	if b := agent.must(200, "GET", "/v1/whoami", nil)["token"].(map[string]any)["brand"].(map[string]any); b["handle"] != "repocket" {
		t.Fatalf("whoami brand: %v", b)
	}
	if ids := agent.must(200, "GET", base, nil)["brand_ids"].([]any); len(ids) != 1 || ids[0] != rp {
		t.Fatalf("workspace brand_ids: %v", ids)
	}

	// It sees its brand's records and the shared ones, nothing of other brands.
	ids := func(c *client, path string) map[string]bool {
		out := map[string]bool{}
		for _, x := range c.must(200, "GET", path, nil)["data"].([]any) {
			out[x.(map[string]any)["id"].(string)] = true
		}
		return out
	}
	if p := ids(agent, base+"/sequences"); len(p) != 2 || !p[rpSeq] || !p[shared] {
		t.Fatalf("agent sequences: %v", p)
	}
	if b := ids(agent, base+"/brands"); len(b) != 1 || !b[rp] {
		t.Fatalf("agent brands: %v", b)
	}
	if l := ids(agent, base+"/lead-lists"); len(l) != 1 || !l[sharedList] {
		t.Fatalf("agent lists: %v", l)
	}
	agent.must(404, "GET", base+"/sequences/"+geoSeq, nil)
	agent.must(404, "GET", base+"/sequences?brand=geonode", nil)
	agent.must(404, "GET", base+"/brands/geonode", nil)
	agent.must(404, "GET", base+"/lead-lists/"+geoList, nil)
	agent.must(404, "GET", base+"/context?brand=geonode", nil)
	c := agent.must(200, "GET", base+"/context", nil)
	if c["brand"].(map[string]any)["brand_id"] != rp || strings.Contains(c["markdown"].(string), "Geonode pricing") || !strings.Contains(c["markdown"].(string), "Company fact") {
		t.Fatalf("agent context: %v", c)
	}
	for _, m := range agent.must(200, "GET", base+"/memories", nil)["data"].([]any) {
		if m.(map[string]any)["brand_id"] == geo {
			t.Fatalf("agent sees a Geonode memory: %v", m)
		}
	}

	// It changes only its brand's records, and creates in its brand.
	agent.must(403, "PATCH", base+"/sequences/"+shared, map[string]any{"name": "Everywhere"})
	agent.must(403, "PATCH", base+"/lead-lists/"+sharedList, map[string]any{"name": "Mine"})
	agent.must(200, "PATCH", base+"/sequences/"+rpSeq, map[string]any{"name": "Students"})
	if m := agent.must(201, "POST", base+"/memories", map[string]any{"text": "Students love it"}); m["brand_id"] != rp {
		t.Fatalf("agent's memory: %v", m)
	}
	if l := agent.must(201, "POST", base+"/lead-lists", map[string]any{"name": "Students"}); l["brand_id"] != rp {
		t.Fatalf("agent's list: %v", l)
	}
	if q := agent.must(201, "POST", base+"/sequences", map[string]any{"name": "Repocket intro"}); q["brand_id"] != rp {
		t.Fatalf("agent's sequence: %v", q)
	}
	agent.must(201, "POST", base+"/leads", map[string]any{"email": "sam@students.test"})

	// Endpoints that need every brand refuse it.
	e := agent.must(403, "GET", base+"/status", nil)
	if !strings.Contains(e["error"].(map[string]any)["message"].(string), "every brand") {
		t.Fatalf("refusal: %v", e)
	}
	agent.must(403, "POST", base+"/brands", map[string]any{"name": "Mine"})

	// People: an operator kept to Geonode, by invite.
	owner.must(400, "POST", base+"/invites", map[string]any{"email": "ad@example.com", "role": "admin", "brand_ids": []string{"geonode"}})
	owner.must(400, "POST", base+"/invites", map[string]any{"email": "op@example.com", "role": "operator", "brand_ids": []string{"nope"}})
	inv := owner.must(201, "POST", base+"/invites", map[string]any{"email": "op@example.com", "role": "operator", "brand_ids": []string{"geonode"}})
	if b := inv["invite"].(map[string]any)["brand_ids"].([]any); len(b) != 1 || b[0] != geo {
		t.Fatalf("invite brands: %v", b)
	}
	op := &client{t: t, h: h}
	op.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Op", "password": "ops long password"})
	if b := op.must(200, "GET", "/v1/me", nil)["workspaces"].([]any)[0].(map[string]any)["brand_ids"].([]any); len(b) != 1 || b[0] != geo {
		t.Fatalf("me brands: %v", b)
	}
	if p := ids(op, base+"/sequences"); len(p) != 2 || !p[geoSeq] || !p[shared] {
		t.Fatalf("operator sequences: %v", p)
	}
	op.must(404, "GET", base+"/sequences/"+rpSeq, nil)
	op.must(403, "GET", base+"/status", nil)

	var opID string
	for _, m := range owner.must(200, "GET", base+"/members", nil)["data"].([]any) {
		if m := m.(map[string]any); m["email"] == "op@example.com" {
			opID = m["user_id"].(string)
			if b := m["brand_ids"].([]any); len(b) != 1 || b[0] != geo {
				t.Fatalf("member brands: %v", m)
			}
		}
	}
	owner.must(400, "PATCH", base+"/members/"+opID, map[string]any{})
	owner.must(400, "PATCH", base+"/members/"+opID, map[string]any{"brand_ids": []string{"nope"}})
	owner.must(200, "PATCH", base+"/members/"+opID, map[string]any{"brand_ids": []string{"geonode", "repocket"}})
	op.must(200, "GET", base+"/sequences/"+rpSeq, nil)
	op.must(403, "GET", base+"/status", nil)
	owner.must(200, "PATCH", base+"/members/"+opID, map[string]any{"brand_ids": []string{}})
	op.must(200, "GET", base+"/status", nil)
	// Admins always work on every brand.
	owner.must(200, "PATCH", base+"/members/"+opID, map[string]any{"brand_ids": []string{"geonode"}})
	if m := owner.must(200, "PATCH", base+"/members/"+opID, map[string]any{"role": "admin"}); len(m["brand_ids"].([]any)) != 0 {
		t.Fatalf("promoted: %v", m)
	}
	owner.must(400, "PATCH", base+"/members/"+opID, map[string]any{"brand_ids": []string{"geonode"}})
	op.must(200, "GET", base+"/sequences/"+rpSeq, nil)

	if n := len(named(got(), "member.brands_changed")); n != 4 {
		t.Fatalf("member.brands_changed events: %d", n)
	}
}

func TestCrossBrandRuleReportsAndWebhooks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC)) // a Tuesday
	owner, base, srv := sendingSetup(t, e, clock)
	geonode := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode"})["id"].(string)
	repocket := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket"})["id"].(string)
	code, imp := owner.raw("POST", base+"/leads/import?list=Founders", "text/csv", "email,first_name\nli@one-io.example,Li\nmo@two-io.example,Mo\n")
	if code != 200 || imp["created"] != 2.0 {
		t.Fatalf("lead import: %d %v", code, imp)
	}
	list := imp["list_id"].(string)
	sequence := func(name, brand string) string {
		id := owner.must(201, "POST", base+"/sequences", map[string]any{
			"name": name, "timezone": "UTC", "send_start": "09:00", "send_end": "17:00", "target_daily": 100,
			"steps": []map[string]any{{"subject": "Hi {{first_name}}", "body": "Hi {{first_name}}"}},
		})["id"].(string)
		owner.must(200, "POST", base+"/brands/"+brand+"/add", map[string]any{"sequence_ids": []string{id}})
		owner.must(200, "POST", base+"/sequences/"+id+"/enrollments", map[string]any{"list_id": list})
		return id
	}

	// Webhooks: one for every brand, one for Geonode only.
	all := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/all", "events": []string{"sequence.*"}})
	geo := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/geo", "events": []string{"sequence.*"}, "brand": "geonode"})
	if all["brand_id"] != nil || geo["brand_id"] != geonode {
		t.Fatalf("webhook brands: %v %v", all["brand_id"], geo["brand_id"])
	}
	owner.must(400, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/x", "brand": "nope"})

	// The rule is off until set.
	if r := owner.must(200, "GET", base+"/brand-rules", nil); r["cross_brand_days"] != nil {
		t.Fatalf("rules: %v", r)
	}
	owner.must(400, "PATCH", base+"/brand-rules", map[string]any{"cross_brand_days": 0})
	owner.must(400, "PATCH", base+"/brand-rules", map[string]any{})

	// Geonode emails both people.
	geoSeq := sequence("Geonode founders", "geonode")
	owner.must(200, "PATCH", base+"/sequences/"+geoSeq, map[string]any{"status": "active"})
	e.pump(t)
	if n := len(srv.Messages()); n != 2 {
		t.Fatalf("geonode sent %d", n)
	}

	// Without the rule, Repocket could email them too; with it, it can't.
	check := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"li@one-io.example"}, "brand": "repocket"})["data"].([]any)[0].(map[string]any)
	if check["allowed"] != true {
		t.Fatalf("rule off: %v", check)
	}
	if r := owner.must(200, "PATCH", base+"/brand-rules", map[string]any{"cross_brand_days": 30}); r["cross_brand_days"] != 30.0 {
		t.Fatalf("set rule: %v", r)
	}
	check = owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"LI@one-io.example", "new@three-io.example"}, "brand": "repocket"})["data"].([]any)[0].(map[string]any)
	if check["allowed"] != false || fmt.Sprint(check["reasons"]) != "[other_brand:geonode]" {
		t.Fatalf("rule on: %v", check)
	}
	if check := owner.must(200, "POST", base+"/recipients/check", map[string]any{"emails": []string{"li@one-io.example"}, "brand": "geonode"})["data"].([]any)[0].(map[string]any); check["allowed"] != true {
		t.Fatalf("same brand: %v", check)
	}
	owner.must(404, "POST", base+"/recipients/check", map[string]any{"emails": []string{"li@one-io.example"}, "brand": "nope"})

	repSeq := sequence("Repocket founders", "repocket")
	owner.must(200, "PATCH", base+"/sequences/"+repSeq, map[string]any{"status": "active"})
	clock.Add(10 * time.Minute)
	e.pump(t)
	if n := len(srv.Messages()); n != 2 {
		t.Fatalf("repocket sent despite the rule: %d", n)
	}
	for email, en := range enrollments(t, owner, base, repSeq) {
		if en["status"] != "blocked" || en["status_reason"] != "other_brand:geonode" {
			t.Fatalf("%s: %v", email, en)
		}
	}
	blocked := named(got(), "message.blocked")
	if len(blocked) != 2 || blocked[0].Props["reason"] != "other_brand" || blocked[0].Props["brand_id"] != repocket {
		t.Fatalf("blocked events: %+v", blocked)
	}
	if ev := named(got(), "brand.rules_updated"); len(ev) != 1 {
		t.Fatalf("rules event: %+v", ev)
	}

	// Reports by brand.
	// (Sends are dated by the test clock, blocks by the database's.)
	byBrand := func(query string) map[string]map[string]any {
		rows := map[string]map[string]any{}
		for _, r := range owner.must(200, "GET", base+"/reports?group_by=brand"+query, nil)["rows"].([]any) {
			rows[r.(map[string]any)["name"].(string)] = r.(map[string]any)
		}
		return rows
	}
	if rows := byBrand("&from=2030-01-01&to=2030-01-31"); rows["Geonode"]["sent"] != 2.0 || rows["Repocket"] != nil {
		t.Fatalf("sends by brand: %v", rows)
	}
	if rows := byBrand(""); rows["Repocket"]["blocked"] != 2.0 || rows["Geonode"] != nil {
		t.Fatalf("blocks by brand: %v", rows)
	}
	if r := owner.must(200, "GET", base+"/reports?brand=repocket&from=2030-01-01&to=2030-01-31", nil); r["totals"].(map[string]any)["sent"] != 0.0 {
		t.Fatalf("repocket report: %v", r["totals"])
	}
	out := owner.must(200, "GET", base+"/outcomes/report?group_by=brand&from=2030-01-01&to=2030-01-31", nil)
	if r0 := out["rows"].([]any)[0].(map[string]any); r0["name"] != "Geonode" || r0["sent"] != 2.0 {
		t.Fatalf("outcomes by brand: %v", out["rows"])
	}
	if r := owner.must(200, "GET", base+"/outcomes/report?brand=repocket&from=2030-01-01&to=2030-01-31", nil); r["totals"].(map[string]any)["sent"] != 0.0 {
		t.Fatalf("repocket outcomes: %v", r["totals"])
	}

	// The audit log and events feed know each change's brand, and a brand
	// webhook hears only its brand.
	owner.must(200, "PATCH", base+"/sequences/"+repSeq, map[string]any{"name": "Repocket founders 2"})
	owner.must(200, "PATCH", base+"/sequences/"+geoSeq, map[string]any{"name": "Geonode founders 2"})
	audit := owner.must(200, "GET", base+"/audit-events?brand=repocket", nil)["data"].([]any)
	if len(audit) == 0 {
		t.Fatal("no repocket audit events")
	}
	for _, ev := range audit {
		if ev.(map[string]any)["brand_id"] != repocket {
			t.Fatalf("audit brand: %v", ev)
		}
	}
	feed := owner.must(200, "GET", base+"/events?types=sequence.updated&brand=geonode", nil)["data"].([]any)
	if len(feed) != 1 || feed[0].(map[string]any)["brand_id"] != geonode {
		t.Fatalf("events feed: %v", feed)
	}
	deliveries := func(id string) int {
		n := 0
		for _, d := range owner.must(200, "GET", base+"/webhooks/"+id+"/deliveries", nil)["data"].([]any) {
			if d.(map[string]any)["event_type"] == "sequence.updated" {
				n++
			}
		}
		return n
	}
	if a, g := deliveries(all["id"].(string)), deliveries(geo["id"].(string)); a != 2 || g != 1 {
		t.Fatalf("webhook deliveries: all %d, geonode %d", a, g)
	}
	owner.must(200, "PATCH", base+"/webhooks/"+geo["id"].(string), map[string]any{"brand": nil})
	if w := owner.must(200, "GET", base+"/webhooks/"+geo["id"].(string), nil); w["brand_id"] != nil {
		t.Fatalf("webhook brand cleared: %v", w)
	}
}
