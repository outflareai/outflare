package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Unmerge (plans/brands/README.md, "Undo for bad data"): a merge can be
// undone, and every moved row goes back; what came to the kept record
// later stays there.
func TestUnmerge(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	acme := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})["id"].(string)
	brand := "/v1/brands/" + acme
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	event := func(rec, name, msg string) {
		t.Helper()
		if ok, err := customers.Record(ctx, e.pool, ws, customers.Event{RecordID: rec, Name: name, MessageID: msg}); err != nil || !ok {
			t.Fatalf("event %s: %v %v", name, ok, err)
		}
	}
	firing := func() int {
		t.Helper()
		return count(`SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind LIKE ANY ('{rule.%,journey.%}'::text[])`, ws) +
			count(`SELECT count(*) FROM webhook_deliveries WHERE workspace_id = $1 AND event_type <> 'record.unmerged'`, ws)
	}
	owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"record.*", "identity.*", "relationship.*"}})
	change := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Title set", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "job_title", "op": "exists"}}, "emit_event": "customer.title_set"})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+change+"/activate", nil)

	company := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak", "brand_id": acme})["id"].(string)
	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana Lima", "brand_id": acme,
		"identities": []map[string]any{{"namespace": "email", "value": "ana@example.com", "verified": true}}})["id"].(string)
	dup := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "A. Lima", "brand_id": acme,
		"attributes": map[string]any{"job_title": "Head of Growth"},
		"identities": []map[string]any{{"namespace": "email", "value": "ana.lima@example.com"}, {"namespace": "phone", "value": "+61412345678"}}})["id"].(string)
	owner.must(201, "POST", base+"/records/"+dup+"/relationships", map[string]any{"to": company, "kind": "member_of"})
	event(dup, "product_activated", "m-dup")

	merged := owner.must(200, "POST", base+"/records/"+ana+"/merge", map[string]any{"from": dup})
	if merged["record"].(map[string]any)["attributes"].(map[string]any)["job_title"] != "Head of Growth" {
		t.Fatalf("merge filled the field: %v", merged)
	}
	if count(`SELECT count(*) FROM customer_events WHERE record_id = $1`, ana) != 1 || count(`SELECT count(*) FROM record_identities WHERE record_id = $1 AND valid_to IS NULL`, dup) != 0 {
		t.Fatal("the merge moved nothing")
	}
	// Ana does something after the merge: it stays hers.
	event(ana, "report_viewed", "m-after")
	owner.must(201, "POST", base+"/records/"+ana+"/identities", map[string]any{"namespace": "user_id", "value": "u_ana"})

	list := owner.must(200, "GET", brand+"/merges?record="+dup, nil)["data"].([]any)
	if len(list) != 1 {
		t.Fatalf("merges: %v", list)
	}
	m := list[0].(map[string]any)
	if m["into_id"] != ana || m["from_id"] != dup || m["reversible"] != true || m["undoable"] != true || m["undone_at"] != nil {
		t.Fatalf("merge: %v", m)
	}
	mergeID := m["id"].(string)

	// Only the brand's managers can undo it.
	reader := agentToken(t, owner, base, "Reader", "customers.read")
	reader.must(403, "POST", brand+"/merges/"+mergeID+"/undo", nil)
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket app", "scopes": []string{"customers.manage", "customers.read"}, "brand": "repocket"})
	other := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	other.must(404, "POST", brand+"/merges/"+mergeID+"/undo", nil)
	owner.must(404, "POST", "/v1/brands/"+rp+"/merges/"+mergeID+"/undo", nil)
	if l := owner.must(200, "GET", "/v1/brands/"+rp+"/merges", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("another brand's merges: %v", l)
	}

	before := firing()
	undo := owner.must(200, "POST", brand+"/merges/"+mergeID+"/undo", nil)
	if undo["merge"].(map[string]any)["undone_at"] == nil || undo["merge"].(map[string]any)["undoable"] != false || len(undo["not_restored"].([]any)) != 0 {
		t.Fatalf("undo: %v", undo)
	}
	if r := undo["restored"].(map[string]any); r["customer_events"] != 1.0 || r["record_identities"] != 2.0 || r["fields"] == nil {
		t.Fatalf("restored: %v", r)
	}
	// Both people are whole again.
	back := owner.must(200, "GET", base+"/records/"+dup, nil)
	if back["merged_into"] != nil || back["archived"] != false || back["attributes"].(map[string]any)["job_title"] != "Head of Growth" {
		t.Fatalf("the merged record: %v", back)
	}
	ids := map[string]bool{}
	for _, i := range back["identities"].([]any) {
		ids[i.(map[string]any)["value"].(string)] = true
	}
	if !ids["ana.lima@example.com"] || !ids["+61412345678"] || len(ids) != 2 {
		t.Fatalf("its identities: %v", back["identities"])
	}
	kept := owner.must(200, "GET", base+"/records/"+ana, nil)
	if _, has := kept["attributes"].(map[string]any)["job_title"]; has {
		t.Fatalf("the field the merge filled is gone again: %v", kept["attributes"])
	}
	ids = map[string]bool{}
	for _, i := range kept["identities"].([]any) {
		ids[i.(map[string]any)["value"].(string)] = true
	}
	if !ids["ana@example.com"] || !ids["u_ana"] || len(ids) != 2 {
		t.Fatalf("the kept record's identities: %v", kept["identities"])
	}
	if count(`SELECT count(*) FROM customer_events WHERE record_id = $1 AND message_id = 'm-dup'`, dup) != 1 ||
		count(`SELECT count(*) FROM customer_events WHERE record_id = $1 AND message_id = 'm-after'`, ana) != 1 {
		t.Fatal("events: the moved one goes back, the later one stays")
	}
	if count(`SELECT count(*) FROM record_relationships WHERE from_id = $1 AND to_id = $2 AND valid_to IS NULL`, dup, company) != 1 ||
		count(`SELECT count(*) FROM record_relationships WHERE (from_id = $1 OR to_id = $1) AND to_id = $2 AND valid_to IS NULL`, ana, company) != 0 {
		t.Fatal("the link goes back")
	}
	if count(`SELECT count(*) FROM record_merge_candidates WHERE status = 'merged' AND (record_a = $1 OR record_b = $1)`, dup) != 0 {
		t.Fatal("the pair is open again")
	}
	if n := firing(); n != before {
		t.Fatalf("undoing fired %d rules, journeys or webhooks", n-before)
	}
	if count(`SELECT count(*) FROM webhook_deliveries WHERE workspace_id = $1 AND event_type = 'record.unmerged'`, ws) != 1 {
		t.Fatal("record.unmerged is a webhook event")
	}
	mustCode(t, owner, 409, "merge_already_undone", "POST", brand+"/merges/"+mergeID+"/undo", nil)

	// A merge from before the ledger can't be undone.
	owner.must(200, "POST", base+"/records/"+ana+"/merge", map[string]any{"from": dup})
	var again string
	if err := e.pool.QueryRow(ctx, `UPDATE record_merges SET reversible = false WHERE from_id = $1 AND undone_at IS NULL RETURNING id::text`, dup).Scan(&again); err != nil {
		t.Fatal(err)
	}
	mustCode(t, owner, 409, "merge_not_reversible", "POST", brand+"/merges/"+again+"/undo", nil)

	// Chained merges undo newest first.
	c1 := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy", "brand_id": acme,
		"identities": []map[string]any{{"namespace": "email", "value": "cy@example.com"}}})["id"].(string)
	c2 := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy Two", "brand_id": acme})["id"].(string)
	c3 := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy Three", "brand_id": acme})["id"].(string)
	owner.must(200, "POST", base+"/records/"+c2+"/merge", map[string]any{"from": c1})
	owner.must(200, "POST", base+"/records/"+c3+"/merge", map[string]any{"from": c2})
	first := owner.must(200, "GET", brand+"/merges?record="+c1, nil)["data"].([]any)[0].(map[string]any)
	second := owner.must(200, "GET", brand+"/merges?record="+c3, nil)["data"].([]any)[0].(map[string]any)
	if first["undoable"] != false || second["undoable"] != true {
		t.Fatalf("only the newest is undoable: %v / %v", first, second)
	}
	mustCode(t, owner, 409, "merge_not_latest", "POST", brand+"/merges/"+first["id"].(string)+"/undo", nil)
	owner.must(200, "POST", brand+"/merges/"+second["id"].(string)+"/undo", nil)
	if r := owner.must(200, "GET", base+"/records/"+c1, nil); r["merged_into"] != c2 {
		t.Fatalf("c1 leads to c2 again: %v", r["merged_into"])
	}
	owner.must(200, "POST", brand+"/merges/"+first["id"].(string)+"/undo", nil)
	if r := owner.must(200, "GET", base+"/records/"+c1, nil); r["merged_into"] != nil || len(r["identities"].([]any)) != 1 {
		t.Fatalf("c1 is its own again: %v", r)
	}
	if !captured(got, "record.unmerged") {
		t.Fatal("record.unmerged is a product event")
	}
}

// Undo a batch (the owner's answer of 2026-10-30): the preview counts what
// a batch made and lists what changed since; undoing only what's
// untouched leaves the changed items, undoing everything takes them too,
// and either is one deletion, restorable in one step.
func TestUndoBatch(t *testing.T) {
	got := track.Capture(t)
	defer api.SetCRMImportSizes(2, 1, 0)()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	acme := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})
	brand := "/v1/brands/" + acme
	e.integrations.Client.BaseURLs["attio"] = fakeAttioCRM(t).URL
	e.runCRMImports(t)
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	firing := func() int {
		t.Helper()
		return count(`SELECT count(*) FROM jobs WHERE workspace_id = $1 AND kind LIKE ANY ('{rule.%,journey.%}'::text[])`, ws) +
			count(`SELECT count(*) FROM webhook_deliveries WHERE workspace_id = $1 AND event_type <> 'batch.undone'`, ws)
	}
	owner.must(201, "POST", base+"/webhooks", map[string]any{"url": "https://example.com/hook", "events": []string{"record.*", "identity.*", "relationship.*"}})
	change := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Title set", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "job_title", "op": "exists"}}, "emit_event": "customer.title_set"})["id"].(string)
	owner.must(200, "POST", base+"/rules/"+change+"/activate", nil)

	// Bo was here before the import; the import fills in his title.
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo", "brand_id": acme,
		"identities": []map[string]any{{"namespace": "email", "value": "bo@example.com", "verified": true}}})["id"].(string)
	code, imp := owner.raw("POST", base+"/leads/import?list=Pilot", "text/csv",
		"email,first_name,last_name,title\nkim@example.com,Kim,Lee,CTO\nlee@example.com,Lee,Park,CEO\nmax@example.com,Max,Ng,VP\nbo@example.com,Bo,Diaz,COO\n")
	if code != 200 || imp["batch_id"] == nil {
		t.Fatalf("import: %d %v", code, imp)
	}
	batch := imp["batch_id"].(string)
	person := func(email string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'email' AND value = $2`, ws, email).Scan(&id); err != nil {
			t.Fatalf("%s: %v", email, err)
		}
		return id
	}
	kim, lee, max := person("kim@example.com"), person("lee@example.com"), person("max@example.com")
	if b := owner.must(200, "GET", base+"/records/"+bo, nil); b["attributes"].(map[string]any)["job_title"] != "COO" {
		t.Fatalf("the import filled Bo in: %v", b["attributes"])
	}

	list := owner.must(200, "GET", brand+"/batches", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != batch {
		t.Fatalf("batches: %v", list)
	}
	b := owner.must(200, "GET", brand+"/batches/"+batch, nil)
	c := b["counts"].(map[string]any)
	if b["kind"] != "csv_import" || b["label"] != "Pilot" || c["records"] != 3.0 || c["leads"] != 4.0 || c["list_memberships"] != 4.0 || c["field_values"] == 0.0 {
		t.Fatalf("batch: %v", b)
	}

	// Since the import, someone edited Kim and Lee was sent an email.
	owner.must(200, "PATCH", base+"/records/"+kim, map[string]any{"attributes": map[string]any{"job_title": "Founder"}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO emails (workspace_id, purpose, from_address, to_addresses, subject, status, record_id, sent_at)
		VALUES ($1, 'transactional', 'hello@example.com', '{lee@example.com}', 'Welcome', 'sent', $2, now())`, ws, lee); err != nil {
		t.Fatal(err)
	}

	pre := owner.must(200, "GET", brand+"/batches/"+batch+"/undo-preview", nil)
	cc := pre["changed_counts"].(map[string]any)
	if cc["edited"] != 1.0 || cc["emailed"] != 1.0 || len(pre["changed"].([]any)) != 2 {
		t.Fatalf("changed: %v", pre)
	}
	if u, ev := pre["untouched"].(map[string]any), pre["everything"].(map[string]any); u["records"] != 1.0 || u["records_kept"] != 2.0 ||
		ev["records"] != 3.0 || ev["records_kept"] != 0.0 || u["field_values"] == 0.0 {
		t.Fatalf("modes: %v / %v", u, ev)
	}

	// Another brand's token sees nothing of it.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Repocket app", "scopes": []string{"customers.manage", "customers.read"}, "brand": "repocket"})
	other := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	other.must(404, "GET", brand+"/batches/"+batch, nil)
	other.must(404, "POST", brand+"/batches/"+batch+"/undo", map[string]any{"mode": "everything"})
	owner.must(400, "POST", brand+"/batches/"+batch+"/undo", map[string]any{"mode": "all"})

	// Only what's untouched.
	before := firing()
	done := owner.must(200, "POST", brand+"/batches/"+batch+"/undo", map[string]any{"mode": "untouched"})
	u := done["undo"].(map[string]any)
	if u["status"] != "done" || u["mode"] != "untouched" || u["deletion_id"] == nil || u["counts"].(map[string]any)["records"] != 1.0 ||
		u["counts"].(map[string]any)["records_kept"] != 2.0 {
		t.Fatalf("undone: %v", done)
	}
	owner.must(404, "GET", base+"/records/"+max, nil)
	owner.must(200, "GET", base+"/records/"+kim, nil)
	owner.must(200, "GET", base+"/records/"+lee, nil)
	if b := owner.must(200, "GET", base+"/records/"+bo, nil); b["attributes"].(map[string]any)["job_title"] != nil {
		t.Fatalf("Bo's title goes back: %v", b["attributes"])
	}
	if count(`SELECT count(*) FROM leads WHERE workspace_id = $1 AND lower(email) IN ('max@example.com', 'bo@example.com')`, ws) != 0 ||
		count(`SELECT count(*) FROM leads WHERE workspace_id = $1 AND lower(email) IN ('kim@example.com', 'lee@example.com')`, ws) != 2 {
		t.Fatal("leads: the untouched ones go")
	}
	if n := firing(); n != before {
		t.Fatalf("undoing fired %d rules, journeys or webhooks", n-before)
	}
	mustCode(t, owner, 409, "batch_already_undone", "POST", brand+"/batches/"+batch+"/undo", map[string]any{"mode": "everything"})
	deleted := owner.must(200, "GET", brand+"/recently-deleted?kind=batch", nil)["data"].([]any)
	if len(deleted) != 1 || deleted[0].(map[string]any)["id"] != u["deletion_id"] || deleted[0].(map[string]any)["label"] != "Pilot" {
		t.Fatalf("recently deleted: %v", deleted)
	}

	// Restored in one step, and then it can be undone again.
	res := owner.must(200, "POST", brand+"/recently-deleted/"+u["deletion_id"].(string)+"/restore", nil)
	if res["outcome"] != "restored" {
		t.Fatalf("restore: %v", res)
	}
	owner.must(200, "GET", base+"/records/"+max, nil)
	if b := owner.must(200, "GET", base+"/records/"+bo, nil); b["attributes"].(map[string]any)["job_title"] != "COO" {
		t.Fatalf("Bo's imported title is back: %v", b["attributes"])
	}
	if count(`SELECT count(*) FROM leads WHERE workspace_id = $1`, ws) != 4 {
		t.Fatal("the leads are back")
	}

	// Everything: the changed records go too, and come back from Recently
	// deleted.
	done = owner.must(200, "POST", brand+"/batches/"+batch+"/undo", map[string]any{"mode": "everything"})
	if done["undo"].(map[string]any)["counts"].(map[string]any)["records"] != 3.0 {
		t.Fatalf("everything: %v", done)
	}
	for _, id := range []string{kim, lee, max} {
		owner.must(404, "GET", base+"/records/"+id, nil)
	}
	// The email stays: it was sent.
	if count(`SELECT count(*) FROM emails WHERE record_id = $1`, lee) != 1 {
		t.Fatal("the email's history stays")
	}
	owner.must(200, "POST", brand+"/recently-deleted/"+done["undo"].(map[string]any)["deletion_id"].(string)+"/restore", nil)
	for _, id := range []string{kim, lee, max} {
		owner.must(200, "GET", base+"/records/"+id, nil)
	}
	if k := owner.must(200, "GET", base+"/records/"+kim, nil); k["attributes"].(map[string]any)["job_title"] != "Founder" {
		t.Fatalf("Kim comes back with the edit: %v", k["attributes"])
	}

	// A batch of events: undoing it removes its events and the person it
	// made.
	sent := owner.must(202, "POST", brand+"/events", map[string]any{"source": "segment", "batch": []any{
		map[string]any{"type": "identify", "userId": "u_new", "messageId": "e1", "traits": map[string]any{"email": "new@example.com"}},
		map[string]any{"type": "identify", "userId": "u_new", "messageId": "e2", "traits": map[string]any{"plan": "pro"}},
	}})
	evBatch := sent["batch_id"].(string)
	deadline := time.Now().Add(30 * time.Second)
	for count(`SELECT count(*) FROM event_inbox WHERE batch_id = $1 AND status = 'pending'`, evBatch) > 0 {
		if time.Now().After(deadline) {
			t.Fatal("events still pending")
		}
		time.Sleep(30 * time.Millisecond)
	}
	eb := owner.must(200, "GET", brand+"/batches/"+evBatch, nil)
	if eb["kind"] != "events" || eb["source"] != "segment" || eb["counts"].(map[string]any)["events"] == 0.0 || eb["counts"].(map[string]any)["records"] != 1.0 {
		t.Fatalf("event batch: %v", eb)
	}
	owner.must(200, "POST", brand+"/batches/"+evBatch+"/undo", map[string]any{"mode": "untouched"})
	if count(`SELECT count(*) FROM customer_events WHERE batch_id = $1`, evBatch) != 0 ||
		count(`SELECT count(*) FROM records WHERE batch_id = $1 AND deleted_at IS NULL`, evBatch) != 0 {
		t.Fatal("the event batch's events and person are gone")
	}

	// A CRM import is a batch too, with the import's id.
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "attio", "credentials": map[string]string{"api_key": "at-key"}})
	run := owner.must(202, "POST", base+"/integrations/"+i["id"].(string)+"/crm-imports", map[string]any{})
	if v := waitCRMImport(t, owner, base, run["id"].(string)); v["status"] != "done" {
		t.Fatalf("import: %v", v)
	}
	crm := owner.must(200, "GET", brand+"/batches/"+run["id"].(string)+"/undo-preview", nil)
	if crm["batch"].(map[string]any)["kind"] != "crm_import" || crm["made"].(map[string]any)["records"].(float64) < 4 ||
		crm["everything"].(map[string]any)["records"] != crm["made"].(map[string]any)["records"] {
		t.Fatalf("crm preview: %v", crm)
	}
	// A large batch is undone in the background.
	defer api.SetUndoInline(1)()
	queued := owner.must(202, "POST", brand+"/batches/"+run["id"].(string)+"/undo", map[string]any{"mode": "everything"})
	if queued["undo"].(map[string]any)["status"] != "queued" {
		t.Fatalf("queued: %v", queued)
	}
	// While it runs, or once it is done, it can't be undone again.
	if st, out := owner.do("POST", brand+"/batches/"+run["id"].(string)+"/undo", map[string]any{"mode": "everything"}); st != 409 ||
		(out["error"].(map[string]any)["code"] != "batch_undo_running" && out["error"].(map[string]any)["code"] != "batch_already_undone") {
		t.Fatalf("undo again: %d %v", st, out)
	}
	deadline = time.Now().Add(30 * time.Second)
	for owner.must(200, "GET", brand+"/batches/"+run["id"].(string), nil)["undo"].(map[string]any)["status"] != "done" {
		if time.Now().After(deadline) {
			t.Fatal("the undo job did not finish")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if count(`SELECT count(*) FROM records WHERE batch_id = $1 AND deleted_at IS NULL`, run["id"]) != 0 {
		t.Fatal("the CRM import's records are gone")
	}
	if !captured(got, "batch.undone") {
		t.Fatal("batch.undone is a product event")
	}
}

// mustCode checks a call fails with status and error code.
func mustCode(t *testing.T, c *client, status int, code, method, path string, body any) {
	t.Helper()
	got, out := c.do(method, path, body)
	if e, _ := out["error"].(map[string]any); got != status || e == nil || e["code"] != code {
		t.Fatalf("%s %s: %d %v, want %d %s", method, path, got, out, status, code)
	}
}

// captured reports whether a product event of that name was recorded.
func captured(got func() []track.Event, name string) bool {
	for _, ev := range got() {
		if ev.Name == name {
			return true
		}
	}
	return false
}
