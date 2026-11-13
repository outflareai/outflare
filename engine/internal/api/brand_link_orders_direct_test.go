package api_test

import (
	"testing"
)

// Direct-contact recommendations on the catalog: a site that is not a
// PBN, has DR or traffic stored, and has a stored, unsuppressed address at
// its own domain is marked; nothing is sent because of the mark.
func TestLinkOrderCatalogDirectContact(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"

	add := func(body map[string]any) string {
		t.Helper()
		return owner.must(201, "POST", root+"/catalog", body)["id"].(string)
	}
	// Only the intermediary's address is stored: not direct.
	broker := add(map[string]any{"domain": "broker-only.example", "domain_rating": 40, "notes": "Through Sam, sam@broker-mail.example"})
	// The site's own address in the notes, DR stored.
	notes := add(map[string]any{"domain": "notes.example", "domain_rating": 35, "notes": "Editor: Desk@Notes.example"})
	// Traffic only, an address at a subdomain.
	sub := add(map[string]any{"domain": "blog.example", "traffic": 1200, "notes": "write to editor@mail.blog.example"})
	// A PBN with everything else.
	pbn := add(map[string]any{"domain": "pbn.example", "pbn": true, "domain_rating": 50, "notes": "owner@pbn.example"})
	// No metrics stored, and metrics stored as zero.
	none := add(map[string]any{"domain": "nometrics.example", "notes": "hi@nometrics.example"})
	zero := add(map[string]any{"domain": "zero.example", "domain_rating": 0, "traffic": 0, "notes": "hi@zero.example"})
	// The only address is suppressed.
	supp := add(map[string]any{"domain": "suppressed.example", "domain_rating": 20, "notes": "only@suppressed.example"})
	owner.must(200, "POST", base+"/suppressions", map[string]any{"values": []string{"only@suppressed.example"}, "reason": "do_not_contact"})
	// An address that is a brand lead and a person, from Send to a sequence.
	lead := add(map[string]any{"domain": "lead.example", "domain_rating": 25})

	byID := func(rows []any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, x := range rows {
			d := x.(map[string]any)
			out[d["id"].(string)] = d["direct_contact"].(map[string]any)
		}
		return out
	}
	missing := func(dc map[string]any) []string {
		var out []string
		for _, m := range dc["missing"].([]any) {
			out = append(out, m.(string))
		}
		return out
	}
	all := byID(owner.must(200, "GET", root+"/catalog?limit=200", nil)["data"].([]any))
	want := map[string]struct {
		rec     bool
		emails  float64
		missing string
	}{
		broker: {false, 0, "no_email"},
		notes:  {true, 1, ""},
		sub:    {true, 1, ""},
		pbn:    {false, 1, "pbn"},
		none:   {false, 1, "no_metrics"},
		zero:   {false, 1, "no_metrics"},
		supp:   {false, 0, "no_email"},
		lead:   {false, 0, "no_email"},
	}
	for id, w := range want {
		dc := all[id]
		m := missing(dc)
		if dc["recommended"] != w.rec || dc["emails"] != w.emails || (w.missing == "" && len(m) != 0) || (w.missing != "" && (len(m) != 1 || m[0] != w.missing)) {
			t.Fatalf("%s: got %v, want %+v", id, dc, w)
		}
	}

	// Sending an address at the site to a sequence stores it, so the site
	// is now recommended. Marking itself enrolled nothing.
	seq := owner.must(201, "POST", "/v1/brands/"+brand+"/sequences", map[string]any{
		"name": "Donor outreach", "steps": []map[string]any{{"subject": "Hi", "body": "Hi"}},
	})["id"].(string)
	if n := len(owner.must(200, "GET", "/v1/brands/"+brand+"/sequences/"+seq+"/enrollments", nil)["data"].([]any)); n != 0 {
		t.Fatalf("enrollments before: %d", n)
	}
	owner.must(200, "POST", root+"/catalog/items/"+lead+"/outreach", map[string]any{"sequence_id": seq, "email": "editor@lead.example"})
	if dc := owner.must(200, "GET", root+"/catalog/items/"+lead, nil)["direct_contact"].(map[string]any); dc["recommended"] != true || dc["emails"] != float64(1) {
		t.Fatalf("lead after send: %v", dc)
	}

	// The filter lists exactly the recommendations, or the rest.
	ids := func(rows []any) map[string]bool {
		out := map[string]bool{}
		for _, x := range rows {
			out[x.(map[string]any)["id"].(string)] = true
		}
		return out
	}
	yes := ids(owner.must(200, "GET", root+"/catalog?direct_contact=true", nil)["data"].([]any))
	if len(yes) != 3 || !yes[notes] || !yes[sub] || !yes[lead] {
		t.Fatalf("direct_contact=true: %v", yes)
	}
	no := ids(owner.must(200, "GET", root+"/catalog?direct_contact=false", nil)["data"].([]any))
	if len(no) != 5 || no[notes] || no[sub] || no[lead] {
		t.Fatalf("direct_contact=false: %v", no)
	}
	owner.must(400, "GET", root+"/catalog?direct_contact=maybe", nil)
}
