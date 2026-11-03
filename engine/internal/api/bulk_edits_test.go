package api_test

import (
	"context"
	"testing"
	"time"
)

// TestBulkEdits covers the owner's rules for bulk edits: only owners,
// signed in, run them (anyone who may change records previews), and every
// change is in each record's own history with the bulk edit, including an
// undo, which adds history and never erases it.
func TestBulkEdits(t *testing.T) {
	e := freshEnv(t)
	e.runServerJobs(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand, ownerID string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT user_id::text FROM memberships WHERE workspace_id = $1 AND role = 'owner'`, ws).Scan(&ownerID); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	person := func(name string, tags ...string) string {
		t.Helper()
		body := map[string]any{"type": "person", "name": name}
		if tags != nil {
			body["attributes"] = map[string]any{"tags": tags}
		}
		return owner.must(201, "POST", base+"/records", body)["id"].(string)
	}
	ana, bo, cy := person("Ana", "vip"), person("Bo"), person("Cy", "vip", "old")
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	vip := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "VIP", "kind": "static"})["id"].(string)
	wait := func(id string) map[string]any {
		t.Helper()
		for range 400 {
			v := owner.must(200, "GET", base+"/bulk-edits/"+id, nil)
			if v["status"] == "done" || v["status"] == "failed" {
				return v
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("bulk edit %s never finished", id)
		return nil
	}
	// The hard rule: every record a bulk edit changed has history rows
	// carrying it, and its values match that history.
	historyOf := func(rec, bulk string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM attribute_changes WHERE record_id = $1 AND bulk_edit_id = $2)
			+ (SELECT count(*) FROM smart_list_changes WHERE record_id = $1 AND bulk_edit_id = $2)`, rec, bulk).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	edit := map[string]any{"record_ids": []string{ana, bo, cy},
		"changes": map[string]any{"set": map[string]any{"owner": ownerID}, "add_tags": []string{"Churn-Risk"}, "remove_tags": []string{"old"}, "add_to_list": vip}}

	// Who may: an operator previews but can't run; a token can't run either.
	op, _ := joinAs(t, owner, ws, "op@acme.example", map[string]any{"role": "operator"})
	p := op.must(200, "POST", base+"/bulk-edits/preview", edit)
	if p["records"] != 3.0 || p["can_run"] != false || len(p["sample"].([]any)) != 3 {
		t.Fatalf("preview: %v", p)
	}
	first := p["sample"].([]any)[0].(map[string]any)["changes"].([]any)
	if len(first) != 3 { // owner, tags, list
		t.Fatalf("preview changes: %v", first)
	}
	if code, out := op.do("POST", base+"/bulk-edits", edit); code != 403 || out["error"].(map[string]any)["code"] != "owner_only" {
		t.Fatalf("operator ran a bulk edit: %d %v", code, out)
	}
	tok := agentToken(t, owner, "/v1/workspaces/"+ws, "Script", "customers.read", "customers.manage")
	if code, _ := tok.do("POST", base+"/bulk-edits", edit); code != 403 {
		t.Fatalf("token ran a bulk edit: %d", code)
	}

	// Checked before anything runs.
	for _, bad := range []map[string]any{
		{"record_ids": []string{ana}, "changes": map[string]any{}},
		{"record_ids": []string{ana}, "smart_list_id": vip, "changes": map[string]any{"archive": true}},
		{"record_ids": []string{ana}, "changes": map[string]any{"set": map[string]any{"no_such_field": 1}}},
		{"record_ids": []string{ana}, "changes": map[string]any{"set": map[string]any{"tags": []string{"a"}}, "add_tags": []string{"b"}}},
		{"record_ids": []string{ana, acme}, "changes": map[string]any{"archive": true}},
		{"record_ids": []string{acme}, "changes": map[string]any{"add_to_list": vip}},
	} {
		owner.must(400, "POST", base+"/bulk-edits", bad)
	}

	// The owner runs it.
	run := owner.must(202, "POST", base+"/bulk-edits", edit)
	id := run["id"].(string)
	if run["records"] != 3.0 || run["source"] != "records" {
		t.Fatalf("created: %v", run)
	}
	done := wait(id)
	counts := done["counts"].(map[string]any)
	if done["status"] != "done" || counts["changed"] != 3.0 || counts["lists"] != 3.0 || done["undo_until"] == nil {
		t.Fatalf("finished: %v", done)
	}
	for _, rec := range []string{ana, bo, cy} {
		if historyOf(rec, id) == 0 {
			t.Fatalf("record %s changed without history", rec)
		}
		h := owner.must(200, "GET", base+"/records/"+rec+"/history?field=owner", nil)["data"].([]any)
		if len(h) != 1 || h[0].(map[string]any)["bulk_edit_id"] != id || h[0].(map[string]any)["actor_id"] == nil {
			t.Fatalf("history of %s: %v", rec, h)
		}
	}
	got := owner.must(200, "GET", base+"/records/"+cy, nil)["attributes"].(map[string]any)
	if tags := got["tags"].([]any); len(tags) != 2 || tags[0] != "vip" || tags[1] != "churn-risk" {
		t.Fatalf("tags: %v", got)
	}
	if tl := owner.must(200, "GET", base+"/records/"+ana+"/timeline?kinds=list", nil)["data"].([]any); len(tl) != 1 ||
		tl[0].(map[string]any)["detail"].(map[string]any)["bulk_edit_id"] != id {
		t.Fatalf("timeline: %v", tl)
	}

	// Running it again changes nothing, and writes no history.
	again := wait(owner.must(202, "POST", base+"/bulk-edits", edit)["id"].(string))
	if c := again["counts"].(map[string]any); c["changed"] != 0.0 || c["unchanged"] != 3.0 || c["fields"] != 0.0 {
		t.Fatalf("second run: %v", again)
	}

	// Archive from a list, then undo: a hand edit made since is kept.
	arch := wait(owner.must(202, "POST", base+"/bulk-edits", map[string]any{"smart_list_id": vip,
		"changes": map[string]any{"archive": true, "set": map[string]any{"owner": nil}}})["id"].(string))
	if arch["counts"].(map[string]any)["changed"] != 3.0 || owner.must(200, "GET", base+"/records/"+bo, nil)["archived"] != true {
		t.Fatalf("archive: %v", arch)
	}
	owner.must(200, "PATCH", base+"/records/"+bo, map[string]any{"attributes": map[string]any{"owner": ownerID}})
	before := historyOf(ana, arch["id"].(string))
	undo := owner.must(202, "POST", base+"/bulk-edits/"+arch["id"].(string)+"/undo", nil)
	undone := wait(undo["id"].(string))
	if undone["source"] != "undo" || undone["counts"].(map[string]any)["kept"] != 1.0 {
		t.Fatalf("undo: %v", undone)
	}
	for _, rec := range []string{ana, bo, cy} {
		r := owner.must(200, "GET", base+"/records/"+rec, nil)
		if r["archived"] != false || r["attributes"].(map[string]any)["owner"] != ownerID {
			t.Fatalf("after undo %s: %v", rec, r)
		}
	}
	if historyOf(ana, arch["id"].(string)) != before || historyOf(ana, undo["id"].(string)) == 0 {
		t.Fatal("an undo must add history and keep the old")
	}
	owner.must(409, "POST", base+"/bulk-edits/"+arch["id"].(string)+"/undo", nil)
	owner.must(409, "POST", base+"/bulk-edits/"+undo["id"].(string)+"/undo", nil)
	if code, _ := op.do("POST", base+"/bulk-edits/"+id+"/undo", nil); code != 403 {
		t.Fatalf("operator undid: %d", code)
	}

	// Undo the first edit: list moves reversed, tags back.
	wait(owner.must(202, "POST", base+"/bulk-edits/"+id+"/undo", nil)["id"].(string))
	if m := owner.must(200, "GET", base+"/smart-lists/"+vip, nil); m["members"] != 0.0 {
		t.Fatalf("list after undo: %v", m)
	}
	got = owner.must(200, "GET", base+"/records/"+cy, nil)["attributes"].(map[string]any)
	if tags := got["tags"].([]any); len(tags) != 2 || tags[1] != "old" {
		t.Fatalf("tags after undo: %v", got)
	}

	// A saved query picks records by its id column.
	sq := owner.must(201, "POST", base+"/queries", map[string]any{"name": "Bo", "sql": "SELECT id FROM people WHERE name = 'Bo'"})["id"].(string)
	if p := owner.must(200, "POST", base+"/bulk-edits/preview", map[string]any{"saved_query_id": sq,
		"changes": map[string]any{"add_tags": []string{"q"}}}); p["records"] != 1.0 || p["sample"].([]any)[0].(map[string]any)["id"] != bo {
		t.Fatalf("query preview: %v", p)
	}
	noID := owner.must(201, "POST", base+"/queries", map[string]any{"name": "Names", "sql": "SELECT name FROM people"})["id"].(string)
	owner.must(400, "POST", base+"/bulk-edits/preview", map[string]any{"saved_query_id": noID, "changes": map[string]any{"archive": true}})

	// Listed newest first; too old to undo after 7 days.
	if l := owner.must(200, "GET", base+"/bulk-edits?limit=2", nil); len(l["data"].([]any)) != 2 || l["next_cursor"] == nil {
		t.Fatalf("list: %v", l)
	}
	old := wait(owner.must(202, "POST", base+"/bulk-edits", map[string]any{"record_ids": []string{acme},
		"changes": map[string]any{"add_tags": []string{"key"}}})["id"].(string))
	if _, err := e.pool.Exec(ctx, `UPDATE bulk_edits SET created_at = now() - interval '8 days' WHERE id = $1`, old["id"]); err != nil {
		t.Fatal(err)
	}
	if code, out := owner.do("POST", base+"/bulk-edits/"+old["id"].(string)+"/undo", nil); code != 409 || out["error"].(map[string]any)["code"] != "undo_expired" {
		t.Fatalf("old undo: %d %v", code, out)
	}
}
