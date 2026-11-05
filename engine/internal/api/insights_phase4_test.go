package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestInsightsPhase4 covers exclusion steps, company funnels, saving a
// step's people as a list, correlation, paths and annotations.
func TestInsightsPhase4(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})["id"].(string)
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo"})["id"].(string)
	cy := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy"})["id"].(string)
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	northpeak := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour
	t0 := time.Now().UTC().Truncate(day).Add(-10*day + time.Hour)
	event := func(name string, record any, anon any, at time.Time) {
		exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, anonymous_id, kind, name, occurred_at)
			VALUES ($1, $2, $3, $4, 'track', $5, $6)`, ws, brand, record, anon, name, at)
	}
	page := func(record any, anon any, path string, at time.Time) {
		exec(`INSERT INTO customer_events (workspace_id, brand_id, record_id, anonymous_id, kind, name, occurred_at, context)
			VALUES ($1, $2, $3, $4, 'page', 'page.viewed', $5, jsonb_build_object('page', jsonb_build_object('path', $6::text)))`,
			ws, brand, record, anon, at, path)
	}
	for _, link := range [][2]string{{ana, acme}, {bo, acme}, {cy, northpeak}} {
		exec(`INSERT INTO record_relationships (workspace_id, from_id, to_id, kind) VALUES ($1, $2, $3, 'companies')`, ws, link[0], link[1])
	}
	// Everyone signs up. Ana and Bo pay; Bo contacted support in between.
	// Cy viewed the docs and never paid; a browser nobody knows signed up
	// and dropped off too.
	event("signed_up", ana, nil, t0)
	event("signed_up", bo, nil, t0.Add(time.Hour))
	event("signed_up", cy, nil, t0.Add(2*time.Hour))
	event("signed_up", nil, "b-anon", t0.Add(3*time.Hour))
	page(ana, nil, "/pricing", t0.Add(10*time.Minute))
	page(bo, nil, "/pricing", t0.Add(70*time.Minute))
	page(cy, nil, "/docs", t0.Add(130*time.Minute))
	page(nil, "b-anon", "/docs", t0.Add(190*time.Minute))
	event("support_contacted", bo, nil, t0.Add(90*time.Minute))
	event("paid", ana, nil, t0.Add(day))
	event("paid", bo, nil, t0.Add(day+time.Hour))

	from := t0.Add(-day).Format(time.DateOnly)
	to := time.Now().UTC().Format(time.DateOnly)
	funnel := map[string]any{
		"date_range": map[string]any{"from": from, "to": to},
		"steps":      []map[string]any{{"kind": "event", "event": "signed_up"}, {"kind": "event", "event": "paid"}},
	}
	count := func(res map[string]any, i int) float64 {
		return res["steps"].([]any)[i].(map[string]any)["count"].(float64)
	}
	res := owner.must(200, "POST", base+"/insights/funnel", funnel)
	if count(res, 0) != 4 || count(res, 1) != 2 {
		t.Fatalf("plain: %v", res["steps"])
	}

	// Excluding people who contacted support between the steps drops Bo
	// from every step.
	funnel["exclusions"] = []map[string]any{{"kind": "event", "event": "support_contacted", "from": 1, "to": 2}}
	res = owner.must(200, "POST", base+"/insights/funnel", funnel)
	if count(res, 0) != 3 || count(res, 1) != 1 {
		t.Fatalf("excluded: %v", res["steps"])
	}
	delete(funnel, "exclusions")

	// By company: Acme (Ana, Bo) paid, Northpeak (Cy) didn't, the browser
	// belongs to nobody.
	funnel["by"] = "company"
	res = owner.must(200, "POST", base+"/insights/funnel", funnel)
	if count(res, 0) != 2 || count(res, 1) != 1 {
		t.Fatalf("by company: %v", res["steps"])
	}
	people := owner.must(200, "POST", base+"/insights/funnel/people", map[string]any{"funnel": funnel, "step": 2, "dropped": true})
	if list := people["data"].([]any); len(list) != 1 || list[0].(map[string]any)["name"] != "Northpeak" {
		t.Fatalf("companies dropped: %v", people)
	}
	owner.must(400, "POST", base+"/insights/funnel/people/save", map[string]any{"funnel": funnel, "step": 2, "dropped": true, "name": "Companies"})
	delete(funnel, "by")

	// Saving the drop-off as a list: Cy is in it, the browser is skipped.
	saved := owner.must(201, "POST", base+"/insights/funnel/people/save", map[string]any{"funnel": funnel, "step": 2, "dropped": true, "name": "Signed up, never paid"})
	if saved["added"] != 1.0 || saved["skipped_anonymous"] != 1.0 {
		t.Fatalf("saved: %v", saved)
	}
	listID := saved["list_id"].(string)
	members := owner.must(200, "GET", base+"/smart-lists/"+listID+"/members", nil)
	if m := members["data"].([]any); len(m) != 1 || m[0].(map[string]any)["record_id"] != cy && m[0].(map[string]any)["id"] != cy {
		t.Fatalf("members: %v", members)
	}
	owner.must(409, "POST", base+"/insights/funnel/people/save", map[string]any{"funnel": funnel, "step": 2, "dropped": true, "name": "Signed up, never paid"})
	owner.must(400, "POST", base+"/insights/funnel/people/save", map[string]any{"funnel": funnel, "step": 2, "name": ""})

	// Correlation: viewing /pricing went with paying, /docs with dropping
	// off (3 people is the floor, so each side needs the counts it has).
	corr := owner.must(200, "POST", base+"/insights/funnel/correlation", map[string]any{"funnel": funnel})
	if corr["converted"] != 2.0 || corr["dropped"] != 2.0 || corr["step"] != 2.0 {
		t.Fatalf("correlation totals: %v", corr)
	}
	// Two viewed /pricing, two /docs and one contacted support: all under
	// the floor of three, so nothing is reported on so few people.
	if len(corr["positive"].([]any)) != 0 || len(corr["negative"].([]any)) != 0 || corr["skipped"] != 3.0 {
		t.Fatalf("correlation: %v", corr)
	}
	page(cy, nil, "/pricing", t0.Add(140*time.Minute))
	corr = owner.must(200, "POST", base+"/insights/funnel/correlation", map[string]any{"funnel": funnel})
	pos := corr["positive"].([]any)
	if len(pos) != 1 || pos[0].(map[string]any)["name"] != "/pricing" || pos[0].(map[string]any)["converted"] != 2.0 || pos[0].(map[string]any)["dropped"] != 1.0 {
		t.Fatalf("positive: %v", corr)
	}
	if pos[0].(map[string]any)["odds_ratio"].(float64) <= 1 {
		t.Fatalf("odds: %v", pos[0])
	}
	owner.must(400, "POST", base+"/insights/funnel/correlation", map[string]any{"funnel": funnel, "step": 1})

	// Paths from signing up: everyone's next step is a page, then Bo's
	// support contact and the payments.
	paths := map[string]any{"date_range": map[string]any{"from": from, "to": to}, "start": map[string]any{"kind": "event", "name": "signed_up"},
		"steps": 3, "window": map[string]any{"value": 2, "unit": "day"}}
	pr := owner.must(200, "POST", base+"/insights/paths", paths)
	if pr["people"] != 4.0 {
		t.Fatalf("paths people: %v", pr)
	}
	nodes := pr["nodes"].([]any)
	byStep := map[float64]map[string]float64{}
	for _, n := range nodes {
		n := n.(map[string]any)
		if byStep[n["step"].(float64)] == nil {
			byStep[n["step"].(float64)] = map[string]float64{}
		}
		byStep[n["step"].(float64)][n["kind"].(string)+":"+n["name"].(string)] = n["count"].(float64)
	}
	if byStep[1]["event:signed_up"] != 4 || byStep[2]["page:/pricing"] != 2 || byStep[2]["page:/docs"] != 2 {
		t.Fatalf("path nodes: %v", nodes)
	}
	var dropped float64
	for _, n := range nodes {
		n := n.(map[string]any)
		if n["step"] == 2.0 && n["name"] == "/docs" {
			dropped = n["drop_off"].(float64)
		}
	}
	// Of the two at /docs, the browser went nowhere; Cy went on to /pricing.
	if dropped != 1 {
		t.Fatalf("drop off: %v", nodes)
	}
	links := pr["links"].([]any)
	if len(links) == 0 || links[0].(map[string]any)["step"] != 1.0 {
		t.Fatalf("links: %v", links)
	}
	// Towards an end: paths ending in paying.
	pr = owner.must(200, "POST", base+"/insights/paths", map[string]any{"date_range": map[string]any{"from": from, "to": to},
		"end": map[string]any{"kind": "event", "name": "paid"}, "steps": 3, "window": map[string]any{"value": 2, "unit": "day"}})
	if pr["people"] != 2.0 {
		t.Fatalf("end paths: %v", pr)
	}
	for _, n := range pr["nodes"].([]any) {
		n := n.(map[string]any)
		if n["step"] == 3.0 && (n["name"] != "paid" || n["count"] != 2.0) {
			t.Fatalf("end node: %v", n)
		}
	}
	// Only pages, with /docs excluded.
	pr = owner.must(200, "POST", base+"/insights/paths", map[string]any{"date_range": map[string]any{"from": from, "to": to},
		"kinds": []string{"page"}, "exclude": []map[string]any{{"kind": "page", "name": "/docs"}}})
	for _, n := range pr["nodes"].([]any) {
		if n.(map[string]any)["name"] == "/docs" || n.(map[string]any)["kind"] == "event" {
			t.Fatalf("excluded node: %v", n)
		}
	}
	for _, bad := range []map[string]any{
		{"start": map[string]any{"kind": "event", "name": "x"}, "end": map[string]any{"kind": "event", "name": "y"}},
		{"start": map[string]any{"kind": "page", "name": "pricing"}},
		{"steps": 9},
		{"kinds": []string{"visit"}},
	} {
		owner.must(400, "POST", base+"/insights/paths", bad)
	}
	// Saved as an insight.
	ins := owner.must(201, "POST", base+"/insights", map[string]any{"kind": "paths", "name": "After signing up", "spec": paths})
	if ins["kind"] != "paths" {
		t.Fatalf("saved paths: %v", ins)
	}
	owner.must(400, "POST", base+"/insights", map[string]any{"kind": "paths", "name": "Bad", "spec": map[string]any{"steps": 99}})

	// Annotations.
	ann := owner.must(201, "POST", base+"/annotations", map[string]any{"day": t0.Format(time.DateOnly), "text": "New pricing page"})
	if ann["day"] != t0.Format(time.DateOnly) || ann["text"] != "New pricing page" {
		t.Fatalf("annotation: %v", ann)
	}
	owner.must(400, "POST", base+"/annotations", map[string]any{"day": "yesterday", "text": "x"})
	owner.must(400, "POST", base+"/annotations", map[string]any{"day": t0.Format(time.DateOnly), "text": ""})
	list := owner.must(200, "GET", base+"/annotations?from="+from+"&to="+to, nil)
	if l := list["data"].([]any); len(l) != 1 || l[0].(map[string]any)["id"] != ann["id"] {
		t.Fatalf("annotations: %v", list)
	}
	if l := owner.must(200, "GET", base+"/annotations?from=2020-01-01&to=2020-01-31", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("other dates: %v", l)
	}
	owner.must(400, "GET", base+"/annotations?from=2020-01-01&to=2024-01-01", nil)
	owner.must(204, "DELETE", base+"/annotations/"+ann["id"].(string), nil)
	owner.must(404, "DELETE", base+"/annotations/"+ann["id"].(string), nil)
	owner.must(404, "DELETE", base+"/annotations/not-a-uuid", nil)

	for _, name := range []string{"insight.people_saved", "annotation.created", "annotation.deleted"} {
		if len(named(got(), name)) == 0 {
			t.Fatalf("no %s event", name)
		}
	}
}
