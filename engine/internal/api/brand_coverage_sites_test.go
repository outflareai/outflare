package api_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A teammate claims a site they're talking to, so nobody else on the team
// reaches out to it: the claim is the owner of the site's company record,
// shown in Presence and the relationship check, and in the company's
// history.
func TestCoverageSiteClaims(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	mate, mateID := joinAs(t, owner, ws, "ana@acme.example", map[string]any{"role": "admin"})
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/coverage"
	if _, err := e.pool.Exec(ctx, `INSERT INTO placement_opportunities (workspace_id,brand_id,url,host,reason,action,score) VALUES
		($1,$2,'https://reviews.example/best','reviews.example','competitor_gap','pitch',80),
		($1,$2,'https://blog.example/post','blog.example','competitor_gap','pitch',40)`, ws, brand); err != nil {
		t.Fatal(err)
	}

	// Nobody manages either site yet.
	sites := owner.must(200, "GET", root+"/sites?host=reviews.example&host=www.blog.example", nil)["data"].([]any)
	if len(sites) != 2 || sites[0].(map[string]any)["owner_id"] != nil || sites[1].(map[string]any)["host"] != "blog.example" {
		t.Fatalf("sites: %v", sites)
	}
	owner.must(400, "GET", root+"/sites", nil)

	// The owner claims one: its company record is added and owned, and its
	// opportunities are theirs (still open, so they stay in the list).
	got := owner.must(200, "POST", root+"/sites/claim", map[string]any{"host": "https://www.reviews.example/best"})
	if got["host"] != "reviews.example" || got["owner_id"] != ownerID || got["owner_name"] != "Jack" || got["record_id"] == nil {
		t.Fatalf("claim: %v", got)
	}
	record := got["record_id"].(string)
	if got["owner_avatar_url"] != nil {
		t.Fatalf("no photo yet: %v", got)
	}
	// With a profile photo, the site and its opportunities show it.
	if _, err := e.pool.Exec(ctx, `UPDATE users SET avatar_url = 'https://lh3.googleusercontent.com/a/test-photo' WHERE id = $1`, ownerID); err != nil {
		t.Fatal(err)
	}
	rec := owner.must(200, "GET", base+"/records/"+record, nil)
	if rec["attributes"].(map[string]any)["owner"] != ownerID {
		t.Fatalf("company owner: %v", rec["attributes"])
	}
	mine := owner.must(200, "GET", root+"/opportunities?state=all&owner=me", nil)
	if mine["total"] != 1.0 {
		t.Fatalf("mine: %v", mine)
	}
	row := mine["data"].([]any)[0].(map[string]any)
	if row["state"] != "open" || row["owner_id"] != ownerID || row["site_owner_id"] != ownerID || row["site_owner_name"] != "Jack" || row["site_owner_avatar_url"] != "https://lh3.googleusercontent.com/a/test-photo" {
		t.Fatalf("claimed opportunity: %v", row)
	}
	if n := owner.must(200, "GET", root+"/opportunities?state=all&owner=none", nil)["total"]; n != 1.0 {
		t.Fatalf("unclaimed: %v", n)
	}
	if n := mate.must(200, "GET", root+"/opportunities?state=all&owner=others", nil)["total"]; n != 1.0 {
		t.Fatalf("others: %v", n)
	}
	owner.must(400, "GET", root+"/opportunities?owner=someone", nil)
	// Claiming it again changes nothing.
	owner.must(200, "POST", root+"/sites/claim", map[string]any{"host": "reviews.example"})

	// A teammate is told who manages it, and can take it over on purpose.
	status, body := mate.do("POST", root+"/sites/claim", map[string]any{"host": "reviews.example"})
	if status != 409 || body["error"].(map[string]any)["code"] != "site_claimed" || !strings.Contains(body["error"].(map[string]any)["message"].(string), "Jack") {
		t.Fatalf("claimed site: %d %v", status, body)
	}
	got = mate.must(200, "POST", root+"/sites/claim", map[string]any{"host": "reviews.example", "take_over": true})
	if got["owner_id"] != mateID || got["record_id"] != record {
		t.Fatalf("take over: %v", got)
	}
	// The relationship check warns the owner, now that a teammate has it.
	rel := owner.must(200, "GET", "/v1/brands/"+brand+"/records/"+record+"/relationship", nil)
	if rel["owner"].(map[string]any)["user_id"] != mateID || !slices.Contains(rel["flags"].([]any), any("claimed")) {
		t.Fatalf("relationship: %v", rel)
	}
	if rel := mate.must(200, "GET", "/v1/brands/"+brand+"/records/"+record+"/relationship", nil); slices.Contains(rel["flags"].([]any), any("claimed")) {
		t.Fatalf("own site flagged: %v", rel)
	}

	// Only with take_over can someone else release it; its manager can.
	owner.must(409, "POST", root+"/sites/claim", map[string]any{"host": "reviews.example", "owner_id": nil})
	got = mate.must(200, "POST", root+"/sites/claim", map[string]any{"host": "reviews.example", "owner_id": nil})
	if got["owner_id"] != nil {
		t.Fatalf("release: %v", got)
	}
	// Handing a site to a teammate.
	got = owner.must(200, "POST", root+"/sites/claim", map[string]any{"host": "blog.example", "owner_id": mateID})
	if got["owner_id"] != mateID {
		t.Fatalf("assign: %v", got)
	}
	owner.must(400, "POST", root+"/sites/claim", map[string]any{"host": "blog.example", "owner_id": "00000000-0000-0000-0000-000000000000", "take_over": true})
	owner.must(400, "POST", root+"/sites/claim", map[string]any{"host": "not a site"})

	// Every change is in the company's own history.
	var changes int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribute_changes WHERE record_id=$1 AND field_key='owner'`, record).Scan(&changes); err != nil {
		t.Fatal(err)
	}
	if changes != 3 {
		t.Fatalf("history: %d owner changes", changes)
	}
	for _, name := range []string{"coverage_site.claimed", "coverage_site.taken_over", "coverage_site.released"} {
		if len(named(events(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
