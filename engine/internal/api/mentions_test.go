package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Mentions listen for the brand's name and its tracked competitors', read
// what Social watches, news searches and Presence already found, and keep
// each place a name was said, once. Off until turned on, and each brand
// sees only its own.
func TestBrandMentions(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/mentions"
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	rival := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, aliases, status) VALUES ($1, $2, $3, 'rivalcorp.example', 'Rival Corp', '{Rival}', 'tracked')`, ws, site, brand)
	exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'ignored.example', 'Ignored', 'dismissed')`, ws, site, brand)

	// Off at first, listening for the brand and its tracked competitor.
	o := owner.must(200, "GET", root+"/overview", nil)
	listen := o["listen_for"].([]any)
	if o["enabled"] != false || o["next_scan_at"] != nil || len(listen) != 2 || listen[0].(map[string]any)["about"] != "brand" ||
		listen[1].(map[string]any)["competitor_id"] != rival || len(listen[1].(map[string]any)["terms"].([]any)) != 2 {
		// Only the brand name and the site: the alias "Rival" is not a term.
		t.Fatalf("overview: %v", o)
	}

	// Changing what is listened for: a term added by hand, an automatic
	// one skipped, a row off and on again.
	lf := root + "/listen-for"
	owner.must(404, "PATCH", lf, map[string]any{"competitor_id": "00000000-0000-0000-0000-000000000001", "listening": false})
	owner.must(400, "PATCH", lf, map[string]any{"competitor_id": rival, "added_terms": []string{"NP"}})
	o = owner.must(200, "PATCH", lf, map[string]any{"competitor_id": rival, "added_terms": []string{"Rival", "rival"}, "skipped_terms": []string{"rivalcorp.example"}})
	row := o["listen_for"].([]any)[1].(map[string]any)
	if fmt.Sprint(row["terms"]) != "[Rival Corp Rival]" || fmt.Sprint(row["added_terms"]) != "[Rival]" || row["listening"] != true {
		t.Fatalf("added: %v", row)
	}
	o = owner.must(200, "PATCH", lf, map[string]any{"competitor_id": rival, "listening": false})
	if row := o["listen_for"].([]any)[1].(map[string]any); row["listening"] != false || len(row["terms"].([]any)) != 0 || fmt.Sprint(row["added_terms"]) != "[Rival]" {
		t.Fatalf("off: %v", row)
	}
	if subs, err := e.srv.MentionSubjectsForTest(ctx, ws, brand); err != nil || len(subs) != 1 {
		t.Fatalf("subjects while off: %v, %v", subs, err)
	}
	owner.must(200, "PATCH", lf, map[string]any{"competitor_id": rival, "listening": true, "skipped_terms": []string{}})
	if b := owner.must(200, "PATCH", lf, map[string]any{"competitor_id": nil, "listening": true})["listen_for"].([]any)[0].(map[string]any); b["listening"] != true {
		t.Fatalf("brand row: %v", b)
	}
	owner.must(409, "POST", root+"/scan", nil)
	owner.must(400, "PATCH", root+"/settings", map[string]any{"sources": []string{"tiktok"}})
	owner.must(400, "PATCH", root+"/settings", map[string]any{"scan_every_hours": 0})

	// What sources already found.
	watch := exec(`INSERT INTO social_watches (workspace_id, brand_id, channel, name, keywords, disclosure) VALUES ($1, $2, 'reddit', 'Proxies', '{proxy}', 'I work at Northpeak')`, ws, brand)
	exec(`INSERT INTO social_items (workspace_id, brand_id, channel, watch_id, external_id, title, body, author, url, posted_at)
		VALUES ($1, $2, 'reddit', $3, 't3_a', 'Best proxy?', 'I moved from Rival to Northpeak last month and it is faster.', 'ana_example', 'https://reddit.example/r/a', now())`, ws, brand, watch)
	exec(`INSERT INTO social_items (workspace_id, brand_id, channel, watch_id, external_id, title, body, author, url, posted_at)
		VALUES ($1, $2, 'reddit', $3, 't3_b', 'Unrelated', 'Nothing to see.', 'bo', 'https://reddit.example/r/b', now())`, ws, brand, watch)
	exec(`INSERT INTO coverage_competitor_placements (workspace_id, brand_id, competitor_id, url, host, title, kind)
		VALUES ($1, $2, $3, 'https://www.youtube.com/watch?v=abc&t=95', 'youtube.com', 'Rival Corp review', '')`, ws, brand, rival)
	exec(`INSERT INTO coverage_competitor_placements (workspace_id, brand_id, competitor_id, url, host, title, kind)
		VALUES ($1, $2, $3, 'https://blog.example/top-10', 'blog.example', 'Top 10 tools', 'listicle')`, ws, brand, rival)

	// Turning it on queues a scan; the scan keeps three mentions.
	o = owner.must(200, "PATCH", root+"/settings", map[string]any{"enabled": true})
	if o["enabled"] != true || o["next_scan_at"] == nil {
		t.Fatalf("on: %v", o)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'mentions.scan' AND payload->>'brand_id' = $1`, brand).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("queued scans: %d, %v", queued, err)
	}
	n, err := e.srv.ScanBrandMentions(ctx, ws, brand)
	if err != nil || n != 3 {
		t.Fatalf("scan kept %d, %v", n, err)
	}
	// Scanning again keeps nothing twice.
	if n, err := e.srv.ScanBrandMentions(ctx, ws, brand); err != nil || n != 0 {
		t.Fatalf("second scan kept %d, %v", n, err)
	}
	list := owner.must(200, "GET", root, nil)["data"].([]any)
	if len(list) != 3 {
		t.Fatalf("mentions: %v", list)
	}
	var video map[string]any
	for _, m := range list {
		if m := m.(map[string]any); m["source"] == "youtube" {
			video = m
		}
	}
	if video == nil || video["about"] != "competitor" || video["at_seconds"] != 95.0 || video["competitor"].(map[string]any)["name"] != "Rival Corp" {
		t.Fatalf("video mention: %v", list)
	}
	comp := owner.must(200, "GET", root+"?about=competitor&source=reddit", nil)["data"].([]any)
	if len(comp) != 1 || comp[0].(map[string]any)["matched"] != "Rival" || comp[0].(map[string]any)["author"].(map[string]any)["url"] != "https://www.reddit.com/user/ana_example" {
		t.Fatalf("reddit competitor mention: %v", comp)
	}
	owner.must(400, "GET", root+"?source=tiktok", nil)
	page := owner.must(200, "GET", root+"?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("page: %v", page)
	}
	rest := owner.must(200, "GET", root+"?limit=2&cursor="+page["next_cursor"].(string), nil)
	if len(rest["data"].([]any)) != 1 || rest["next_cursor"] != nil {
		t.Fatalf("second page: %v", rest)
	}

	// Moving one along, and the totals.
	id := video["id"].(string)
	owner.must(400, "PATCH", root+"/"+id, map[string]any{"status": "bought"})
	if m := owner.must(200, "PATCH", root+"/"+id, map[string]any{"status": "contacted"}); m["status"] != "contacted" {
		t.Fatalf("contacted: %v", m)
	}
	o = owner.must(200, "GET", root+"/overview", nil)
	totals := o["totals"].(map[string]any)
	if totals["total"] != 3.0 || totals["contacted"] != 1.0 || totals["new"] != 2.0 || totals["people"] != 2.0 {
		t.Fatalf("totals: %v", totals)
	}
	owner.must(202, "POST", root+"/scan", nil)

	// Another brand sees none of it.
	owner.must(404, "GET", "/v1/brands/"+other+"/mentions/"+id, nil)
	owner.must(404, "PATCH", "/v1/brands/"+other+"/mentions/"+id, map[string]any{"status": "new"})
	if l := owner.must(200, "GET", "/v1/brands/"+other+"/mentions", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("other brand: %v", l)
	}

	// Off stops scans.
	owner.must(200, "PATCH", root+"/settings", map[string]any{"enabled": false})
	if n, _ := e.srv.ScanBrandMentions(ctx, ws, brand); n != 0 {
		t.Fatal("scanned while off")
	}
	want := map[string]bool{"brand_mentions.listen_for_updated": false, "brand_mentions.enabled": false, "brand_mentions.scanned": false, "brand_mention.status_changed": false,
		"brand_mentions.scan_requested": false, "brand_mentions.disabled": false}
	for _, ev := range events() {
		if _, ok := want[ev.Name]; ok {
			want[ev.Name] = true
		}
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("no %s event", name)
		}
	}
}
