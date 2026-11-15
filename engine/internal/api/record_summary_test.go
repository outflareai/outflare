package api_test

import (
	"context"
	"testing"
	"time"
)

// The account summary: one call that says where a person or company
// stands, from the brand's stored data, and what needs attention.
func TestRecordSummary(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "identities": []map[string]any{{"namespace": "domain", "value": "acme.example"}}})
	brand, acmeID := acme["brand_id"].(string), acme["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana", "attributes": map[string]any{"job_title": "Head of data"},
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	root := "/v1/brands/" + brand
	owner.must(201, "POST", root+"/records/"+ana+"/relationships", map[string]any{"to": acmeID, "kind": "member_of", "properties": map[string]any{"role": "buyer"}})
	sum := func(rec string) map[string]any {
		t.Helper()
		return owner.must(200, "GET", root+"/records/"+rec+"/summary", nil)
	}
	obj := func(v map[string]any, k string) map[string]any { t.Helper(); return v[k].(map[string]any) }

	// Nothing yet: every card is empty, and nothing needs attention.
	got := sum(acmeID)
	if got["type"] != "account" || got["owner"] != nil || got["billing"] != nil || len(got["attention"].([]any)) != 0 ||
		len(got["products"].([]any)) != 0 || obj(got, "contact")["last_at"] != nil || obj(got, "stage")["stage"] != nil {
		t.Fatalf("empty summary: %v", got)
	}
	people := got["people"].([]any)
	if len(people) != 1 || got["people_total"] != 1.0 || people[0].(map[string]any)["role"] != "buyer" || people[0].(map[string]any)["job_title"] != "Head of data" {
		t.Fatalf("people: %v", people)
	}
	// The person's summary names the company.
	if c := sum(ana)["company"].(map[string]any); c["id"] != acmeID || c["name"] != "Acme" {
		t.Fatalf("company: %v", c)
	}

	// A stage, an owner and a plan, set like any field.
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", root+"/records/"+acmeID, map[string]any{"version": acme["version"], "attributes": map[string]any{
		"lifecycle_stage": "customer", "owner": me, "goal": "Move to a yearly plan", "hesitations": "Price"}})
	// A failed payment, a waiting support conversation, an overdue task,
	// an open deal and a visit.
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, kind, status, amount_minor, currency, brand_amount_minor, brand_currency, source, source_id, occurred_at)
		VALUES ($1, $2, $3, 'payment', 'succeeded', 5000, 'USD', 5000, 'USD', 'api', 'p1', now() - interval '40 days'),
			($1, $2, $3, 'payment', 'succeeded', 9000, 'USD', 9000, 'USD', 'api', 'p2', now() - interval '3 days'),
			($1, $2, $3, 'payment', 'failed', 9000, 'USD', 9000, 'USD', 'api', 'p3', now() - interval '1 day')`, ws, brand, acmeID)
	exec(`INSERT INTO support_conversations (workspace_id, brand_id, record_id, channel, subject, status, last_from, last_message_at)
		VALUES ($1, $2, $3, 'email', 'Fictional question', 'open', 'customer', now() - interval '2 hours')`, ws, brand, ana)
	exec(`INSERT INTO crm_activities (workspace_id, brand_id, record_id, kind, title, status, due_at)
		VALUES ($1, $2, $3, 'task', 'Call about renewal', 'open', now() - interval '1 day')`, ws, brand, acmeID)
	exec(`INSERT INTO deals (workspace_id, brand_id, record_id, source, state, name) VALUES ($1, $2, $3, 'manual', 'open', 'Fictional renewal')`, ws, brand, ana)
	exec(`INSERT INTO site_sessions (workspace_id, brand_id, record_id, anonymous_id, started_at, last_seen_at, landing_path, utm_source)
		VALUES ($1, $2, $3, 'anon-1', $4, $4, '/pricing', 'newsletter')`, ws, brand, ana, time.Now().Add(-time.Hour))

	got = sum(acmeID)
	if st := obj(got, "stage"); st["stage"] != "customer" || st["since"] == nil || st["by"] == nil {
		t.Fatalf("stage: %v", st)
	}
	if o := obj(got, "owner"); o["id"] != me {
		t.Fatalf("owner: %v", o)
	}
	if p := obj(got, "plan"); p["goal"] != "Move to a yearly plan" || p["hesitations"] != "Price" || len(p) != 2 {
		t.Fatalf("plan: %v", p)
	}
	if b := obj(got, "billing"); b["paid_30d"] != 9000.0 || b["paid_prev_30d"] != 5000.0 || b["failed_payments_30d"] != 1.0 || b["currency"] != "USD" || b["last_payment_at"] == nil {
		t.Fatalf("billing: %v", b)
	}
	if c := obj(got, "conversations"); c["open"] != 1.0 || c["waiting"] != 1.0 || len(c["latest"].([]any)) != 1 {
		t.Fatalf("conversations: %v", c)
	}
	if c := obj(got, "contact"); c["last_channel"] != "support" || c["last_direction"] != "in" || c["last_in_at"] == nil {
		t.Fatalf("contact: %v", c)
	}
	if tk := obj(got, "tasks"); tk["open"] != 1.0 || tk["overdue"] != 1.0 || len(tk["next"].([]any)) != 1 {
		t.Fatalf("tasks: %v", tk)
	}
	if d := obj(got, "deals"); d["open"] != 1.0 || len(d["latest"].([]any)) != 1 {
		t.Fatalf("deals: %v", d)
	}
	if w := obj(got, "website"); w["visits_30d"] != 1.0 || w["last_path"] != "/pricing" || obj(got, "origin")["source"] != "newsletter" {
		t.Fatalf("website: %v %v", w, got["origin"])
	}
	var kinds []string
	for _, a := range got["attention"].([]any) {
		kinds = append(kinds, a.(map[string]any)["kind"].(string))
	}
	if want := []string{"conversation_waiting", "task_overdue", "payment_failed"}; len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] || kinds[2] != want[2] {
		t.Fatalf("attention: %v", kinds)
	}

	// An agreement that renews in a month is on the company's page and
	// needs attention.
	owner.must(200, "POST", root+"/kits/agreements/apply", nil)
	renews := time.Now().Add(30 * 24 * time.Hour).Format(time.DateOnly)
	owner.must(201, "POST", root+"/records", map[string]any{"type": "agreement", "name": "Acme yearly plan",
		"attributes": map[string]any{"stage": "active", "company": acmeID, "renews_on": renews}})
	got = sum(acmeID)
	pipes := got["pipelines"].([]any)
	if len(pipes) != 1 || pipes[0].(map[string]any)["type"] != "agreement" || pipes[0].(map[string]any)["renews_on"] != renews {
		t.Fatalf("agreement: %v", pipes)
	}
	found := false
	for _, a := range got["attention"].([]any) {
		found = found || a.(map[string]any)["kind"] == "renewal_due"
	}
	if !found {
		t.Fatalf("no renewal_due: %v", got["attention"])
	}

	// Only people and companies have a summary, and only in their brand.
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo"})["id"].(string)
	owner.must(404, "GET", "/v1/brands/"+other+"/records/"+acmeID+"/summary", nil)
}

// Support conversations, tasks and notes are on the record's timeline.
func TestTimelineSupportTasksNotes(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	ana := owner.must(201, "POST", "/v1/workspaces/"+ws+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []map[string]any{{"namespace": "email", "value": "ana@acme.example"}}})
	brand, id := ana["brand_id"].(string), ana["id"].(string)
	for _, q := range []string{
		`INSERT INTO support_conversations (workspace_id, brand_id, record_id, channel, subject, status, last_from, closed_at) VALUES ($1, $2, $3, 'email', 'Fictional question', 'closed', 'team', now())`,
		`INSERT INTO crm_activities (workspace_id, brand_id, record_id, kind, title, status) VALUES ($1, $2, $3, 'task', 'Call about renewal', 'open'), ($1, $2, $3, 'note', 'Met at the fair', 'open')`,
	} {
		if _, err := e.pool.Exec(ctx, q, ws, brand, id); err != nil {
			t.Fatal(err)
		}
	}
	got := owner.must(200, "GET", "/v1/brands/"+brand+"/records/"+id+"/timeline?kinds=support,task,note", nil)["data"].([]any)
	count := map[string]int{}
	for _, it := range got {
		count[it.(map[string]any)["kind"].(string)]++
	}
	if count["support"] != 2 || count["task"] != 1 || count["note"] != 1 {
		t.Fatalf("timeline: %v", got)
	}
}
