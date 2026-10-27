package api_test

import (
	"context"
	"slices"
	"testing"
	"time"
)

// One timeline per person and company: every module's history in time
// order, a company's with its people's, filtered by brand and paged.
func TestRecordTimelineEveryModule(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@northpeak.example"}}})["id"].(string)
	owner.must(201, "POST", base+"/records/"+ana+"/relationships", map[string]any{"to": company, "kind": "works_at"})

	now := time.Now()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	// A visit for each brand, an opt-out and a finished journey.
	exec(`INSERT INTO site_sessions (workspace_id, anonymous_id, record_id, brand_id, landing_path, pageviews, started_at, last_seen_at)
		VALUES ($1, 'a1', $2, $3, '/pricing', 3, $4, $4), ($1, 'a2', $2, $5, '/blog', 1, $6, $6)`,
		ws, ana, acme, now.Add(-3*time.Hour), other, now.Add(-4*time.Hour))
	exec(`INSERT INTO suppressions (workspace_id, kind, value, reason, source, method, record_id, created_at)
		VALUES ($1, 'email', 'ana@northpeak.example', 'unsubscribed', 'unsubscribe_link', 'automated', $2, $3)`, ws, ana, now.Add(-time.Hour))
	var journey string
	if err := e.pool.QueryRow(ctx, `INSERT INTO journeys (workspace_id, name, brand_id) VALUES ($1, 'Welcome', $2) RETURNING id::text`, ws, acme).Scan(&journey); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO journey_runs (workspace_id, journey_id, record_id, status, entered_at, finished_at)
		VALUES ($1, $2, $3, 'completed', $4, $5)`, ws, journey, ana, now.Add(-2*time.Hour), now.Add(-90*time.Minute))

	type item struct{ kind, title, record string }
	read := func(path string) []item {
		t.Helper()
		var out []item
		for _, it := range owner.must(200, "GET", path, nil)["data"].([]any) {
			it := it.(map[string]any)
			out = append(out, item{it["kind"].(string), it["title"].(string), it["record_id"].(string)})
		}
		return out
	}
	kinds := "visit,opt_out,journey"
	want := []item{{"opt_out", "unsubscribed", ana}, {"journey", "Welcome", ana}, {"journey", "Welcome", ana},
		{"visit", "/pricing", ana}, {"visit", "/blog", ana}}
	if got := read(base + "/records/" + ana + "/timeline?kinds=" + kinds); !slices.Equal(got, want) {
		t.Fatalf("person = %v", got)
	}
	// The company sees its people's history, and only its own without them.
	if got := read(base + "/records/" + company + "/timeline?kinds=" + kinds); !slices.Equal(got, want) {
		t.Fatalf("company = %v", got)
	}
	if got := read(base + "/records/" + company + "/timeline?people=false&kinds=" + kinds); len(got) != 0 {
		t.Fatalf("company alone = %v", got)
	}
	// One brand: its own items and those no brand owns.
	got := read(base + "/records/" + ana + "/timeline?brand=northpeak&kinds=" + kinds)
	if !slices.Equal(got, []item{{"opt_out", "unsubscribed", ana}, {"visit", "/blog", ana}}) {
		t.Fatalf("one brand = %v", got)
	}
	// Pages carry on where the last one ended.
	page := owner.must(200, "GET", base+"/records/"+ana+"/timeline?limit=2&kinds="+kinds, nil)
	next, _ := page["next_before"].(string)
	if len(page["data"].([]any)) != 2 || next == "" {
		t.Fatalf("page = %v", page)
	}
	rest := owner.must(200, "GET", base+"/records/"+ana+"/timeline?limit=10&kinds="+kinds+"&before="+next, nil)["data"].([]any)
	if len(rest) != 3 {
		t.Fatalf("rest = %v", rest)
	}
	owner.must(400, "GET", base+"/records/"+ana+"/timeline?kinds=visit,tweets", nil)
	owner.must(400, "GET", base+"/records/"+ana+"/timeline?people=maybe", nil)
	// Every kind reads together.
	owner.must(200, "GET", base+"/records/"+company+"/timeline", nil)
}
