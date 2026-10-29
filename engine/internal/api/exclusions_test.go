package api_test

import (
	"context"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Exclusions keep people on a list, with a tag, or at a tagged company out
// of the journeys, broadcasts and sequences that name them.
func TestExclusions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	person := func(addr string) string {
		t.Helper()
		rec, err := customers.EnsurePerson(ctx, e.pool, ws, "email", addr, true, "")
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	// Ana is on the hands-off list, Bo is tagged competitor, Cy works at a
	// competitor, and Dee is fine.
	ana, bo, cy, dee := person("ana@acme.example"), person("bo@acme.example"), person("cy@northpeak.example"), person("dee@acme.example")
	rival := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak"})["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+rival, map[string]any{"attributes": map[string]any{"tags": []string{"competitor"}}})
	owner.must(200, "PATCH", base+"/records/"+cy, map[string]any{"attributes": map[string]any{"companies": []string{rival}}})
	owner.must(200, "PATCH", base+"/records/"+bo, map[string]any{"attributes": map[string]any{"tags": []string{"competitor"}}})
	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Hands off", "kind": "static"})["id"].(string)
	owner.must(200, "POST", base+"/smart-lists/"+list+"/members", map[string]any{"record_ids": []string{ana}})
	exclude := map[string]any{"lists": []string{list}, "tags": []string{"competitor"}}

	// Bad exclusions are refused.
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "exclude": map[string]any{"lists": []string{"00000000-0000-0000-0000-000000000000"}, "tags": []string{}}})
	owner.must(400, "POST", base+"/journeys", map[string]any{"name": "Bad", "exclude": map[string]any{"lists": []string{}, "tags": []string{"Not A Tag!"}}})

	// A journey refuses them entry, even by hand, and says why.
	j := owner.must(201, "POST", base+"/journeys", map[string]any{"name": "Welcome", "exclude": exclude,
		"steps": []map[string]any{{"id": "wait", "kind": "wait", "wait_seconds": 3600}}})
	jid := j["id"].(string)
	if x := j["exclude"].(map[string]any); len(x["lists"].([]any)) != 1 || x["tags"].([]any)[0] != "competitor" {
		t.Fatalf("exclude = %v", j["exclude"])
	}
	owner.must(200, "POST", base+"/journeys/"+jid+"/activate", nil)
	for rec, want := range map[string]string{ana: "on the list Hands off", bo: "tagged competitor", cy: "their company is tagged competitor"} {
		code, body := owner.do("POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": rec})
		if code < 400 || !strings.Contains(errText(body), want) {
			t.Fatalf("enter %s = %d %v, want %q", rec, code, body, want)
		}
	}
	owner.must(201, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": dee})
	// A draft keeps the exclusion, and changing it is a new version.
	d := owner.must(201, "POST", base+"/journeys/"+jid+"/draft", nil)
	if len(d["exclude"].(map[string]any)["tags"].([]any)) != 1 {
		t.Fatalf("draft = %v", d)
	}
	owner.must(200, "PATCH", base+"/journeys/"+jid+"/draft", map[string]any{"exclude": map[string]any{"lists": []string{}, "tags": []string{}}})
	owner.must(200, "POST", base+"/journeys/"+jid+"/publish", map[string]any{"runs": "keep"})
	owner.must(201, "POST", base+"/journeys/"+jid+"/runs", map[string]any{"record_id": bo})

	// A broadcast says how many its exclusion leaves out.
	owner.must(201, "POST", base+"/email-templates", map[string]any{"name": "News", "purpose": "lifecycle", "subject": "News", "html": "<p>Hi</p>",
		"from": "news@example.com", "publish": true})
	b := owner.must(201, "POST", base+"/email-broadcasts", map[string]any{"name": "October", "template": "news", "exclude": exclude})
	if b["audience_size"] != 4.0 || b["audience_excluded"] != 3.0 {
		t.Fatalf("broadcast = %v", b)
	}
	b = owner.must(200, "PATCH", base+"/email-broadcasts/"+b["id"].(string), map[string]any{"exclude": map[string]any{"lists": []string{list}, "tags": []string{}}})
	if b["audience_excluded"] != 1.0 {
		t.Fatalf("broadcast = %v", b)
	}

	// A sequence doesn't enroll them.
	_, imp := owner.raw("POST", base+"/leads/import?list=Outbound", "text/csv",
		"email\nana@acme.example\nbo@acme.example\ncy@northpeak.example\ndee@acme.example\n")
	if _, err := e.pool.Exec(ctx, `UPDATE leads l SET record_id = i.record_id FROM record_identities i
		WHERE l.workspace_id = $1 AND i.workspace_id = $1 AND i.namespace = 'email' AND i.value = lower(l.email)`, ws); err != nil {
		t.Fatal(err)
	}
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Outbound", "exclude": exclude,
		"steps": []map[string]any{{"subject": "Hello", "body": "Hi"}}})
	if len(seq["exclude"].(map[string]any)["lists"].([]any)) != 1 {
		t.Fatalf("sequence = %v", seq)
	}
	res := owner.must(200, "POST", base+"/sequences/"+seq["id"].(string)+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	if res["enrolled"] != 1.0 || res["excluded"] != 3.0 {
		t.Fatalf("enroll = %v", res)
	}
	owner.must(400, "PATCH", base+"/sequences/"+seq["id"].(string), map[string]any{"exclude": map[string]any{"lists": []string{}, "tags": []string{"BAD TAG"}}})
}
