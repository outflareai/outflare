package api_test

import (
	"context"
	"testing"

	"github.com/outflareai/outflare/engine/internal/customers"
)

// Brands own their data (plans/brands/README.md): every record and what
// hangs off it belongs to one brand, and duplicates are only found and
// merged within a brand.
func TestBrandCustomerData(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var first string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&first); err != nil || first == "" {
		t.Fatalf("first brand: %q %v", first, err)
	}

	// A new workspace's Deal type is its first brand's.
	var types, fields, sets int
	if err := e.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM record_types WHERE workspace_id = $1 AND key = 'deal' AND brand_id = $2),
		(SELECT count(*) FROM field_definitions WHERE workspace_id = $1 AND record_type = 'deal' AND brand_id = $2),
		(SELECT count(*) FROM option_sets WHERE workspace_id = $1 AND key = 'deal_stage' AND brand_id = $2)`, ws, first).
		Scan(&types, &fields, &sets); err != nil {
		t.Fatal(err)
	}
	if types != 1 || fields != 5 || sets != 1 {
		t.Fatalf("deal type in the first brand: %d types, %d fields, %d option sets", types, fields, sets)
	}

	// A person made without a brand (an email seen in a reply) gets the
	// first brand, and so does a field a workspace adds.
	ana, err := customers.EnsurePerson(ctx, e.pool, ws, "email", "ana@acme.example", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if b := brandOf(t, e, `SELECT brand_id::text FROM records WHERE id = $1`, ana); b != first {
		t.Fatalf("EnsurePerson brand: %q, want %q", b, first)
	}
	if b := brandOf(t, e, `SELECT brand_id::text FROM record_identities WHERE record_id = $1`, ana); b != first {
		t.Fatalf("identity brand: %q", b)
	}
	owner.must(201, "POST", base+"/catalog/types/person/fields", map[string]any{"label": "Plan", "value_type": "text"})
	if b := brandOf(t, e, `SELECT brand_id::text FROM field_definitions WHERE workspace_id = $1 AND key = 'plan'`, ws); b != first {
		t.Fatalf("field brand: %q", b)
	}

	// A record of another brand: its identity, consent and history carry
	// that brand.
	north := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	ben := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben", "brand_id": "northpeak",
		"identities": []map[string]any{{"namespace": "email", "value": "ben@example.com"}}})["id"].(string)
	owner.must(201, "POST", base+"/records/"+ben+"/consents", map[string]any{"channel": "email", "status": "granted"})
	owner.must(200, "PATCH", base+"/records/"+ben, map[string]any{"name": "Ben B"})
	for _, q := range []string{
		`SELECT brand_id::text FROM record_identities WHERE record_id = $1`,
		`SELECT brand_id::text FROM consents WHERE record_id = $1`,
		`SELECT brand_id::text FROM attribute_changes WHERE record_id = $1 ORDER BY id DESC LIMIT 1`,
	} {
		if b := brandOf(t, e, q, ben); b != north {
			t.Fatalf("%s: %q, want %q", q, b, north)
		}
	}

	// The same email in the first brand is another person: never a
	// duplicate, never merged.
	other := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben",
		"identities": []map[string]any{{"namespace": "email", "value": "ben@example.com"}}})["id"].(string)
	if n := len(owner.must(200, "GET", base+"/records/duplicates?status=all", nil)["data"].([]any)); n != 0 {
		t.Fatalf("pairs across brands: %d", n)
	}
	refused := owner.must(400, "POST", base+"/records/"+other+"/merge", map[string]any{"from": ben})
	if refused["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("merge across brands: %v", refused)
	}

	// A record moved to another brand takes its identities and consent along.
	owner.must(200, "PATCH", base+"/records/"+ben, map[string]any{"brand_id": first})
	if b := brandOf(t, e, `SELECT brand_id::text FROM consents WHERE record_id = $1`, ben); b != first {
		t.Fatalf("consent after move: %q", b)
	}
}

func brandOf(t *testing.T, e *env, q, arg string) string {
	t.Helper()
	var b *string
	if err := e.pool.QueryRow(context.Background(), q, arg).Scan(&b); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	if b == nil {
		return ""
	}
	return *b
}
