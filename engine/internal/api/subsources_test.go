package api_test

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Sub-sources: the sales team adds the people they met at an event with
// Source Events and conferences and the event's name, picked from the
// brand's list or typed new, and finds them again by that event.
func TestSubSourcesForEvents(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	bb := brandBase(t, e, ws)

	// Source says Events and conferences, and Sub-source is a built-in field.
	cat := owner.must(200, "GET", bb+"/catalog", nil)
	labelled, field := false, false
	for _, s := range cat["option_sets"].([]any) {
		m := s.(map[string]any)
		if m["key"] == "person_source" {
			for _, o := range m["options"].([]any) {
				if o.(map[string]any)["key"] == "event" && o.(map[string]any)["label"] == "Events and conferences" {
					labelled = true
				}
			}
		}
	}
	for _, ty := range cat["types"].([]any) {
		if ty.(map[string]any)["key"] != "person" {
			continue
		}
		for _, f := range ty.(map[string]any)["fields"].([]any) {
			if f.(map[string]any)["key"] == "sub_source" {
				field = true
			}
		}
	}
	if !labelled || !field {
		t.Fatalf("catalog: Events and conferences %v, Sub-source field %v", labelled, field)
	}

	// Typing the same name twice is the same event.
	ws1 := owner.must(201, "POST", bb+"/sub-sources", map[string]any{"source": "event", "label": "  Web   Summit 2026 "})
	if ws1["key"] != "web_summit_2026" || ws1["label"] != "Web Summit 2026" || ws1["source"] != "event" {
		t.Fatalf("add: %v", ws1)
	}
	if again := owner.must(200, "POST", bb+"/sub-sources", map[string]any{"source": "event", "label": "web summit 2026"}); again["key"] != "web_summit_2026" {
		t.Fatalf("the same name again: %v", again)
	}
	if n := named(got(), "sub_source.created"); len(n) != 1 || n[0].WorkspaceID != ws {
		t.Fatalf("sub_source.created events: %+v", n)
	}
	owner.must(201, "POST", bb+"/sub-sources", map[string]any{"source": "inbound", "label": "Webinar"})
	owner.must(400, "POST", bb+"/sub-sources", map[string]any{"source": "lead", "label": "X"})
	owner.must(400, "POST", bb+"/sub-sources", map[string]any{"source": "event", "label": "  "})

	// A person added by hand with the event.
	ana := owner.must(201, "POST", bb+"/records", map[string]any{"type": "person", "name": "Ana Silva",
		"attributes": map[string]any{"source": "event", "sub_source": "web_summit_2026"},
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})
	if a := ana["attributes"].(map[string]any); a["sub_source"] != "web_summit_2026" {
		t.Fatalf("add a person with the event: %v", a)
	}
	owner.must(400, "POST", bb+"/records", map[string]any{"type": "person", "name": "Nope",
		"attributes": map[string]any{"source": "event", "sub_source": "not_an_event"}})

	// Several at once from the event, with names, titles and companies.
	imp := owner.must(200, "POST", bb+"/records/import", map[string]any{"sub_source": "web_summit_2026", "people": []map[string]any{
		{"email": "ben@northpeak.example", "first_name": "Ben", "last_name": "Okafor", "job_title": "CTO", "company": "Northpeak"},
		{"email": "cy@northpeak.example", "first_name": "Cy", "company": "northpeak"},
		{"email": "ana@acme.example"},
	}})
	if imp["created"] != 2.0 || imp["matched"] != 1.0 {
		t.Fatalf("import: %v", imp)
	}
	ids := imp["record_ids"].([]any)
	ben := owner.must(200, "GET", bb+"/records/"+ids[0].(string), nil)
	cy := owner.must(200, "GET", bb+"/records/"+ids[1].(string), nil)
	ba, ca := ben["attributes"].(map[string]any), cy["attributes"].(map[string]any)
	if ba["source"] != "event" || ba["sub_source"] != "web_summit_2026" || ba["job_title"] != "CTO" || ba["first_name"] != "Ben" || ben["name"] != "Ben Okafor" {
		t.Fatalf("Ben: %v %v", ben["name"], ba)
	}
	bc, _ := ba["companies"].([]any)
	cc, _ := ca["companies"].([]any)
	if len(bc) != 1 || len(cc) != 1 || bc[0] != cc[0] {
		t.Fatalf("one Northpeak for both: %v %v", bc, cc)
	}
	hist := owner.must(200, "GET", bb+"/records/"+ids[0].(string)+"/history", nil)
	seen := false
	for _, h := range hist["data"].([]any) {
		if m, _ := h.(map[string]any); m["field_key"] == "sub_source" || m["field"] == "sub_source" {
			seen = true
		}
	}
	if !seen {
		t.Fatalf("no Sub-source change in Ben's history: %v", hist)
	}
	// A sub-source of another Source doesn't fit.
	owner.must(400, "POST", bb+"/records/import", map[string]any{"source": "cold", "sub_source": "web_summit_2026", "people": []map[string]any{{"email": "x@northpeak.example"}}})
	owner.must(400, "POST", bb+"/records/import", map[string]any{"sub_source": "nope", "people": []map[string]any{{"email": "x@northpeak.example"}}})

	// Filter People by the event, and count them.
	at := owner.must(200, "GET", bb+"/records?type=person&attribute=sub_source:web_summit_2026", nil)["data"].([]any)
	if len(at) != 3 {
		t.Fatalf("people from Web Summit: %d", len(at))
	}
	list := owner.must(200, "GET", bb+"/sub-sources?source=event", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["people"] != 3.0 {
		t.Fatalf("event list: %v", list)
	}

	// Rename fixes the name everywhere; archived ones can't be picked.
	if r := owner.must(200, "PATCH", bb+"/sub-sources/web_summit_2026", map[string]any{"label": "Web Summit Lisbon 2026"}); r["label"] != "Web Summit Lisbon 2026" {
		t.Fatalf("rename: %v", r)
	}
	owner.must(201, "POST", bb+"/sub-sources", map[string]any{"source": "event", "label": "SaaStr"})
	owner.must(409, "PATCH", bb+"/sub-sources/saastr", map[string]any{"label": "web summit lisbon 2026"})
	owner.must(200, "PATCH", bb+"/sub-sources/saastr", map[string]any{"archived": true})
	if l := owner.must(200, "GET", bb+"/sub-sources?source=event", nil)["data"].([]any); len(l) != 1 {
		t.Fatalf("archived still listed: %v", l)
	}
	owner.must(400, "POST", bb+"/records", map[string]any{"type": "person", "name": "Late",
		"attributes": map[string]any{"source": "event", "sub_source": "saastr"}})
	owner.must(404, "PATCH", bb+"/sub-sources/nope", map[string]any{"archived": true})
	// Adding it again brings it back.
	if back := owner.must(201, "POST", bb+"/sub-sources", map[string]any{"source": "event", "label": "SAASTR"}); back["key"] != "saastr" || back["archived"] != false {
		t.Fatalf("bring back: %v", back)
	}
}
