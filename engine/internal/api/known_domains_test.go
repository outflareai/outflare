package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"testing"
)

// The browser extension's list of known websites: a brand's company
// domains and its do-not-contact domains, as salted digests, never
// another brand's.
func TestKnownDomains(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	company := func(brand, name, domain string) {
		t.Helper()
		owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": name, "brand_id": brand,
			"identities": []map[string]any{{"namespace": "domain", "value": domain}}})
	}
	company("alpha", "Acme", "acme.example")
	company("bravo", "Northpeak", "northpeak.example")
	owner.must(200, "POST", "/v1/brands/"+alpha+"/suppressions", map[string]any{"values": []string{"blocked.example"}, "reason": "do_not_contact"})
	owner.must(200, "POST", "/v1/brands/"+bravo+"/suppressions", map[string]any{"values": []string{"other.example"}, "reason": "do_not_contact"})

	digest := func(salt, domain string) string {
		sum := sha256.Sum256([]byte(salt + ":" + domain))
		return hex.EncodeToString(sum[:8])
	}
	strs := func(v any) []string {
		var out []string
		for _, x := range v.([]any) {
			out = append(out, x.(string))
		}
		return out
	}
	got := owner.must(200, "GET", "/v1/brands/"+alpha+"/records/known-domains", nil)
	salt := got["salt"].(string)
	if c := strs(got["companies"]); !slices.Equal(c, []string{digest(salt, "acme.example")}) {
		t.Fatalf("alpha companies: %v", got)
	}
	if d := strs(got["do_not_contact"]); !slices.Equal(d, []string{digest(salt, "blocked.example")}) {
		t.Fatalf("alpha do not contact: %v", got)
	}
	if got["truncated"] != false {
		t.Fatalf("truncated: %v", got)
	}
	// A new salt on every answer, and Bravo sees only its own.
	other := owner.must(200, "GET", "/v1/brands/"+bravo+"/records/known-domains", nil)
	if other["salt"] == salt {
		t.Fatal("the salt was reused")
	}
	if c := strs(other["companies"]); !slices.Equal(c, []string{digest(other["salt"].(string), "northpeak.example")}) {
		t.Fatalf("bravo companies: %v", other)
	}
	if d := strs(other["do_not_contact"]); !slices.Equal(d, []string{digest(other["salt"].(string), "other.example")}) {
		t.Fatalf("bravo do not contact: %v", other)
	}
}
