package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeAttio is a small Attio workspace: People with one field and two
// records, no lists, one member, for the token "at-key".
func fakeAttio(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-key" {
			w.WriteHeader(401)
			return
		}
		var out any
		switch r.Method + " " + r.URL.Path {
		case "GET /v2/self":
			out = map[string]any{"active": true, "workspace_id": "w1", "workspace_name": "Acme", "workspace_slug": "acme"}
		case "GET /v2/objects":
			out = map[string]any{"data": []any{map[string]any{"id": map[string]string{"object_id": "o1"}, "api_slug": "people", "singular_noun": "Person", "plural_noun": "People"}}}
		case "GET /v2/objects/people/attributes":
			out = map[string]any{"data": []any{map[string]any{"api_slug": "email_addresses", "title": "Email addresses", "type": "email-address", "is_system_attribute": true, "is_unique": true}}}
		case "POST /v2/objects/people/records/query":
			var body struct{ Offset int }
			json.NewDecoder(r.Body).Decode(&body)
			rows := []any{}
			if body.Offset == 0 {
				rows = []any{
					map[string]any{"values": map[string]any{"email_addresses": []any{map[string]string{"email_address": "ann@acme.test"}}}},
					map[string]any{"values": map[string]any{}},
				}
			}
			out = map[string]any{"data": rows}
		case "GET /v2/lists", "GET /v2/notes", "GET /v2/tasks":
			out = map[string]any{"data": []any{}}
		case "GET /v2/workspace_members":
			out = map[string]any{"data": []any{map[string]string{}}}
		default:
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCRMImportPreviewFromAttio(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.integrations.Client.BaseURLs["attio"] = fakeAttio(t).URL

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "attio", "credentials": map[string]string{"api_key": "at-key"}})
	p := owner.must(200, "GET", base+"/integrations/"+i["id"].(string)+"/crm-import-preview", nil)
	objs := p["objects"].([]any)
	people := objs[0].(map[string]any)
	f := people["fields"].([]any)[0].(map[string]any)
	if p["connector"] != "attio" || p["workspace"] != "Acme" || p["members"] != 1.0 || len(objs) != 1 ||
		people["records"].(map[string]any)["count"] != 2.0 || f["filled"] != 1.0 || f["unique"] != true {
		t.Fatalf("preview: %v", p)
	}

	// A connection that is not a CRM says so.
	sl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "smartlead", "credentials": map[string]string{"api_key": "sl-key"}})
	if got := owner.must(400, "GET", base+"/integrations/"+sl["id"].(string)+"/crm-import-preview", nil); got["error"].(map[string]any)["code"] != "not_crm_importer" {
		t.Fatalf("not a CRM: %v", got)
	}
	owner.must(404, "GET", base+"/integrations/00000000-0000-0000-0000-000000000000/crm-import-preview", nil)
}

// fakeAttioCRM is an Attio workspace with Companies, People and Deals for
// the token "at-key", one member (the test owner) and Attio's own shapes.
func fakeAttioCRM(t *testing.T) *httptest.Server {
	t.Helper()
	attr := func(slug, title, typ string, extra map[string]any) map[string]any {
		a := map[string]any{"api_slug": slug, "title": title, "type": typ, "is_system_attribute": false, "is_archived": false}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	ref := func(object string) map[string]any {
		return map[string]any{"config": map[string]any{"record_reference": map[string]any{"allowed_object_ids": []string{object}}}}
	}
	text := func(s string) []any { return []any{map[string]any{"value": s}} }
	link := func(object, id string) map[string]any {
		return map[string]any{"target_object": object, "target_record_id": id}
	}
	records := map[string][]map[string]any{
		"companies": {
			{"id": map[string]string{"record_id": "c1"}, "values": map[string]any{
				"name": text("Acme"), "domains": []any{map[string]any{"domain": "acme.example", "root_domain": "acme.example"}},
				"industry":   []any{map[string]any{"option": map[string]any{"title": "Software and SaaS"}}},
				"categories": []any{map[string]any{"option": map[string]any{"title": "B2B"}}, map[string]any{"option": map[string]any{"title": "Enterprise"}}},
				"team":       []any{link("people", "p1")}}},
			{"id": map[string]string{"record_id": "c2"}, "values": map[string]any{
				"name": text("Northpeak"), "domains": []any{map[string]any{"domain": "northpeak.example"}},
				"industry": []any{map[string]any{"option": map[string]any{"title": "Widgets"}}}}},
		},
		"people": {
			{"id": map[string]string{"record_id": "p1"}, "values": map[string]any{
				"name":             []any{map[string]any{"first_name": "Ann", "last_name": "Lee", "full_name": "Ann Lee"}},
				"email_addresses":  []any{map[string]any{"email_address": "Ann@Acme.example"}},
				"phone_numbers":    []any{map[string]any{"phone_number": "+61412345678"}},
				"job_title":        text("CTO"),
				"company":          []any{link("companies", "c1")},
				"primary_location": []any{map[string]any{"line_1": "1 George St", "locality": "Sydney", "region": "NSW", "postcode": "2000", "country_code": "AU"}},
				"linkedin":         text("linkedin.com/in/ann-example"),
				"last_interaction": []any{map[string]any{"interaction_type": "email"}}}},
			{"id": map[string]string{"record_id": "p2"}, "values": map[string]any{
				"name":            []any{map[string]any{"first_name": "Bo", "last_name": "Diaz", "full_name": "Bo Diaz"}},
				"email_addresses": []any{map[string]any{"email_address": "bo@northpeak.example"}}}},
			{"id": map[string]string{"record_id": "p3"}, "values": map[string]any{
				"name":    []any{map[string]any{"full_name": "Cy"}},
				"company": []any{link("companies", "c9")}}},
		},
		"deals": {
			{"id": map[string]string{"record_id": "d1"}, "values": map[string]any{
				"name":               text("Acme rollout"),
				"stage":              []any{map[string]any{"status": map[string]any{"title": "Won"}}},
				"value":              []any{map[string]any{"currency_value": 1200.5, "currency_code": "USD"}},
				"owner":              []any{map[string]any{"referenced_actor_type": "workspace-member", "referenced_actor_id": "m1"}},
				"associated_people":  []any{link("people", "p1"), link("people", "p3")},
				"associated_company": []any{link("companies", "c1")},
				"close_date":         text("2026-10-01")}},
		},
	}
	objects := []any{
		map[string]any{"id": map[string]string{"object_id": "o-people"}, "api_slug": "people", "singular_noun": "Person", "plural_noun": "People"},
		map[string]any{"id": map[string]string{"object_id": "o-companies"}, "api_slug": "companies", "singular_noun": "Company", "plural_noun": "Companies"},
		map[string]any{"id": map[string]string{"object_id": "o-deals"}, "api_slug": "deals", "singular_noun": "Deal", "plural_noun": "Deals"},
	}
	attributes := map[string][]any{
		"companies": {
			attr("name", "Name", "text", map[string]any{"is_system_attribute": true}),
			attr("domains", "Domains", "domain", map[string]any{"is_system_attribute": true, "is_multiselect": true}),
			attr("industry", "Industry", "select", nil),
			attr("categories", "Categories", "select", map[string]any{"is_multiselect": true}),
			attr("team", "Team", "record-reference", ref("o-people")),
		},
		"people": {
			attr("name", "Name", "personal-name", map[string]any{"is_system_attribute": true}),
			attr("email_addresses", "Email addresses", "email-address", map[string]any{"is_system_attribute": true, "is_multiselect": true}),
			attr("phone_numbers", "Phone numbers", "phone-number", map[string]any{"is_system_attribute": true, "is_multiselect": true}),
			attr("job_title", "Job title", "text", nil),
			attr("company", "Company", "record-reference", ref("o-companies")),
			attr("primary_location", "Primary location", "location", nil),
			attr("linkedin", "LinkedIn", "text", nil),
			attr("last_interaction", "Last interaction", "interaction", nil),
		},
		"deals": {
			attr("name", "Deal name", "text", nil),
			attr("stage", "Deal stage", "status", nil),
			attr("value", "Deal value", "currency", nil),
			attr("owner", "Deal owner", "actor-reference", nil),
			attr("associated_people", "Associated people", "record-reference", merge(ref("o-people"), map[string]any{"is_multiselect": true})),
			attr("associated_company", "Associated company", "record-reference", ref("o-companies")),
			attr("close_date", "Close date", "date", nil),
		},
	}
	options := map[string][]string{
		"/v2/objects/companies/attributes/industry/options":   {"Software and SaaS", "Widgets"},
		"/v2/objects/companies/attributes/categories/options": {"B2B", "Enterprise"},
		"/v2/objects/deals/attributes/stage/statuses":         {"Lead", "Won"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer at-key" {
			w.WriteHeader(401)
			return
		}
		var out any
		path := r.URL.Path
		switch {
		case path == "/v2/self":
			out = map[string]any{"active": true, "workspace_id": "w1", "workspace_name": "Acme", "workspace_slug": "acme"}
		case path == "/v2/objects":
			out = map[string]any{"data": objects}
		case path == "/v2/workspace_members":
			out = map[string]any{"data": []any{map[string]any{"id": map[string]string{"workspace_member_id": "m1"}, "email_address": "JACK@example.com"},
				map[string]any{"id": map[string]string{"workspace_member_id": "m2"}, "email_address": "someone@elsewhere.example"}}}
		case path == "/v2/lists", path == "/v2/notes", path == "/v2/tasks":
			out = map[string]any{"data": []any{}}
		case options[path] != nil:
			list := []any{}
			for _, title := range options[path] {
				list = append(list, map[string]any{"title": title, "is_archived": false})
			}
			out = map[string]any{"data": list}
		case strings.HasSuffix(path, "/attributes"):
			out = map[string]any{"data": attributes[strings.Split(path, "/")[3]]}
		case strings.HasSuffix(path, "/records/query") && r.Method == http.MethodPost:
			var body struct{ Limit, Offset int }
			json.NewDecoder(r.Body).Decode(&body)
			rows := records[strings.Split(path, "/")[3]]
			page := []map[string]any{}
			if body.Offset < len(rows) {
				page = rows[body.Offset:min(len(rows), body.Offset+body.Limit)]
			}
			out = map[string]any{"data": page}
		default:
			t.Errorf("unexpected Attio call %s %s", r.Method, path)
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func merge(a, b map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// runCRMImports works CRM import jobs until the test ends.
func (e *env) runCRMImports(t *testing.T) {
	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { q.Run(ctx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })
}

// waitCRMImport polls an import until it finishes.
func waitCRMImport(t *testing.T, c *client, base, id string) map[string]any {
	t.Helper()
	for range 500 {
		v := c.must(200, "GET", base+"/crm-imports/"+id, nil)
		if v["status"] == "done" || v["status"] == "failed" {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the import did not finish")
	return nil
}

// recordsByName lists a type's records by name.
func recordsByName(c *client, base, recordType string) map[string]map[string]any {
	out := map[string]map[string]any{}
	for _, r := range c.must(200, "GET", base+"/records?type="+recordType+"&limit=200", nil)["data"].([]any) {
		r := r.(map[string]any)
		out[r["name"].(string)] = r
	}
	return out
}

func TestCRMImportFromAttio(t *testing.T) {
	got := track.Capture(t)
	defer api.SetCRMImportSizes(2, 1, 0)()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.integrations.Client.BaseURLs["attio"] = fakeAttioCRM(t).URL
	e.runCRMImports(t)

	// Bo is already here, with his email verified: the import fills him in
	// instead of adding a second Bo.
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo",
		"identities": []any{map[string]any{"namespace": "email", "value": "bo@northpeak.example", "verified": true}}})

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "attio", "credentials": map[string]string{"api_key": "at-key"}})
	path := base + "/integrations/" + i["id"].(string) + "/crm-imports"
	if code, out := owner.do("POST", path, map[string]any{"objects": []string{"tickets"}}); code != 202 {
		t.Fatalf("start: %d %v", code, out)
	} else if v := waitCRMImport(t, owner, base, out["id"].(string)); v["status"] != "failed" || !strings.Contains(*strPtr(v["error"]), "no object tickets") {
		t.Fatalf("unknown object: %v", v)
	}

	run := owner.must(202, "POST", path, map[string]any{})
	if run["status"] != "queued" || run["connector"] != "attio" {
		t.Fatalf("started: %v", run)
	}
	v := waitCRMImport(t, owner, base, run["id"].(string))
	if v["status"] != "done" {
		t.Fatalf("import: %v", v)
	}
	sum := v["summary"].(map[string]any)
	counts := map[string]map[string]any{}
	for _, o := range sum["objects"].([]any) {
		o := o.(map[string]any)
		counts[o["object"].(string)] = o
	}
	if c := counts["companies"]; c["record_type"] != "account" || c["seen"] != 2.0 || c["created"] != 2.0 {
		t.Fatalf("companies: %v", c)
	}
	// p3's company c9 is not in Attio's companies, so it is a placeholder.
	if c := counts["people"]; c["record_type"] != "person" || c["seen"] != 3.0 || c["created"] != 2.0 || c["matched"] != 1.0 || sum["placeholders"] != 1.0 {
		t.Fatalf("people: %v (%v)", c, sum)
	}
	if c := counts["deals"]; c["record_type"] != "deal" || c["created"] != 1.0 {
		t.Fatalf("deals: %v", c)
	}
	cat := sum["catalog"].(map[string]any)
	if fmt.Sprint(cat["types_created"]) != "[deal]" || fmt.Sprint(cat["option_sets_created"]) != "[account_categories deal_stage]" ||
		!strings.Contains(fmt.Sprint(cat["fields_skipped"]), "companies.team") || !strings.Contains(fmt.Sprint(cat["fields_skipped"]), "people.last_interaction") ||
		!strings.Contains(fmt.Sprint(cat["fields_reused"]), "account.industry") {
		t.Fatalf("catalog: %v", cat)
	}
	if m := sum["members"].(map[string]any); m["total"] != 2.0 || m["matched"] != 1.0 {
		t.Fatalf("members: %v", m)
	}
	if !strings.Contains(fmt.Sprint(sum["warnings"]), `"Widgets" is not one of its options`) {
		t.Fatalf("warnings: %v", sum["warnings"])
	}

	companies := recordsByName(owner, base, "account")
	acme := companies["Acme"]
	if acme["origin"] != "import" || acme["attributes"].(map[string]any)["industry"] != "software" ||
		fmt.Sprint(acme["attributes"].(map[string]any)["categories"]) != "[b2b enterprise]" ||
		acme["attributes"].(map[string]any)["website"] != "https://acme.example" || acme["attribute_sources"].(map[string]any)["industry"] != "crm_sync" {
		t.Fatalf("acme: %v", acme)
	}
	people := recordsByName(owner, base, "person")
	ann := people["Ann Lee"]
	attrs := ann["attributes"].(map[string]any)
	if attrs["first_name"] != "Ann" || attrs["job_title"] != "CTO" || attrs["city"] != "Sydney" || attrs["country"] != "AU" ||
		attrs["linkedin_url"] != "https://linkedin.com/in/ann-example" || fmt.Sprint(attrs["companies"]) != "["+acme["id"].(string)+"]" ||
		attrs["primary_location"].(map[string]any)["postal_code"] != "2000" {
		t.Fatalf("ann: %v", ann)
	}
	ids := map[string]bool{}
	for _, id := range ann["identities"].([]any) {
		id := id.(map[string]any)
		ids[fmt.Sprint(id["namespace"], "=", id["value"], " ", id["verified"])] = true
	}
	if !ids["email=ann@acme.example false"] || !ids["phone=+61412345678 false"] || !ids["attio.record=p1 true"] {
		t.Fatalf("ann's identities: %v", ids)
	}
	if b := people["Bo Diaz"]; b == nil || b["id"] != bo["id"] || b["attributes"].(map[string]any)["last_name"] != "Diaz" {
		t.Fatalf("bo was not matched: %v", people)
	}
	if len(people) != 3 || people["Cy"] == nil {
		t.Fatalf("people: %v", people)
	}
	deal := recordsByName(owner, base, "deal")["Acme rollout"]
	da := deal["attributes"].(map[string]any)
	me := owner.must(200, "GET", "/v1/me", nil)
	if da["stage"] != "won" || fmt.Sprint(da["value"]) != "map[amount_minor:120050 currency:USD]" || da["owner"] != me["user"].(map[string]any)["id"] ||
		da["close_date"] != "2026-10-01" || da["associated_company"] != acme["id"] ||
		fmt.Sprint(da["associated_people"]) != fmt.Sprint([]any{ann["id"], people["Cy"]["id"]}) {
		t.Fatalf("deal: %v (me %v)", deal, me["user"])
	}

	// Someone corrects Ann's title in Outflare; running the import again
	// keeps it, adds nothing and changes nothing else.
	owner.must(200, "PATCH", base+"/records/"+ann["id"].(string), map[string]any{"attributes": map[string]any{"job_title": "Chief Technology Officer"}})
	again := waitCRMImport(t, owner, base, owner.must(202, "POST", path, map[string]any{"objects": []string{"people", "companies"}})["id"].(string))
	if again["status"] != "done" {
		t.Fatalf("again: %v", again)
	}
	for _, o := range again["summary"].(map[string]any)["objects"].([]any) {
		o := o.(map[string]any)
		if o["created"] != 0.0 || o["updated"] != 0.0 || o["matched"] != 0.0 {
			t.Fatalf("second run changed %v", o)
		}
	}
	if n := len(recordsByName(owner, base, "person")); n != 3 {
		t.Fatalf("people after a second run: %d", n)
	}
	if a := owner.must(200, "GET", base+"/records/"+ann["id"].(string), nil); a["attributes"].(map[string]any)["job_title"] != "Chief Technology Officer" {
		t.Fatalf("manual title lost: %v", a)
	}
	list := owner.must(200, "GET", base+"/crm-imports", nil)["data"].([]any)
	if len(list) != 3 {
		t.Fatalf("imports: %v", list)
	}
	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"crm_import.started", "crm_import.finished", "crm_import.failed"} {
		if !seen[name] {
			t.Fatalf("no %s event", name)
		}
	}

	// Importing needs customers.manage too; reading imports needs customers.read.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "catalog only", "scopes": []string{"catalog.manage", "integrations.read"}})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	bot.must(403, "POST", path, map[string]any{})
	bot.must(403, "GET", base+"/crm-imports", nil)
	owner.must(404, "GET", base+"/crm-imports/00000000-0000-0000-0000-000000000000", nil)
}

func strPtr(v any) *string {
	s, _ := v.(string)
	return &s
}
