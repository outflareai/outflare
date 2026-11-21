package api_test

import (
	"testing"
)

// Flags gated on who the visitor is: a People list, a field of their
// record or your id for them. The visitor is the brand's person their
// anonymous id or user id belongs to; nobody else gets in.
func TestFlagPersonRules(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	g := "/v1/brands/" + geo

	person := func(brand, name, anon, user string, attrs map[string]any) string {
		t.Helper()
		return owner.must(201, "POST", "/v1/brands/"+brand+"/records", map[string]any{"type": "person", "name": name, "attributes": attrs,
			"identities": []any{map[string]any{"namespace": "anonymous_id", "value": anon}, map[string]any{"namespace": "user_id", "value": user}}})["id"].(string)
	}
	owner.must(201, "POST", g+"/catalog/types/person/fields", map[string]any{"label": "Plan", "value_type": "text"})
	ana := person(geo, "Ana Lima", "anon-ana-0001", "u-ana", map[string]any{"plan": "Pro"})
	person(geo, "Ben Ode", "anon-ben-0001", "u-ben", map[string]any{"plan": "free"})
	// The same ids in another brand are another person.
	other := person(rp, "Ana Lima", "anon-ana-0001", "u-ana", map[string]any{})

	pilots := owner.must(201, "POST", g+"/smart-lists", map[string]any{"name": "Pilot users", "kind": "static"})["id"].(string)
	owner.must(200, "POST", g+"/smart-lists/"+pilots+"/members", map[string]any{"record_ids": []string{ana}})
	theirs := owner.must(201, "POST", "/v1/brands/"+rp+"/smart-lists", map[string]any{"name": "Summitly pilots", "kind": "static"})["id"].(string)
	owner.must(200, "POST", "/v1/brands/"+rp+"/smart-lists/"+theirs+"/members", map[string]any{"record_ids": []string{other}})

	rule := func(attribute, op string, values ...string) []map[string]any {
		return []map[string]any{{"attribute": attribute, "op": op, "values": values}}
	}
	// A list rule takes this brand's People lists only.
	for _, bad := range [][]map[string]any{
		rule("list", "is", "pilots"),
		rule("list", "is", theirs),
		rule("list", "contains", pilots),
		rule("person.", "is", "pro"),
	} {
		owner.must(400, "POST", g+"/flags", map[string]any{"key": "bad", "rules": bad})
	}
	owner.must(201, "POST", g+"/flags", map[string]any{"key": "playground", "status": "on", "rules": rule("list", "is", pilots)})
	owner.must(201, "POST", g+"/flags", map[string]any{"key": "no-pilots", "status": "on", "rules": rule("list", "is_not", pilots)})
	owner.must(201, "POST", g+"/flags", map[string]any{"key": "pro-tools", "status": "on", "rules": rule("person.plan", "is", "pro")})
	owner.must(201, "POST", g+"/flags", map[string]any{"key": "staff", "status": "on", "rules": rule("user_id", "is", "u-ben", "u-zed")})

	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"northpeak.example"}})["key"].(string)
	site := func(anon string) map[string]any {
		t.Helper()
		code, out := askSiteFlags(t, e, "https://northpeak.example", k, anon, "https://northpeak.example/")
		if code != 200 {
			t.Fatalf("site flags: %d %v", code, out)
		}
		return out["flags"].(map[string]any)
	}
	server := func(body map[string]any) map[string]any {
		t.Helper()
		return owner.must(200, "POST", g+"/flags/evaluate", body)["flags"].(map[string]any)
	}
	want := func(who string, got map[string]any, playground, noPilots, pro, staff any) {
		t.Helper()
		if got["playground"] != playground || got["no-pilots"] != noPilots || got["pro-tools"] != pro || got["staff"] != staff {
			t.Fatalf("%s got %v", who, got)
		}
	}
	// The site knows the visitor by the browser's id alone.
	want("Ana on the site", site("anon-ana-0001"), "on", false, "on", false)
	want("Ben on the site", site("anon-ben-0001"), false, "on", false, "on")
	want("a stranger on the site", site("anon-new-0001"), false, "on", false, false)
	// A server knows them by either id.
	want("Ana by user id", server(map[string]any{"user_id": "u-ana"}), "on", false, "on", false)
	want("Ana by browser id", server(map[string]any{"anonymous_id": "anon-ana-0001"}), "on", false, "on", false)
	want("Ben by both", server(map[string]any{"anonymous_id": "anon-new-0002", "user_id": "u-ben"}), false, "on", false, "on")
	// A user id nobody has a record for still matches a rule that names it.
	want("Zed", server(map[string]any{"user_id": "u-zed"}), false, "on", false, "on")

	// Leaving the list closes the gate.
	owner.must(200, "POST", g+"/smart-lists/"+pilots+"/members/remove", map[string]any{"record_ids": []string{ana}})
	want("Ana after leaving", site("anon-ana-0001"), false, "on", "on", false)
}
