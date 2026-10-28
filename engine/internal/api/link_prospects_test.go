package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestLinkProspectsAddScoreAndPitch(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)
	ctx := context.Background()

	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	// What is stored: a live link from linked.example (strength 70), a
	// tracked competitor and a suppressed domain.
	if _, err := e.pool.Exec(ctx, `INSERT INTO backlinks (workspace_id, site_id, source_url, target_url, source_domain, source_rating, domain_traffic)
		VALUES ($1, $2, 'https://linked.example/a', 'https://example.com/', 'linked.example', 70, 20000)`, ws, siteID); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": "rival.example"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason) VALUES ($1, 'domain', 'nope.example', 'do_not_contact'),
		($1, 'domain', 'partner.example', 'partner')`, ws); err != nil {
		t.Fatal(err)
	}

	add := func(want int, prospects ...map[string]any) map[string]any {
		t.Helper()
		return owner.must(want, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": prospects})
	}
	res := add(200,
		map[string]any{"page_url": "https://Blog.Example.org/best-tools#top", "method": "broken_link", "domain_rating": 60, "traffic": 1000,
			"target_url": "https://example.com/tools", "evidence": map[string]any{"broken_url": "https://gone.example/tool"}},
		map[string]any{"page_url": "https://blog.example.org/other", "method": "resource_page"},
		map[string]any{"page_url": "https://linked.example/b", "method": "competitor_gap"},
		map[string]any{"page_url": "https://linked.example/a", "method": "reclaim"},
		map[string]any{"page_url": "https://rival.example/blog", "method": "competitor_gap"},
		map[string]any{"page_url": "https://www.nope.example/", "method": "unlinked_mention"},
		map[string]any{"page_url": "https://shop.partner.example/", "method": "partner"},
		map[string]any{"page_url": "https://spam.example/", "spam_score": 80},
	)
	if res["added"].(float64) != 8 || res["existing"].(float64) != 0 || res["excluded"].(float64) != 5 {
		t.Fatalf("added: %v", res)
	}
	data := res["data"].([]any)
	first := data[0].(map[string]any)
	// 60/2 + log10(1001)/6*25 + 15 for a broken link.
	if first["page_url"] != "https://blog.example.org/best-tools" || first["domain"] != "blog.example.org" || first["stage"] != "prospect" ||
		first["score"].(float64) != 58 || len(first["score_factors"].([]any)) != 3 || first["target_url"] != "https://example.com/tools" ||
		first["source"] != "manual" || first["evidence"].(map[string]any)["broken_url"] != "https://gone.example/tool" {
		t.Fatalf("first: %v", first)
	}
	// Both pages on one website share its company record.
	if data[1].(map[string]any)["record_id"] != first["record_id"] {
		t.Fatalf("one company per website: %v", data[1])
	}
	want := []struct{ stage, reason string }{
		{"excluded", "links_to_us"}, {"prospect", ""}, {"excluded", "competitor"}, {"excluded", "opted_out"}, {"excluded", "partner"}, {"excluded", "spam"},
	}
	for i, w := range want {
		p := data[i+2].(map[string]any)
		reason, _ := p["excluded_reason"].(string)
		if p["stage"] != w.stage || reason != w.reason {
			t.Errorf("%s: %v %v, want %s %s", p["page_url"], p["stage"], p["excluded_reason"], w.stage, w.reason)
		}
	}
	// A winning-back prospect takes the stored numbers: 70/2 + traffic + 25.
	if r := data[3].(map[string]any); r["domain_rating"].(float64) != 70 || r["score"].(float64) != 35+18+25 {
		t.Fatalf("stored numbers: %v", r)
	}

	// Adding a page again keeps it as it is.
	if again := add(200, map[string]any{"page_url": "https://blog.example.org/best-tools", "method": "manual"}); again["added"].(float64) != 0 ||
		again["existing"].(float64) != 1 || again["data"].([]any)[0].(map[string]any)["method"] != "broken_link" {
		t.Fatalf("again: %v", again)
	}
	add(400, map[string]any{"page_url": "https://shop.example.com/x"})
	add(400, map[string]any{"page_url": "ftp://files.example.org/"})
	add(400, map[string]any{"page_url": "https://a.example.org/", "method": "bribe"})
	add(400, map[string]any{"page_url": "https://a.example.org/", "target_url": "https://elsewhere.example/"})
	owner.must(400, "POST", base+"/link-prospects", map[string]any{"site_id": siteID, "prospects": []any{}})

	// The list leaves out excluded ones and puts the best first.
	list := owner.must(200, "GET", base+"/link-prospects?site_id="+siteID, nil)
	if list["total"].(float64) != 3 || list["data"].([]any)[0].(map[string]any)["page_url"] != "https://linked.example/a" {
		t.Fatalf("list: %v", list)
	}
	if l := owner.must(200, "GET", base+"/link-prospects?stage=excluded&method=competitor_gap", nil); l["total"].(float64) != 2 {
		t.Fatalf("excluded: %v", l)
	}
	owner.must(400, "GET", base+"/link-prospects?stage=maybe", nil)
	owner.must(400, "GET", base+"/link-prospects?min_score=101", nil)

	// An excluded prospect is reopened before it is pitched.
	owner.must(409, "PATCH", base+"/link-prospects/"+data[2].(map[string]any)["id"].(string), map[string]any{"stage": "pitched"})

	// One pitch per website at a time, however it enters outreach.
	one, two := first["id"].(string), data[1].(map[string]any)["id"].(string)
	if p := owner.must(200, "PATCH", base+"/link-prospects/"+one, map[string]any{"stage": "pitched"}); p["stage"] != "pitched" || p["pitched_at"] == nil {
		t.Fatalf("pitched: %v", p)
	}
	if code := owner.must(409, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": "pitched"})["error"].(map[string]any)["code"]; code != "site_in_outreach" {
		t.Fatalf("second pitch: %v", code)
	}
	owner.must(409, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": "agreed"})
	// Declined starts the cool-down.
	owner.must(200, "PATCH", base+"/link-prospects/"+one, map[string]any{"stage": "declined"})
	owner.must(409, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": "pitched"})
	if _, err := e.pool.Exec(ctx, `UPDATE link_prospects SET ended_at = now() - interval '61 days' WHERE id = $1`, one); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": "pitched"})
	for _, st := range []string{"replied", "agreed", "live"} {
		owner.must(200, "PATCH", base+"/link-prospects/"+two, map[string]any{"stage": st})
	}
	if p := owner.must(200, "GET", base+"/link-prospects/"+two, nil); p["stage"] != "live" || p["agreed_at"] == nil || p["live_at"] == nil {
		t.Fatalf("live: %v", p)
	}
	// Opt-outs win, even after a prospect was added.
	reclaim := data[3].(map[string]any)["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO suppressions (workspace_id, kind, value, reason) VALUES ($1, 'domain', 'linked.example', 'unsubscribed')`, ws); err != nil {
		t.Fatal(err)
	}
	owner.must(409, "PATCH", base+"/link-prospects/"+reclaim, map[string]any{"stage": "pitched"})

	// Excluding needs a reason; edits and deletes.
	owner.must(400, "PATCH", base+"/link-prospects/"+reclaim, map[string]any{"stage": "excluded"})
	if p := owner.must(200, "PATCH", base+"/link-prospects/"+reclaim, map[string]any{"stage": "excluded", "excluded_reason": "opted_out"}); p["excluded_reason"] != "opted_out" {
		t.Fatalf("excluded: %v", p)
	}
	if p := owner.must(200, "PATCH", base+"/link-prospects/"+reclaim, map[string]any{"stage": "prospect", "note": " ask Jo "}); p["excluded_reason"] != nil || p["note"] != "ask Jo" {
		t.Fatalf("reopened: %v", p)
	}
	owner.must(400, "PATCH", base+"/link-prospects/"+reclaim, map[string]any{"target_url": "https://elsewhere.example/"})
	owner.must(204, "DELETE", base+"/link-prospects/"+reclaim, nil)
	owner.must(404, "GET", base+"/link-prospects/"+reclaim, nil)

	got := map[string]bool{}
	for _, ev := range events() {
		got[ev.Name] = true
		if ev.Name == "link_prospect.pitched" && (ev.Props["method"] != "broken_link" && ev.Props["method"] != "resource_page" || ev.Props["was"] == nil) {
			t.Errorf("pitched props: %v", ev.Props)
		}
	}
	for _, name := range []string{"link_prospect.found", "link_prospect.pitched", "link_prospect.declined", "link_prospect.replied", "link_prospect.agreed",
		"link_prospect.live", "link_prospect.excluded", "link_prospect.reopened", "link_prospect.deleted"} {
		if !got[name] {
			t.Errorf("no %s event", name)
		}
	}
}

func TestReclaimProspects(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	siteID := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	// Lost links: a recent one (its site still links elsewhere), one lost
	// long ago, one marked to disavow and a toxic one; and a live link.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO backlinks (workspace_id, site_id, source_url, target_url, source_domain, source_rating, status, lost_at, review, spam_score) VALUES
		($1, $2, 'https://blog.example.org/tools', 'https://example.com/guide', 'blog.example.org', 50, 'lost', now() - interval '10 days', '', 5),
		($1, $2, 'https://blog.example.org/home', 'https://example.com/', 'blog.example.org', 50, 'live', NULL, '', 5),
		($1, $2, 'https://old.example.org/a', 'https://example.com/', 'old.example.org', 70, 'lost', now() - interval '200 days', '', 5),
		($1, $2, 'https://bad.example.org/a', 'https://example.com/', 'bad.example.org', 70, 'lost', now() - interval '5 days', 'disavow', 5),
		($1, $2, 'https://spam.example.org/a', 'https://example.com/', 'spam.example.org', 70, 'lost', now() - interval '5 days', '', 90)`, ws, siteID); err != nil {
		t.Fatal(err)
	}
	res := owner.must(200, "POST", base+"/link-prospects/reclaim", map[string]any{"site_id": siteID})
	if res["added"].(float64) != 1 || res["excluded"].(float64) != 0 {
		t.Fatalf("reclaim: %v", res)
	}
	p := owner.must(200, "GET", base+"/link-prospects?method=reclaim", nil)["data"].([]any)[0].(map[string]any)
	if p["page_url"] != "https://blog.example.org/tools" || p["target_url"] != "https://example.com/guide" || p["stage"] != "prospect" || p["source"] != "reclaim" ||
		p["evidence"].(map[string]any)["backlink_id"] == nil {
		t.Fatalf("prospect: %v", p)
	}
	// Running it again adds nothing new; a wider window finds the old one.
	if again := owner.must(200, "POST", base+"/link-prospects/reclaim", map[string]any{"site_id": siteID, "days": 365}); again["added"].(float64) != 1 || again["existing"].(float64) != 1 {
		t.Fatalf("again: %v", again)
	}
	owner.must(400, "POST", base+"/link-prospects/reclaim", map[string]any{"site_id": siteID, "days": 0})
}
