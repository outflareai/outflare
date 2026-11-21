package api_test

import (
	"reflect"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestImportRecordsUserID(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	otherBase := brandBase(t, e, ws)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	base := "/v1/brands/" + brand
	person := func(base, email, userID string, verified bool) map[string]any {
		t.Helper()
		identities := []map[string]any{{"namespace": "email", "value": email}}
		if userID != "" {
			identities = append(identities, map[string]any{"namespace": "user_id", "value": userID, "verified": verified})
		}
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "origin": "import", "identities": identities})
	}

	// The same email and user id in another brand must neither claim this
	// import nor prevent the existing person in this brand acquiring the id.
	other := person(otherBase, "email@example.com", "u-email", true)
	byEmail := person(base, "email@example.com", "", false)
	conflict := person(base, "conflict@example.com", "u-existing", true)
	unverified := person(base, "unverified@example.com", "u-unverified", false)
	byUserID := person(base, "known@example.com", "u-known", true)
	list := owner.must(201, "POST", base+"/smart-lists", map[string]any{"name": "Imported people", "kind": "static"})["id"].(string)
	in := map[string]any{"list_id": list, "source": "customer", "people": []map[string]any{
		{"email": " Email@Example.com ", "user_id": " u-email "},
		{"email": "conflict@example.com", "user_id": "u-conflicting"},
		// User id still takes precedence over an email held by someone else.
		{"email": "conflict@example.com", "user_id": "u-known"},
		{"email": "new@example.com", "user_id": "u-new"},
		// Both rows matched the same email before the transaction began.
		{"email": "email@example.com", "user_id": "u-later-row"},
		{"email": "unverified@example.com", "user_id": "u-conflicting-unverified"},
	}}
	var firstIDs []any
	var newID string
	var imported map[string]any
	for pass := 0; pass < 2; pass++ {
		res := owner.must(200, "POST", base+"/records/import", in)
		wantCreated, wantMatched := 1.0, 2.0
		if pass > 0 {
			wantCreated, wantMatched = 0, 3
		}
		if res["created"] != wantCreated || res["matched"] != wantMatched || res["list_members"] != 3.0 {
			t.Fatalf("pass %d: import = %v", pass, res)
		}
		skipped := res["skipped"].([]any)
		if len(skipped) != 3 {
			t.Fatalf("pass %d: skipped = %v", pass, skipped)
		}
		for i, row := range []float64{2, 5, 6} {
			skip := skipped[i].(map[string]any)
			if skip["row"] != row || skip["reason"] != "this email belongs to a person with another user id" {
				t.Fatalf("pass %d: skipped row = %v", pass, skip)
			}
		}
		ids := res["record_ids"].([]any)
		if len(ids) != 3 || ids[0] != byEmail["id"] || ids[1] != byUserID["id"] {
			t.Fatalf("pass %d: record ids = %v", pass, ids)
		}
		if pass == 0 {
			firstIDs, newID = ids, ids[2].(string)
			imported = owner.must(200, "GET", base+"/records/"+byEmail["id"].(string), nil)
		} else {
			if !reflect.DeepEqual(ids, firstIDs) {
				t.Fatalf("reimport made different records: %v, first %v", ids, firstIDs)
			}
			again := owner.must(200, "GET", base+"/records/"+byEmail["id"].(string), nil)
			if !reflect.DeepEqual(again, imported) {
				t.Fatalf("reimport changed the matched person: %v, first %v", again, imported)
			}
		}
		for userID, id := range map[string]any{"u-email": byEmail["id"], "u-known": byUserID["id"], "u-new": newID} {
			lookup := owner.must(200, "GET", base+"/records/lookup?namespace=user_id&value="+userID, nil)
			if lookup["owner"] != id || len(lookup["data"].([]any)) != 1 {
				t.Fatalf("pass %d: user id %s lookup = %v", pass, userID, lookup)
			}
		}
		for _, userID := range []string{"u-conflicting", "u-later-row", "u-conflicting-unverified"} {
			lookup := owner.must(200, "GET", base+"/records/lookup?namespace=user_id&value="+userID, nil)
			if lookup["owner"] != nil || len(lookup["data"].([]any)) != 0 {
				t.Fatalf("pass %d: skipped user id %s was added: %v", pass, userID, lookup)
			}
		}
	}

	for _, id := range []string{byEmail["id"].(string), newID} {
		rec := owner.must(200, "GET", base+"/records/"+id, nil)
		identities := rec["identities"].([]any)
		if len(identities) != 2 {
			t.Fatalf("expected one email and one user id: %v", identities)
		}
		for _, item := range identities {
			identity := item.(map[string]any)
			if identity["namespace"] == "user_id" && (identity["source"] != "import" || identity["verified"] != true) {
				t.Fatalf("imported user id = %v", identity)
			}
			if identity["namespace"] == "email" && identity["verified"] != false {
				t.Fatalf("import verified an email: %v", identity)
			}
		}
	}
	for _, before := range []map[string]any{conflict, unverified} {
		after := owner.must(200, "GET", base+"/records/"+before["id"].(string), nil)
		if !reflect.DeepEqual(after, before) {
			t.Fatalf("skipped person changed: %v, before %v", after, before)
		}
	}
	known := owner.must(200, "GET", base+"/records/"+byUserID["id"].(string), nil)
	if !reflect.DeepEqual(known["identities"], byUserID["identities"]) {
		t.Fatalf("user id match changed identities: %v", known["identities"])
	}
	if after := owner.must(200, "GET", otherBase+"/records/"+other["id"].(string), nil); !reflect.DeepEqual(after, other) {
		t.Fatalf("another brand's person changed: %v", after)
	}
	if records := owner.must(200, "GET", base+"/records?type=person", nil)["data"].([]any); len(records) != 5 {
		t.Fatalf("unexpected people after reimport: %v", records)
	}
	members := owner.must(200, "GET", base+"/smart-lists/"+list+"/members", nil)["data"].([]any)
	if len(members) != 3 {
		t.Fatalf("list members = %v", members)
	}
	for _, item := range members {
		if email := item.(map[string]any)["email"]; email == "conflict@example.com" || email == "unverified@example.com" {
			t.Fatalf("skipped person joined the list: %v", item)
		}
	}
	var importedIdentity string
	for _, item := range imported["identities"].([]any) {
		identity := item.(map[string]any)
		if identity["namespace"] == "user_id" {
			importedIdentity = identity["id"].(string)
		}
	}
	added := 0
	for _, event := range named(got(), "identity.added") {
		if event.Props["target_id"] == importedIdentity {
			added++
		}
	}
	if added != 1 {
		t.Fatalf("expected one user id audit event on the matched person, got %d", added)
	}
	imports := named(got(), "records.imported")
	if len(imports) != 2 || imports[1].Props["target_id"] != brand {
		t.Fatalf("import events = %v", imports)
	}
}
