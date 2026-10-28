package attio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fakeAttio has People (3 records), Deals (1 record, a status field and a
// reference to People) and a Sales pipeline list with 2 entries.
func fakeAttio(t *testing.T) *httptest.Server {
	t.Helper()
	attr := func(slug, typ string, extra map[string]any) map[string]any {
		a := map[string]any{"api_slug": slug, "title": slug, "type": typ, "is_system_attribute": false, "is_archived": false}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-key" {
			w.WriteHeader(401)
			return
		}
		var body struct{ Limit, Offset int }
		if r.Method == http.MethodPost {
			json.NewDecoder(r.Body).Decode(&body)
		}
		page := func(rows []map[string]any) []map[string]any {
			if body.Offset >= len(rows) {
				return nil
			}
			return rows[body.Offset:min(len(rows), body.Offset+body.Limit)]
		}
		var out any
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/self":
			out = map[string]any{"active": true, "workspace_id": "ws1", "workspace_name": "Acme", "workspace_slug": "acme"}
		case "GET /v2/objects":
			out = map[string]any{"data": []any{
				map[string]any{"id": map[string]string{"object_id": "o-people"}, "api_slug": "people", "singular_noun": "Person", "plural_noun": "People"},
				map[string]any{"id": map[string]string{"object_id": "o-deals"}, "api_slug": "deals", "singular_noun": "Deal", "plural_noun": "Deals"},
			}}
		case "GET /v2/objects/people/attributes":
			out = map[string]any{"data": []any{
				attr("email_addresses", "email-address", map[string]any{"is_system_attribute": true, "is_unique": true, "is_multiselect": true}),
				attr("job_title", "text", nil),
				attr("old_field", "text", map[string]any{"is_archived": true}),
			}}
		case "GET /v2/objects/deals/attributes":
			out = map[string]any{"data": []any{
				attr("stage", "status", map[string]any{"is_required": true}),
				attr("associated_people", "record-reference", map[string]any{"is_multiselect": true,
					"config": map[string]any{"record_reference": map[string]any{"allowed_object_ids": []string{"o-people"}}}}),
			}}
		case "GET /v2/objects/deals/attributes/stage/statuses":
			out = map[string]any{"data": []any{map[string]any{"title": "Lead"}, map[string]any{"title": "Won"}, map[string]any{"title": "Old", "is_archived": true}}}
		case "POST /v2/objects/people/records/query":
			out = map[string]any{"data": page([]map[string]any{
				{"values": map[string]any{"email_addresses": []any{map[string]string{"email_address": "ann@acme.test"}}, "job_title": []any{map[string]string{"value": "CTO"}}}},
				{"values": map[string]any{"email_addresses": []any{map[string]string{"email_address": "bob@acme.test"}}, "job_title": []any{}}},
				{"values": map[string]any{"email_addresses": []any{}}},
			})}
		case "POST /v2/objects/deals/records/query":
			out = map[string]any{"data": page([]map[string]any{{"values": map[string]any{"stage": []any{map[string]string{"status": "Lead"}}}}})}
		case "GET /v2/lists":
			out = map[string]any{"data": []any{map[string]any{"api_slug": "sales", "name": "Sales", "parent_object": []string{"deals"}}}}
		case "GET /v2/lists/sales/attributes":
			out = map[string]any{"data": []any{attr("owner", "actor-reference", nil)}}
		case "POST /v2/lists/sales/entries/query":
			out = map[string]any{"data": page([]map[string]any{
				{"entry_values": map[string]any{"owner": []any{map[string]string{"referenced_actor_id": "m1"}}}},
				{"entry_values": map[string]any{}},
			})}
		case "GET /v2/workspace_members":
			out = map[string]any{"data": []any{map[string]string{}, map[string]string{}}}
		case "GET /v2/notes":
			offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			n := 0
			if offset == 0 {
				n = min(3, limit)
			}
			out = map[string]any{"data": make([]map[string]string, n)}
		case "GET /v2/tasks":
			out = map[string]any{"data": []any{}}
		default:
			t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
}

func TestPreviewReadsObjectsListsAndCounts(t *testing.T) {
	srv := fakeAttio(t)
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"attio": srv.URL}}
	p, err := Attio{}.CRMPreview(context.Background(), cl, c.Creds{"api_key": "at-key"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if p.Workspace != "Acme" || p.Members != 2 || p.Notes.Count != 3 || p.Notes.Capped || p.Tasks.Count != 0 {
		t.Fatalf("preview: %+v", p)
	}
	people, deals := p.Objects[0], p.Objects[1]
	if people.Records.Count != 3 || people.Records.Capped || len(people.Fields) != 2 {
		t.Fatalf("people: %+v", people)
	}
	if f := people.Fields[0]; f.Slug != "email_addresses" || f.Kind != c.CRMEmail || !f.System || !f.Unique || !f.Multiple || f.Filled != 2 {
		t.Fatalf("email field: %+v", f)
	}
	if people.Fields[1].Filled != 1 {
		t.Fatalf("job title filled: %+v", people.Fields[1])
	}
	stage, ref := deals.Fields[0], deals.Fields[1]
	if !stage.Required || len(stage.Options) != 2 || stage.Options[1] != "Won" || stage.Filled != 1 {
		t.Fatalf("stage: %+v", stage)
	}
	if len(ref.Target) != 1 || ref.Target[0] != "people" || ref.Kind != c.CRMReference || stage.Kind != c.CRMStatus {
		t.Fatalf("reference: %+v", ref)
	}
	l := p.Lists[0]
	if l.Slug != "sales" || l.Object != "deals" || l.Entries.Count != 2 || l.Fields[0].Filled != 1 {
		t.Fatalf("list: %+v", l)
	}
}

func TestPreviewStopsAtTheLimit(t *testing.T) {
	srv := fakeAttio(t)
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"attio": srv.URL}}
	p, err := Attio{}.CRMPreview(context.Background(), cl, c.Creds{"api_key": "at-key"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if n := p.Objects[0].Records; n.Count != 2 || !n.Capped {
		t.Fatalf("people count: %+v", n)
	}
	if n := p.Objects[1].Records; n.Count != 1 || n.Capped {
		t.Fatalf("deals count: %+v", n)
	}
	if n := p.Notes; !n.Capped {
		t.Fatalf("notes count: %+v", n)
	}
}

func TestWrongTokenIsAnAuthError(t *testing.T) {
	srv := fakeAttio(t)
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"attio": srv.URL}}
	_, err := Attio{}.Test(context.Background(), cl, c.Creds{"api_key": "nope"})
	if c.KindOf(err) != c.ErrAuth {
		t.Fatalf("err: %v", err)
	}
}

func TestRecordsAndMembersConvertAttioValues(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.URL.Path {
		case "/v2/workspace_members":
			out = map[string]any{"data": []any{map[string]any{"id": map[string]string{"workspace_member_id": "m1"}, "email_address": "ann@acme.test"}}}
		case "/v2/objects/deals/records/query":
			var body struct {
				Limit, Offset int
				Sorts         []map[string]string
			}
			json.NewDecoder(r.Body).Decode(&body)
			if body.Limit != 50 || body.Offset != 100 || body.Sorts[0]["attribute"] != "created_at" {
				t.Errorf("query: %+v", body)
			}
			out = map[string]any{"data": []any{map[string]any{"id": map[string]string{"record_id": "d1"}, "values": map[string]any{
				"name":   []any{map[string]any{"value": "Big deal"}},
				"stage":  []any{map[string]any{"status": map[string]any{"title": "Won"}}},
				"tags":   []any{map[string]any{"option": map[string]any{"title": "A"}}, map[string]any{"option": map[string]any{"title": "B"}}},
				"value":  []any{map[string]any{"currency_value": 12.5, "currency_code": "EUR"}},
				"owner":  []any{map[string]any{"referenced_actor_type": "workspace-member", "referenced_actor_id": "m1"}},
				"bot":    []any{map[string]any{"referenced_actor_type": "api-token", "referenced_actor_id": "t1"}},
				"people": []any{map[string]any{"target_object": "people", "target_record_id": "p1"}},
				"where":  []any{map[string]any{"locality": "Sydney", "country_code": "AU"}},
				"who":    []any{map[string]any{"first_name": "Ann", "last_name": "Lee", "full_name": "Ann Lee"}},
				"won":    []any{map[string]any{"value": true}},
				"score":  []any{map[string]any{"value": 4}},
				"empty":  []any{},
			}}}}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"attio": srv.URL}}
	creds := c.Creds{"api_key": "at-key"}
	members, err := Attio{}.CRMMembers(context.Background(), cl, creds)
	if err != nil || len(members) != 1 || members[0] != (c.CRMMember{ID: "m1", Email: "ann@acme.test"}) {
		t.Fatalf("members: %v %v", members, err)
	}
	recs, err := Attio{}.CRMRecords(context.Background(), cl, creds, "deals", 100, 50)
	if err != nil || len(recs) != 1 {
		t.Fatalf("records: %v %v", recs, err)
	}
	v := recs[0].Values
	want := map[string][]any{
		"name": {"Big deal"}, "stage": {"Won"}, "tags": {"A", "B"}, "value": {c.CRMMoney{Amount: 12.5, Currency: "EUR"}}, "owner": {"m1"},
		"people": {c.CRMRef{Object: "people", ID: "p1"}}, "where": {c.CRMLocation{City: "Sydney", Country: "AU"}},
		"who": {c.CRMName{First: "Ann", Last: "Lee", Full: "Ann Lee"}}, "won": {true}, "score": {4.0},
	}
	if recs[0].ID != "d1" || len(v) != len(want) {
		t.Fatalf("values: %#v", v)
	}
	for k, w := range want {
		if fmt.Sprint(v[k]) != fmt.Sprint(w) {
			t.Fatalf("%s: %#v, want %#v", k, v[k], w)
		}
	}
}

// A workspace too big to count while someone waits: every page is full, so
// only the time limit stops the preview, and each count says it is capped.
func TestPreviewStopsInTime(t *testing.T) {
	defer func(d time.Duration) { previewTime = d }(previewTime)
	previewTime = 200 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Limit int }
		if r.Method == http.MethodPost {
			json.NewDecoder(r.Body).Decode(&body)
		} else {
			body.Limit, _ = strconv.Atoi(r.URL.Query().Get("limit"))
		}
		var out any
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/self":
			out = map[string]any{"active": true, "workspace_name": "Acme"}
		case "GET /v2/objects":
			out = map[string]any{"data": []any{map[string]any{"id": map[string]string{"object_id": "o-people"}, "api_slug": "people"}}}
		case "GET /v2/lists", "GET /v2/workspace_members", "GET /v2/objects/people/attributes":
			out = map[string]any{"data": []any{}}
		default:
			out = map[string]any{"data": make([]map[string]any, body.Limit)}
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"attio": srv.URL}}
	start := time.Now()
	p, err := Attio{}.CRMPreview(context.Background(), cl, c.Creds{"api_key": "at-key"}, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("preview took %s", d)
	}
	if n := p.Objects[0].Records; n.Count == 0 || !n.Capped {
		t.Fatalf("people count: %+v", n)
	}
	if !p.Notes.Capped || !p.Tasks.Capped {
		t.Fatalf("notes %+v, tasks %+v", p.Notes, p.Tasks)
	}
}

// A workspace whose record queries hang: counting gives up on them when its
// time is up, instead of holding the preview for the request timeout.
func TestPreviewGivesUpOnSlowCounts(t *testing.T) {
	defer func(d time.Duration) { previewTime = d }(previewTime)
	previewTime = 200 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var out any
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/self":
			out = map[string]any{"active": true, "workspace_name": "Acme"}
		case "GET /v2/objects":
			out = map[string]any{"data": []any{map[string]any{"id": map[string]string{"object_id": "o-people"}, "api_slug": "people"}}}
		case "GET /v2/lists", "GET /v2/workspace_members", "GET /v2/objects/people/attributes":
			out = map[string]any{"data": []any{}}
		default:
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	defer srv.Close()
	cl := &c.Client{BaseURLs: map[string]string{"attio": srv.URL}}
	start := time.Now()
	p, err := Attio{}.CRMPreview(context.Background(), cl, c.Creds{"api_key": "at-key"}, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("preview took %s", d)
	}
	if n := p.Objects[0].Records; n.Count != 0 || !n.Capped {
		t.Fatalf("people count: %+v", n)
	}
	if !p.Notes.Capped || !p.Tasks.Capped {
		t.Fatalf("notes %+v, tasks %+v", p.Notes, p.Tasks)
	}
}
