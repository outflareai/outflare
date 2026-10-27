package api_test

import "testing"

func TestWorkspaceSlugs(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	// The slug works in place of the ID, in any case.
	if got := owner.must(200, "GET", "/v1/workspaces/acme-outbound", nil)["id"]; got != ws {
		t.Fatalf("by slug: %v", got)
	}
	owner.must(200, "GET", "/v1/workspaces/ACME-Outbound/members", nil)
	owner.must(404, "GET", "/v1/workspaces/nobody-here", nil)
	owner.must(404, "GET", "/v1/workspaces/x", nil)

	// Names that are reserved, too short or the wrong shape get a new slug.
	for name, want := range map[string]string{"Admin": "admin-workspace", "AB": "ab-workspace", "!!!": "workspace", "Linear & Co.": "linear-co"} {
		if got := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": name})["slug"]; got != want {
			t.Fatalf("%q: slug %v, want %v", name, got, want)
		}
	}
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Geonode"})["id"].(string)

	// Changing it: shape, reserved words and taken slugs are refused.
	owner.must(400, "PATCH", base, map[string]string{"slug": "a"})
	owner.must(400, "PATCH", base, map[string]string{"slug": "-acme"})
	owner.must(400, "PATCH", base, map[string]string{"slug": "acme_co"})
	owner.must(400, "PATCH", base, map[string]string{"slug": "signup"})
	owner.must(400, "PATCH", base, map[string]string{"slug": ws})
	owner.must(400, "PATCH", base, map[string]string{"name": " "})
	if code, out := owner.do("PATCH", base, map[string]string{"slug": "geonode"}); code != 409 || out["error"].(map[string]any)["code"] != "slug_taken" {
		t.Fatalf("taken: %d %v", code, out)
	}
	if out := owner.must(200, "GET", base+"/slug-availability?slug=geonode", nil); out["available"] != false || out["reason"] != "slug is already taken" {
		t.Fatalf("availability taken: %v", out)
	}
	if out := owner.must(200, "GET", base+"/slug-availability?slug=Acme", nil); out["available"] != true || out["slug"] != "acme" {
		t.Fatalf("availability free: %v", out)
	}
	if out := owner.must(200, "GET", base+"/slug-availability?slug=login", nil); out["available"] != false {
		t.Fatalf("availability reserved: %v", out)
	}

	out := owner.must(200, "PATCH", base, map[string]string{"name": "Acme", "slug": "Acme"})
	if out["name"] != "Acme" || out["slug"] != "acme" || out["id"] != ws {
		t.Fatalf("patch: %v", out)
	}
	owner.must(200, "GET", "/v1/workspaces/acme/stats", nil)
	// The old slug still leads here, until another workspace takes it.
	if got := owner.must(200, "GET", "/v1/workspaces/acme-outbound", nil); got["id"] != ws || got["slug"] != "acme" {
		t.Fatalf("old slug: %v", got)
	}
	owner.must(200, "GET", "/v1/workspaces/"+other+"/slug-availability?slug=acme-outbound", nil)
	owner.must(200, "PATCH", "/v1/workspaces/"+other, map[string]string{"slug": "acme-outbound"})
	if got := owner.must(200, "GET", "/v1/workspaces/acme-outbound", nil)["id"]; got != other {
		t.Fatalf("claimed slug: %v", got)
	}
	owner.must(200, "PATCH", "/v1/workspaces/"+other, map[string]string{"slug": "geonode"})
	owner.must(200, "PATCH", "/v1/workspaces/acme", map[string]string{"slug": "acme"}) // no change

	events := owner.must(200, "GET", base+"/audit-events", nil)["data"].([]any)
	if a := events[0].(map[string]any); a["action"] != "workspace.updated" {
		t.Fatalf("audit: %v", a)
	}
	if n := len(events); n > 1 && events[1].(map[string]any)["action"] == "workspace.updated" {
		t.Fatal("an unchanged PATCH was audited")
	}

	// A token for one workspace cannot use another's slug; workspace.manage
	// is for owners and admins.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "bot", "scopes": []string{"workspace.read"}})
	bot := &client{t: t, h: h, bearer: tok["token"].(string)}
	bot.must(200, "GET", "/v1/workspaces/acme", nil)
	bot.must(404, "GET", "/v1/workspaces/geonode", nil)
	bot.must(403, "PATCH", "/v1/workspaces/acme", map[string]string{"name": "Nope"})

	inv := owner.must(201, "POST", "/v1/workspaces/"+other+"/invites", map[string]string{"email": "op@example.com", "role": "operator"})
	op := &client{t: t, h: h}
	op.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Op", "password": "op long password"})
	op.must(404, "GET", "/v1/workspaces/acme", nil)
	op.must(200, "GET", "/v1/workspaces/geonode", nil)
	op.must(403, "PATCH", "/v1/workspaces/geonode", map[string]string{"name": "Mine"})
}

func TestItemLinks(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	steps := []map[string]any{{"subject": "Hi", "body": "Hello"}}
	a := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Q3 founders", "steps": steps})["id"].(string)
	b := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Q3 Founders!", "steps": steps})["id"].(string)

	link := func(ref string) map[string]any { return owner.must(200, "GET", base+"/links/sequence/"+ref, nil) }
	if got := link(a); got["slug"] != "q3-founders" || got["id"] != a || got["name"] != "Q3 founders" {
		t.Fatalf("first: %v", got)
	}
	if got := link(b)["slug"]; got != "q3-founders-2" {
		t.Fatalf("second: %v", got)
	}
	if got := link("Q3-Founders")["id"]; got != a {
		t.Fatalf("by slug: %v", got)
	}

	// Renamed: a new slug, and the old one still finds it.
	owner.must(200, "PATCH", base+"/sequences/"+a, map[string]any{"name": "Q4 founders"})
	if got := link("q3-founders"); got["slug"] != "q4-founders" || got["id"] != a {
		t.Fatalf("renamed: %v", got)
	}
	if got := link("q4-founders")["id"]; got != a {
		t.Fatalf("new slug: %v", got)
	}
	// Renamed back, it takes its first slug again.
	owner.must(200, "PATCH", base+"/sequences/"+a, map[string]any{"name": "Q3 founders"})
	if got := link(a)["slug"]; got != "q3-founders" {
		t.Fatalf("renamed back: %v", got)
	}

	owner.must(404, "GET", base+"/links/sequence/nothing-here", nil)
	owner.must(404, "GET", base+"/links/project/"+a, nil) // not a project
	owner.must(400, "GET", base+"/links/widget/"+a, nil)
	proj := owner.must(201, "POST", base+"/projects", map[string]any{"name": "New"})["id"].(string)
	if got := owner.must(200, "GET", base+"/links/project/"+proj, nil)["slug"]; got != "new-project" {
		t.Fatalf("reserved item slug: %v", got)
	}

	// Another workspace cannot see these, and the item's own scope is needed.
	other := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Other"})["id"].(string)
	owner.must(404, "GET", "/v1/workspaces/"+other+"/links/sequence/q3-founders", nil)
	owner.must(404, "GET", "/v1/workspaces/"+other+"/links/sequence/"+a, nil)
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "bot", "scopes": []string{"workspace.read"}})
	bot := &client{t: t, h: h, bearer: tok["token"].(string)}
	bot.must(403, "GET", base+"/links/sequence/q3-founders", nil)
}

func TestConnectionLinks(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	link := func(kind, ref string) map[string]any { return owner.must(200, "GET", base+"/links/"+kind+"/"+ref, nil) }
	connect := func(name string) string {
		return owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": name, "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	}

	// A connection's slug is its connector, whatever it is called.
	a, b := connect("Main site"), connect("Blog")
	if got := link("connection", a); got["slug"] != "plausible" || got["name"] != "Main site" {
		t.Fatalf("first: %v", got)
	}
	if got := link("connection", b)["slug"]; got != "plausible-2" {
		t.Fatalf("second: %v", got)
	}
	owner.must(200, "PATCH", base+"/integrations/"+a, map[string]any{"name": "Marketing site"})
	if got := link("connection", "plausible"); got["id"] != a || got["slug"] != "plausible" {
		t.Fatalf("renamed: %v", got)
	}

	// Deleted and connected again: the new one takes the free slug.
	owner.must(204, "DELETE", base+"/integrations/"+a, nil)
	owner.must(404, "GET", base+"/links/connection/plausible", nil)
	c := connect("Again")
	if got := link("connection", c)["slug"]; got != "plausible" {
		t.Fatalf("reconnected: %v", got)
	}
	if got := link("connection", "plausible-2")["id"]; got != b {
		t.Fatalf("other kept its slug: %v", got)
	}
	owner.must(404, "GET", base+"/links/connection/posthog", nil)

	// Underscores, as in catalog ids such as google_calendar, read as dashes.
	if got := link("connection", "Plausible_2")["id"]; got != b {
		t.Fatalf("underscore: %v", got)
	}

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Zapier bot", "scopes": []string{"workspace.read"}})
	if got := link("token", tok["api_token"].(map[string]any)["id"].(string))["slug"]; got != "zapier-bot" {
		t.Fatalf("token: %v", got)
	}
	hook := owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://hooks.example.com/x", "events": []string{"lead.*"}})
	if got := link("webhook", hook["id"].(string))["slug"]; got != "hooks-example-com" {
		t.Fatalf("webhook: %v", got)
	}

	// Each kind needs its own scope.
	bot := &client{t: t, h: h, bearer: tok["token"].(string)}
	bot.must(403, "GET", base+"/links/connection/plausible", nil)
	bot.must(403, "GET", base+"/links/token/zapier-bot", nil)
}
