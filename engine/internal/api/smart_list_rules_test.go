package api_test

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/smartlists"
	"github.com/outflareai/outflare/engine/internal/track"
)

// smartListDraftBodies keeps what the describe box sent the model.
var smartListDraftBodies struct {
	sync.Mutex
	all []string
}

// answerSmartListDraft answers fakeClaude's describe-box calls: rules
// naming an event that doesn't exist first, then good ones once told why;
// nothing for the weather.
func answerSmartListDraft(w http.ResponseWriter, body string) {
	smartListDraftBodies.Lock()
	smartListDraftBodies.all = append(smartListDraftBodies.all, body)
	smartListDraftBodies.Unlock()
	switch {
	case strings.Contains(body, "weather"):
		answerJSON(w, map[string]any{"possible": false, "name": "", "match": "all", "rules_json": "[]", "explanation": "There is no weather data here."})
	case strings.Contains(body, "cannot be used"):
		answerJSON(w, map[string]any{"possible": true, "name": "Paid, never opened the app", "match": "all",
			"rules_json":  `[{"event": "payment_succeeded", "op": "exists", "within_days": 90}, {"match": "none", "rules": [{"page": {"host": "app.example.com"}, "op": "exists"}]}]`,
			"explanation": "People who paid in the last 90 days and never viewed the app."})
	default:
		answerJSON(w, map[string]any{"possible": true, "name": "x", "match": "all", "rules_json": `[{"event": "Paid It", "op": "exists"}]`, "explanation": "x"})
	}
}

// TestSmartListRuleKinds covers the tests beyond fields and events: page
// visits, event counts and properties, other lists, marketing email, the
// newer comparisons, the picker's choices and rules described in words.
func TestSmartListRuleKinds(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	e.runServerJobs(t)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand

	person := func(addr, attrs string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = $2, brand_id = $3 WHERE id = $1`, rec, attrs, brand); err != nil {
			t.Fatal(err)
		}
		return rec
	}
	event := func(rec, name, ago, props string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, record_id, brand_id, kind, name, occurred_at, source, properties)
			VALUES ($1, $2, $3, 'track', $4, now() - $5::interval, 'api', $6)`, ws, rec, brand, name, ago, props); err != nil {
			t.Fatal(err)
		}
	}
	// view is a page view from browser anon, which identifiedBy tied to
	// rec ("" leaves it anonymous, as a forged view would be).
	view := func(rec, anon, identifiedBy, host, path, ago string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, record_id, brand_id, anonymous_id, kind, name, occurred_at, source, context)
			VALUES ($1, $2, $3, $4, 'track', 'page.viewed', now() - $7::interval, 'browser',
				jsonb_build_object('page', jsonb_build_object('host', $5::text, 'path', $6::text, 'url', 'https://' || $5 || $6)))`,
			ws, rec, brand, anon, host, path, ago); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_visitors (workspace_id, anonymous_id, record_id, brand_id, identified_by, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $5, now(), now()) ON CONFLICT DO NOTHING`, ws, anon, rec, brand, identifiedBy); err != nil {
			t.Fatal(err)
		}
	}
	count := func(rules []map[string]any) []string {
		t.Helper()
		p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{"rules": rules})
		var out []string
		for _, s := range p["sample"].([]any) {
			out = append(out, s.(map[string]any)["email"].(string))
		}
		if int(p["members"].(float64)) != len(out) {
			t.Fatalf("%v: members %v, sample %v", rules, p["members"], out)
		}
		return out
	}
	want := func(rules []map[string]any, emails ...string) {
		t.Helper()
		got := count(rules)
		if strings.Join(sorted(got), ",") != strings.Join(sorted(emails), ",") {
			t.Fatalf("%v: got %v, want %v", rules, got, emails)
		}
	}

	ana := person("ana@acme.example", `{"plan": "pro", "renews_at": "2099-01-01", "signed_up_at": "`+"2026-01-01"+`"}`)
	bo := person("bo@acme.example", `{"plan": "team", "signed_up_at": "not a date"}`)
	cy := person("cy@acme.example", `{"plan": "free"}`)
	event(ana, "payment_succeeded", "5 days", `{"amount_minor": 25000}`)
	event(ana, "payment_succeeded", "40 days", `{"amount_minor": 5000}`)
	event(ana, "payment_succeeded", "400 days", `{"amount_minor": 5000}`)
	event(bo, "payment_succeeded", "2 days", `{"amount_minor": 900}`)
	view(ana, "b-ana", "product", "app.example.com", "/dashboard", "1 day")
	view(ana, "b-ana", "product", "www.example.com", "/pricing/", "2 days")
	view(bo, "b-bo", "email_click", "example.com", "/pricing", "50 days")
	// A view from a browser nobody was tied to never counts, even when it
	// carries a record: anyone can send one.
	view(cy, "b-cy", "", "app.example.com", "/dashboard", "1 day")

	// Pages: by site (www. is the same site), by path, and how long ago.
	want([]map[string]any{{"page": map[string]any{"host": "app.example.com"}, "op": "exists"}}, "ana@acme.example")
	want([]map[string]any{{"page": map[string]any{"host": "example.com", "path": "/pricing", "match": "is"}, "op": "exists"}}, "ana@acme.example", "bo@acme.example")
	want([]map[string]any{{"page": map[string]any{"path": "/pri", "match": "starts_with"}, "op": "exists", "within_days": 30}}, "ana@acme.example")
	want([]map[string]any{{"page": map[string]any{"path": "pricing"}, "op": "exists", "before_days": 30}}, "bo@acme.example")
	want([]map[string]any{{"page": map[string]any{}, "op": "not_exists"}}, "cy@acme.example")
	// The question that started it: paid, but never opened the app.
	want([]map[string]any{{"event": "payment_succeeded", "op": "exists"},
		{"match": "none", "rules": []map[string]any{{"page": map[string]any{"host": "app.example.com"}, "op": "exists"}}}}, "bo@acme.example")

	// Event counts, properties and time.
	want([]map[string]any{{"event": "payment_succeeded", "op": "exists", "min_count": 2}}, "ana@acme.example")
	want([]map[string]any{{"event": "payment_succeeded", "op": "not_exists", "min_count": 2}}, "bo@acme.example", "cy@acme.example")
	want([]map[string]any{{"event": "payment_succeeded", "op": "exists", "where": []map[string]any{{"property": "amount_minor", "op": "gt", "value": 10000}}}}, "ana@acme.example")
	want([]map[string]any{{"event": "payment_succeeded", "op": "exists", "before_days": 365}}, "ana@acme.example")
	want([]map[string]any{{"event": "payment_succeeded", "op": "exists", "min_count": 2, "within_days": 60,
		"where": []map[string]any{{"property": "amount_minor", "op": "in", "value": []any{5000, 25000}}}}}, "ana@acme.example")

	// The newer comparisons.
	want([]map[string]any{{"attribute": "plan", "op": "in", "value": []any{"PRO", "team"}}}, "ana@acme.example", "bo@acme.example")
	want([]map[string]any{{"attribute": "plan", "op": "not_in", "value": []any{"pro"}}}, "bo@acme.example", "cy@acme.example")
	want([]map[string]any{{"attribute": "plan", "op": "starts_with", "value": "T"}}, "bo@acme.example")
	// Dates: a value that isn't one is skipped, never an error.
	want([]map[string]any{{"attribute": "signed_up_at", "op": "more_than_days_ago", "value": 30}}, "ana@acme.example")
	want([]map[string]any{{"attribute": "renews_at", "op": "in_last_days", "value": 30}})

	// Marketing email: as the People list says.
	marketable := map[string]bool{}
	for _, m := range count([]map[string]any{{"marketing_email": true, "op": "exists"}}) {
		marketable[m] = true
	}
	notMarketable := count([]map[string]any{{"marketing_email": true, "op": "not_exists"}})
	if len(marketable)+len(notMarketable) != 3 {
		t.Fatalf("marketing email splits 3 people into %v and %v", marketable, notMarketable)
	}

	// Bad rules say why.
	for _, rules := range [][]map[string]any{
		{{"page": map[string]any{"host": "Not A Host"}, "op": "exists"}},
		{{"page": map[string]any{"match": "regex"}, "op": "exists"}},
		{{"page": map[string]any{}, "op": "eq", "value": 1}},
		{{"attribute": "plan", "op": "exists", "min_count": 2}},
		{{"attribute": "plan", "op": "in", "value": "pro"}},
		{{"attribute": "plan", "op": "in_last_days", "value": "soon"}},
		{{"event": "x", "op": "exists", "within_days": 3, "before_days": 4}},
		{{"page": map[string]any{}, "op": "exists", "where": []map[string]any{{"property": "a", "op": "exists"}}}},
		{{"marketing_email": true, "op": "eq", "value": true}},
		{{"in_smart_list": "00000000-0000-0000-0000-000000000000", "op": "exists"}},
	} {
		owner.must(400, "POST", base+"/smart-lists/preview", map[string]any{"rules": rules})
	}

	// Another list: its members at its last refresh. A list can't depend
	// on itself, and a list another one uses stays until that changes.
	refresh := func(id string) {
		t.Helper()
		if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := smartlists.Refresh(ctx, tx, ws, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	payers := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Payers", "rules": []map[string]any{{"event": "payment_succeeded", "op": "exists"}}})["id"].(string)
	refresh(payers)
	want([]map[string]any{{"in_smart_list": payers, "op": "not_exists"}}, "cy@acme.example")
	others := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Not payers", "rules": []map[string]any{{"in_smart_list": payers, "op": "not_exists"}}})["id"].(string)
	refresh(others)
	owner.must(400, "PATCH", base+"/smart-lists/"+payers, map[string]any{"rules": []map[string]any{{"in_smart_list": others, "op": "exists"}}})
	owner.must(400, "PATCH", base+"/smart-lists/"+payers, map[string]any{"rules": []map[string]any{{"in_smart_list": payers, "op": "exists"}}})
	if msg := owner.must(409, "DELETE", base+"/smart-lists/"+payers, nil)["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "Not payers") {
		t.Fatalf("delete refused with %q", msg)
	}
	owner.must(204, "DELETE", base+"/smart-lists/"+others, nil)
	owner.must(204, "DELETE", base+"/smart-lists/"+payers, nil)

	// The picker's choices: events (not the browser's), sites and pages.
	ch := owner.must(200, "GET", base+"/smart-lists/choices", nil)
	if ev := ch["events"].([]any); len(ev) != 1 || ev[0].(map[string]any)["name"] != "payment_succeeded" || ev[0].(map[string]any)["count"] != 3.0 {
		t.Fatalf("choice events = %v", ev)
	}
	// A property the catalog doesn't describe is offered from what the
	// events carry, with the kind of value seen.
	if props := ch["events"].([]any)[0].(map[string]any)["properties"].([]any); len(props) != 1 ||
		props[0].(map[string]any)["key"] != "amount_minor" || props[0].(map[string]any)["value_type"] != "number" {
		t.Fatalf("choice properties = %v", props)
	}
	hosts := map[string]float64{}
	for _, h := range ch["hosts"].([]any) {
		hosts[h.(map[string]any)["host"].(string)] = h.(map[string]any)["views"].(float64)
	}
	if hosts["app.example.com"] != 2 || hosts["www.example.com"] != 1 || len(ch["pages"].([]any)) != 2 {
		t.Fatalf("choice hosts = %v, pages = %v", hosts, ch["pages"])
	}

	// Described in words: on the workspace's own AI model only.
	if c := owner.must(409, "POST", base+"/smart-list-drafts", map[string]any{"description": "Paid but never opened the app"})["error"].(map[string]any)["code"]; c != "connector_needed" {
		t.Fatalf("no AI: %v", c)
	}
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "claude", "credentials": map[string]any{"api_key": "sk-ant-good"}})
	owner.must(400, "POST", base+"/smart-list-drafts", map[string]any{"description": " "})
	owner.must(400, "POST", base+"/smart-list-drafts", map[string]any{"description": "x", "record_type": "deal"})
	owner.must(404, "GET", base+"/smart-list-drafts/00000000-0000-0000-0000-000000000000", nil)
	draft := func(d string) map[string]any {
		t.Helper()
		r := owner.must(202, "POST", base+"/smart-list-drafts", map[string]any{"description": d})
		if r["status"] != "queued" || r["description"] != d || r["rules"] != nil {
			t.Fatalf("drafted: %v", r)
		}
		return waitFor(t, owner, base+"/smart-list-drafts/"+r["id"].(string), func(m map[string]any) bool { return m["status"] == "ready" || m["status"] == "failed" })
	}
	d := draft("People who paid in the last 90 days but never opened the app")
	if d["status"] != "ready" || d["name"] != "Paid, never opened the app" || d["match"] != "all" || len(d["rules"].([]any)) != 2 {
		t.Fatalf("draft: %v", d)
	}
	// The rules preview and save as they are.
	rules := d["rules"]
	if p := owner.must(200, "POST", base+"/smart-lists/preview", map[string]any{"rules": rules}); p["members"] != 1.0 {
		t.Fatalf("draft preview = %v", p)
	}
	owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": d["name"], "rules": rules})
	smartListDraftBodies.Lock()
	bodies := strings.Join(smartListDraftBodies.all, "\n")
	n := len(smartListDraftBodies.all)
	smartListDraftBodies.Unlock()
	if n != 2 || !strings.Contains(bodies, "payment_succeeded") || !strings.Contains(bodies, "app.example.com") || !strings.Contains(bodies, "Paid It") {
		t.Fatalf("the model was asked %d times, or without the brand's names or the engine's reason", n)
	}
	// Names only: never a person.
	if strings.Contains(bodies, "acme.example") {
		t.Fatal("a person's email reached the model")
	}
	none := draft("What will the weather be?")
	if none["status"] != "ready" || none["rules"] != nil || !strings.Contains(none["explanation"].(string), "weather") {
		t.Fatalf("no rules: %v", none)
	}

	seen := map[string]int{}
	for _, ev := range got() {
		seen[ev.Name]++
		if strings.HasPrefix(ev.Name, "smart_list_draft.") {
			for k, v := range ev.Props {
				if s, ok := v.(string); ok && strings.Contains(s, "paid") {
					t.Fatalf("%s carries the description in %s", ev.Name, k)
				}
			}
		}
	}
	if seen["smart_list_draft.requested"] != 2 || seen["smart_list_draft.written"] != 2 {
		t.Fatalf("events: %v", seen)
	}
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}
