package api_test

import (
	"testing"
)

// A catalog site carries the latest Outreach enrollment of someone at it,
// so Recommended donors can leave out sites already being written to: an
// address at the site's domain or one filed under the site as its company
// (a free-mail address) counts; stopping the enrollment or archiving the
// sequence takes the site back out of outreach.
func TestLinkOrderCatalogOutreachState(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	add := func(domain string) string {
		t.Helper()
		return owner.must(201, "POST", root+"/catalog", map[string]any{"domain": domain, "domain_rating": 40})["id"].(string)
	}
	own := add("own.example")
	free := add("free.example")
	quiet := add("quiet.example")

	seq := owner.must(201, "POST", "/v1/brands/"+brand+"/sequences", map[string]any{
		"name": "Paid articles", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}},
	})["id"].(string)
	outreach := func(id string) map[string]any {
		t.Helper()
		o, _ := owner.must(200, "GET", root+"/catalog/items/"+id, nil)["outreach"].(map[string]any)
		return o
	}
	if outreach(own) != nil || outreach(free) != nil || outreach(quiet) != nil {
		t.Fatal("outreach before any send")
	}

	owner.must(200, "POST", root+"/catalog/items/"+own+"/outreach", map[string]any{"sequence_id": seq, "email": "ads@own.example"})
	owner.must(200, "POST", root+"/catalog/items/"+free+"/outreach", map[string]any{"sequence_id": seq, "email": "free.owner@freemail.example"})

	o := outreach(own)
	if o == nil || o["sequence_id"] != seq || o["sequence_name"] != "Paid articles" || o["sequence_status"] != "draft" || o["status"] != "active" || o["email"] != "ads@own.example" || o["enrolled_at"] == nil {
		t.Fatalf("own: %v", o)
	}
	if o := outreach(free); o == nil || o["email"] != "free.owner@freemail.example" {
		t.Fatalf("free-mail filed under the site: %v", o)
	}
	if outreach(quiet) != nil {
		t.Fatal("quiet site has outreach")
	}
	// The list carries it too.
	for _, x := range owner.must(200, "GET", root+"/catalog?limit=200", nil)["data"].([]any) {
		d := x.(map[string]any)
		if want := d["id"] != quiet; want != (d["outreach"] != nil) {
			t.Fatalf("list: %v", d)
		}
	}

	// Stopping the enrollment takes the site out of outreach.
	var eid string
	for _, x := range owner.must(200, "GET", "/v1/brands/"+brand+"/sequences/"+seq+"/enrollments", nil)["data"].([]any) {
		en := x.(map[string]any)
		if en["email"] == "ads@own.example" {
			eid = en["id"].(string)
		}
	}
	if eid == "" {
		t.Fatal("enrollment not found")
	}
	owner.must(204, "POST", "/v1/brands/"+brand+"/sequences/"+seq+"/enrollments/"+eid+"/stop", nil)
	if o := outreach(own); o != nil {
		t.Fatalf("own after stop: %v", o)
	}

	// Archiving the sequence does the same for everyone in it.
	owner.must(200, "PATCH", "/v1/brands/"+brand+"/sequences/"+seq, map[string]any{"status": "archived"})
	if o := outreach(free); o != nil {
		t.Fatalf("free after archive: %v", o)
	}
}
