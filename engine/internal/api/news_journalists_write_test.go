package api_test

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

func beatsOf(j map[string]any) []string {
	var out []string
	for _, b := range j["beats"].([]any) {
		out = append(out, b.(string))
	}
	return out
}

func TestJournalistAddedByHand(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})

	// A journalist the news watch never found: a person on the outlet's
	// company, with no articles.
	j := owner.must(201, "POST", base+"/journalists", map[string]any{"name": "Ana Diaz", "email": "Ana@News.example.org",
		"outlet_website": "https://www.news.example.org/tech", "outlet_name": "Example News", "outlet_tier": "national",
		"job_title": "Technology reporter", "beats": []string{"Proxies", " proxies ", "Privacy"}, "brand_id": "northpeak"})
	id := j["record_id"].(string)
	outlets := j["outlet_records"].([]any)
	if j["email"] != "ana@news.example.org" || j["journalist"] != true || j["articles"] != 0.0 || j["latest_article"] != nil ||
		j["job_title"] != "Technology reporter" || !slices.Equal(beatsOf(j), []string{"Proxies", "Privacy"}) || len(outlets) != 1 ||
		outlets[0].(map[string]any)["tier"] != "national" || outlets[0].(map[string]any)["domain"] != "news.example.org" ||
		outlets[0].(map[string]any)["name"] != "Example News" || fmt.Sprint(j["outlets"]) != "[news.example.org]" {
		t.Fatalf("added: %v", j)
	}
	rec := owner.must(200, "GET", base+"/records/"+id, nil)
	if a := rec["attributes"].(map[string]any); a["journalist"] != true || a["first_name"] != "Ana" || a["last_name"] != "Diaz" ||
		rec["attribute_sources"].(map[string]any)["beats"] != "manual" {
		t.Fatalf("record: %v", rec)
	}

	// Adding them again finds the same person, and adds beats.
	again := owner.must(200, "POST", base+"/journalists", map[string]any{"name": "Ana Diaz", "email": "ana@news.example.org",
		"outlet_website": "news.example.org", "beats": []string{"AI"}, "brand_id": "northpeak"})
	if again["record_id"] != id || !slices.Equal(beatsOf(again), []string{"Proxies", "Privacy", "AI"}) || len(again["outlet_records"].([]any)) != 1 {
		t.Fatalf("again: %v", again)
	}

	// Someone already a person here becomes a journalist on their record.
	ben := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben Ito", "brand_id": "northpeak",
		"identities": []map[string]any{{"namespace": "email", "value": "ben@trade.example.net"}}})["id"].(string)
	if b := owner.must(200, "POST", base+"/journalists", map[string]any{"name": "Ben Ito", "email": "ben@trade.example.net",
		"outlet_website": "trade.example.net", "outlet_tier": "trade", "brand_id": "northpeak"}); b["record_id"] != ben {
		t.Fatalf("existing person: %v", b)
	}

	for q, want := range map[string]float64{"": 2, "?beat=privacy": 1, "?beat=Other": 0, "?tier=national": 1, "?tier=trade,national": 2, "?tier=local": 0} {
		if n := owner.must(200, "GET", base+"/journalists?brand=northpeak"+strings.Replace(q, "?", "&", 1), nil)["total"]; n != want {
			t.Fatalf("%q: %v", q, n)
		}
	}
	owner.must(400, "GET", base+"/journalists?brand=northpeak&tier=tabloid", nil)
	for _, bad := range []map[string]any{
		{"name": "No Email"},
		{"email": "x@news.example.org"},
		{"name": "X", "email": "x@news.example.org", "outlet_tier": "national"},
		{"name": "X", "email": "x@news.example.org", "outlet_website": "news.example.org", "outlet_tier": "tabloid"},
		{"name": "X", "email": "x@news.example.org", "outlet_website": "not a site"},
		{"name": "X", "email": "x@news.example.org", "beats": []string{""}},
	} {
		owner.must(400, "POST", base+"/journalists", bad)
	}

	// A person's edit replaces beats; job_title null clears it.
	upd := owner.must(200, "PATCH", base+"/journalists/"+id, map[string]any{"beats": []string{"Security"}, "job_title": nil})
	if !slices.Equal(beatsOf(upd), []string{"Security"}) || upd["job_title"] != nil {
		t.Fatalf("updated: %v", upd)
	}
	owner.must(400, "PATCH", base+"/journalists/"+id, map[string]any{})

	// Media lists by beat and outlet tier.
	media := func(rules ...map[string]any) int {
		list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": fmt.Sprint(rules), "brand_id": "northpeak", "rules": rules})["id"].(string)
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := smartlists.Refresh(ctx, tx, ws, list)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return len(owner.must(200, "GET", base+"/smart-lists/"+list+"/members", nil)["data"].([]any))
	}
	if n := media(map[string]any{"attribute": "beats", "op": "contains", "value": "security"},
		map[string]any{"company_attribute": "outlet_tier", "op": "eq", "value": "national"}); n != 1 {
		t.Fatalf("beat and tier list: %d", n)
	}
	if n := media(map[string]any{"attribute": "journalist", "op": "eq", "value": true},
		map[string]any{"company_attribute": "outlet_tier", "op": "neq", "value": "national"}); n != 1 {
		t.Fatalf("not national: %d", n)
	}
	if n := media(map[string]any{"attribute": "journalist", "op": "eq", "value": true},
		map[string]any{"company_attribute": "outlet_tier", "op": "not_exists"}); n != 0 {
		t.Fatalf("no tier: %d", n)
	}
	owner.must(400, "POST", base+"/smart-lists", map[string]any{"name": "Bad", "rules": []any{map[string]any{"company_attribute": "Bad Field", "op": "exists"}}})

	// Taken off the list, they stay a person.
	off := owner.must(200, "PATCH", base+"/journalists/"+id, map[string]any{"journalist": false})
	if off["journalist"] != false {
		t.Fatalf("unmarked: %v", off)
	}
	if n := owner.must(200, "GET", base+"/journalists?brand=northpeak", nil)["total"]; n != 1.0 {
		t.Fatalf("after unmarking: %v", n)
	}
	owner.must(200, "GET", base+"/records/"+id, nil)

	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["journalist.added"] != 3 || names["journalist.updated"] != 2 {
		t.Fatalf("events: %v", names)
	}
}

func TestJournalistImport(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	csv := "Email,Full Name,Job Title,Outlet,Outlet Website,Beats,Outlet Tier\n" +
		"ana@news.example.org,Ana Diaz,Reporter,Example News,news.example.org,Proxies; Privacy,National\n" +
		"ben@trade.example.net,Ben Ito,Editor,,trade.example.net,AI,trade\n" +
		"cy@blog.example.com,Cy Park,,,blog.example.com,,tabloid\n" +
		"not-an-email,Dee Roe,,,,,\n" +
		"ana@news.example.org,Ana Diaz,,,,Security,\n"

	// A dry run says what would happen and saves nothing.
	code, dry := owner.raw("POST", base+"/journalists/import?dry_run=true", "text/csv", csv)
	if code != 200 || dry["dry_run"] != true || dry["created"] != 2.0 || dry["updated"] != 0.0 || dry["duplicates"] != 1.0 ||
		len(dry["errors"].([]any)) != 2 || dry["batch_id"] != nil {
		t.Fatalf("dry run: %d %v", code, dry)
	}
	if n := owner.must(200, "GET", base+"/journalists", nil)["total"]; n != 0.0 {
		t.Fatalf("saved by a dry run: %v", n)
	}

	code, imp := owner.raw("POST", base+"/journalists/import", "text/csv", csv)
	if code != 200 || imp["created"] != 2.0 || imp["batch_id"] == nil || len(imp["errors"].([]any)) != 2 {
		t.Fatalf("import: %d %v", code, imp)
	}
	list := owner.must(200, "GET", base+"/journalists", nil)
	if list["total"] != 2.0 {
		t.Fatalf("imported: %v", list)
	}
	for _, x := range list["data"].([]any) {
		j := x.(map[string]any)
		if j["email"] == "ana@news.example.org" && (!slices.Equal(beatsOf(j), []string{"Proxies", "Privacy", "Security"}) ||
			j["outlet_records"].([]any)[0].(map[string]any)["tier"] != "national" || j["job_title"] != "Reporter") {
			t.Fatalf("ana: %v", j)
		}
	}
	// Again: the same people, beats added to.
	_, imp = owner.raw("POST", base+"/journalists/import", "text/csv", "email,name,beats\nben@trade.example.net,Ben Ito,Startups\n")
	if imp["created"] != 0.0 || imp["updated"] != 1.0 {
		t.Fatalf("again: %v", imp)
	}
	if n := owner.must(200, "GET", base+"/journalists?beat=startups", nil)["total"]; n != 1.0 {
		t.Fatalf("beat added: %v", n)
	}
	if code, _ := owner.raw("POST", base+"/journalists/import", "text/csv", "name\nAna\n"); code != 400 {
		t.Fatalf("no email column: %d", code)
	}
	if code, _ := owner.raw("POST", base+"/journalists/import", "application/json", "{}"); code != 415 {
		t.Fatalf("not csv: %d", code)
	}
	names := map[string]int{}
	for _, ev := range got() {
		names[ev.Name]++
	}
	if names["journalist.imported"] != 2 {
		t.Fatalf("events: %v", names)
	}
}

func TestJournalistsKeepToTheirBrand(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})
	theirs := owner.must(201, "POST", base+"/journalists", map[string]any{"name": "Ana Diaz", "email": "ana@news.example.org",
		"outlet_website": "news.example.org", "brand_id": "northpeak"})["record_id"].(string)

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "acme", "scopes": []string{"seo.manage"}, "brand": "acme"})
	agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	// Its own journalist goes in its brand; the same email there is
	// another person.
	mine := agent.must(201, "POST", base+"/journalists", map[string]any{"name": "Ana Diaz", "email": "ana@news.example.org", "outlet_website": "news.example.org"})
	if mine["brand_id"] != acme || mine["record_id"] == theirs {
		t.Fatalf("brand-limited add: %v", mine)
	}
	agent.must(400, "POST", base+"/journalists", map[string]any{"name": "Ben Ito", "email": "ben@news.example.org", "brand_id": "northpeak"})
	list := agent.must(200, "GET", base+"/journalists", nil)
	if list["total"] != 1.0 || list["data"].([]any)[0].(map[string]any)["record_id"] != mine["record_id"] {
		t.Fatalf("brand-limited list: %v", list)
	}
	agent.must(404, "PATCH", base+"/journalists/"+theirs, map[string]any{"beats": []string{"AI"}})
	if n := owner.must(200, "GET", base+"/journalists?brand=acme", nil)["total"]; n != 1.0 {
		t.Fatalf("owner's list: %v", n)
	}
	if n := owner.must(200, "GET", base+"/journalists?brand=northpeak", nil)["total"]; n != 1.0 {
		t.Fatalf("one brand's list: %v", n)
	}
}
