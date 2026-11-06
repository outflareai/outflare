package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Brands own their data, step 2b (plans/brands/README.md, decisions 1 and
// 3): identities are matched within a brand, so the same email or user id
// in two brands is two people, and each brand has its own catalog, which
// starts from the default set.
func TestBrandIdentities(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)

	// Events: the same userId and proven email in two brands make two
	// people, one in each.
	identify := func(brand string) map[string]any {
		t.Helper()
		res := owner.must(200, "POST", base+"/customer-events", map[string]any{"brand_id": brand, "batch": []any{
			map[string]any{"type": "identify", "userId": "u_1", "traits": map[string]any{"email": "ada@example.com", "email_verified": true, "first_name": "Ada"}},
		}})
		r := res["results"].([]any)[0].(map[string]any)
		if r["status"] != "accepted" {
			t.Fatalf("identify in %s: %v", brand, res)
		}
		return r
	}
	inAlpha, inBravo := identify("alpha")["record_id"].(string), identify("bravo")["record_id"].(string)
	if inAlpha == inBravo {
		t.Fatalf("one person across brands: %s", inAlpha)
	}
	if b := owner.must(200, "GET", base+"/records/"+inBravo, nil)["brand_id"]; b != bravo {
		t.Fatalf("bravo's person is in %v", b)
	}
	// Sending again finds each brand's own person.
	if again := identify("alpha")["record_id"]; again != inAlpha {
		t.Fatalf("alpha again: %v, want %s", again, inAlpha)
	}

	// Lookup: a brand's route and ?brand= find only that brand's person.
	look := owner.must(200, "GET", "/v1/brands/"+bravo+"/records/lookup?namespace=email&value=ada@example.com", nil)
	if l := look["data"].([]any); len(l) != 1 || look["owner"] != inBravo {
		t.Fatalf("bravo lookup: %v", look)
	}
	look = owner.must(200, "GET", base+"/records/lookup?namespace=user_id&value=u_1&brand=alpha", nil)
	if l := look["data"].([]any); len(l) != 1 || look["owner"] != inAlpha {
		t.Fatalf("alpha lookup: %v", look)
	}
	if l := owner.must(200, "GET", base+"/records/lookup?namespace=user_id&value=u_1&brand=all", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("every brand: %d", len(l))
	}

	// Records made with verified identities (a CRM sync, the API): each
	// brand may hold the same verified email; within one brand it is taken.
	mk := func(want int, brand, email string) map[string]any {
		t.Helper()
		return owner.must(want, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben", "brand_id": brand,
			"identities": []map[string]any{{"namespace": "email", "value": email, "verified": true}}})
	}
	benA, benB := mk(201, "alpha", "ben@example.com")["id"].(string), mk(201, "bravo", "ben@example.com")["id"].(string)
	if e := mk(409, "alpha", "ben@example.com"); e["error"].(map[string]any)["code"] != "identity_taken" {
		t.Fatalf("taken in alpha: %v", e)
	}

	// Duplicates are found and merged within a brand only.
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ben", "brand_id": "bravo",
		"identities": []map[string]any{{"namespace": "email", "value": "ben@example.com"}}})
	pairs := owner.must(200, "GET", base+"/records/duplicates?status=all&brand=bravo", nil)["data"].([]any)
	if len(pairs) != 1 {
		t.Fatalf("pairs: %v", pairs)
	}
	for _, p := range pairs {
		for _, k := range []string{"record_a", "record_b"} {
			if id := fmt.Sprint(p.(map[string]any)[k]); id == benA {
				t.Fatalf("a pair reaches across brands: %v", p)
			}
		}
	}
	if e := owner.must(400, "POST", base+"/records/"+benA+"/merge", map[string]any{"from": benB}); e["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("merge across brands: %v", e)
	}

	// CSV import: a lead imported into Bravo's list is Bravo's person, not
	// Alpha's who has the same email.
	carl := mk(201, "alpha", "carl@example.com")["id"].(string)
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Bravo leads", "scopes": []string{"leads.manage", "customers.read"}, "brand": "bravo"})
	bravoApp := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	if code, imp := bravoApp.raw("POST", base+"/leads/import?list=Bravo+founders", "text/csv", "email,first_name\ncarl@example.com,Carl\n"); code != 200 || imp["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, imp)
	}
	var lead, leadBrand string
	if err := e.pool.QueryRow(ctx, `SELECT l.record_id::text, r.brand_id::text FROM leads l JOIN records r ON r.id = l.record_id
		WHERE l.workspace_id = $1 AND l.email = 'carl@example.com'`, ws).Scan(&lead, &leadBrand); err != nil {
		t.Fatal(err)
	}
	if lead == carl || leadBrand != bravo {
		t.Fatalf("the lead joined %s of %s, want a new person of bravo", lead, leadBrand)
	}
	// Each brand's person and identities carry its brand.
	var crossed []string
	if err := e.pool.QueryRow(ctx, `SELECT COALESCE(array_agg(i.namespace || ':' || i.value || ' ' || COALESCE(i.brand_id::text, '-') || ' ' || COALESCE(r.brand_id::text, '-')), '{}')
		FROM record_identities i JOIN records r ON r.id = i.record_id
		WHERE i.workspace_id = $1 AND i.brand_id IS DISTINCT FROM r.brand_id`, ws).Scan(&crossed); err != nil || len(crossed) != 0 {
		t.Fatalf("identities in another brand than their person: %v %v", crossed, err)
	}
	_ = alpha
}

func TestBrandCatalog(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var first string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&first); err != nil {
		t.Fatal(err)
	}
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	cat := func(brand string) string { return "/v1/brands/" + brand + "/catalog" }

	// A new brand starts with the default catalog: the built-in types and
	// option lists and the Deal type with its stages and fields.
	for _, b := range []string{first, alpha, bravo} {
		c := owner.must(200, "GET", cat(b), nil)
		var deal map[string]any
		for _, x := range c["types"].([]any) {
			if x.(map[string]any)["key"] == "deal" {
				deal = x.(map[string]any)
			}
		}
		if deal == nil || len(deal["fields"].([]any)) != 5 {
			t.Fatalf("brand %s has no default Deal type: %v", b, deal)
		}
		sets := map[string]bool{}
		for _, x := range c["option_sets"].([]any) {
			sets[x.(map[string]any)["key"].(string)] = true
		}
		if !sets["deal_stage"] || !sets["country"] || !sets["lifecycle_stage"] {
			t.Fatalf("brand %s option lists: %v", b, sets)
		}
	}

	hasField := func(brand, typ, key string) (bool, bool) {
		t.Helper()
		for _, f := range owner.must(200, "GET", cat(brand)+"/types/"+typ, nil)["fields"].([]any) {
			if f.(map[string]any)["key"] == key {
				return true, f.(map[string]any)["archived"] == true
			}
		}
		return false, false
	}
	// A field made in Alpha is Alpha's only, and Bravo may make its own of
	// the same key.
	owner.must(201, "POST", cat(alpha)+"/types/person/fields", map[string]any{"label": "Plan", "value_type": "text"})
	if in, _ := hasField(bravo, "person", "plan"); in {
		t.Fatal("alpha's field shows in bravo")
	}
	owner.must(201, "POST", cat(bravo)+"/types/person/fields", map[string]any{"label": "Plan", "value_type": "number"})
	owner.must(400, "POST", cat(bravo)+"/types/person/fields", map[string]any{"label": "Plan", "value_type": "number"})
	// Archiving (deleting) it in Alpha leaves Bravo's.
	owner.must(200, "PATCH", cat(alpha)+"/types/person/fields/plan", map[string]any{"archived": true})
	if _, archived := hasField(alpha, "person", "plan"); !archived {
		t.Fatal("alpha's field not archived")
	}
	if in, archived := hasField(bravo, "person", "plan"); !in || archived {
		t.Fatal("archiving alpha's field touched bravo's")
	}
	// Types and option lists too.
	owner.must(201, "POST", cat(alpha)+"/types", map[string]any{"name": "Subscription"})
	owner.must(201, "POST", cat(bravo)+"/types", map[string]any{"name": "Subscription", "description": "Bravo's"})
	owner.must(200, "PATCH", cat(alpha)+"/types/subscription", map[string]any{"archived": true})
	if bt := owner.must(200, "GET", cat(bravo)+"/types/subscription", nil); bt["archived"] != false || bt["description"] != "Bravo's" {
		t.Fatalf("bravo's type: %v", bt)
	}
	owner.must(201, "POST", cat(alpha)+"/option-sets", map[string]any{"name": "Tier", "options": []map[string]any{{"key": "gold", "label": "Gold"}}})
	owner.must(201, "POST", cat(bravo)+"/option-sets", map[string]any{"name": "Tier", "options": []map[string]any{{"key": "pro", "label": "Pro"}}})
	owner.must(200, "PATCH", cat(alpha)+"/option-sets/tier", map[string]any{"options": []map[string]any{{"key": "silver", "label": "Silver"}}})
	for _, s := range owner.must(200, "GET", cat(bravo)+"/option-sets", nil)["data"].([]any) {
		if s.(map[string]any)["key"] == "tier" && len(s.(map[string]any)["options"].([]any)) != 1 {
			t.Fatalf("alpha's change reached bravo's list: %v", s)
		}
	}
	// A record is checked against its brand's catalog: Bravo's plan is a
	// number, Alpha's archived.
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "brand_id": "bravo", "attributes": map[string]any{"plan": 3}})
	owner.must(400, "POST", base+"/records", map[string]any{"type": "person", "brand_id": "alpha", "attributes": map[string]any{"plan": "x"}})

	// The workspace routes still act on the first brand, and ?brand= reads
	// another's.
	owner.must(201, "POST", base+"/catalog/types/person/fields", map[string]any{"label": "Tier", "value_type": "text"})
	if in, _ := hasField(first, "person", "tier"); !in {
		t.Fatal("the workspace route didn't write the first brand's catalog")
	}
	if in, _ := hasField(alpha, "person", "tier"); in {
		t.Fatal("the workspace route wrote alpha's catalog")
	}
	fieldsOf := func(path string) []string {
		var keys []string
		for _, ty := range owner.must(200, "GET", path, nil)["types"].([]any) {
			if ty.(map[string]any)["key"] == "person" {
				for _, f := range ty.(map[string]any)["fields"].([]any) {
					keys = append(keys, f.(map[string]any)["key"].(string))
				}
			}
		}
		return keys
	}
	if keys := fieldsOf(base + "/catalog"); !slices.Contains(keys, "tier") || slices.Contains(keys, "plan") {
		t.Fatalf("workspace catalog: %v", keys)
	}
	if keys := fieldsOf(base + "/catalog?brand=bravo"); slices.Contains(keys, "tier") || !slices.Contains(keys, "plan") {
		t.Fatalf("workspace catalog of bravo: %v", keys)
	}

	// A token for one brand reads and changes only its own catalog.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Alpha app", "scopes": []string{"customers.manage", "catalog.manage"}, "brand": "alpha"})
	app := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	app.must(200, "GET", cat(alpha), nil)
	app.must(404, "GET", cat(bravo), nil)
	app.must(404, "POST", cat(bravo)+"/types", map[string]any{"name": "Order"})
	app.must(404, "GET", base+"/catalog?brand=bravo", nil)
	if keys := func() []string {
		var keys []string
		for _, ty := range app.must(200, "GET", base+"/catalog?archived=true", nil)["types"].([]any) {
			if ty.(map[string]any)["key"] == "subscription" {
				keys = append(keys, fmt.Sprint(ty.(map[string]any)["archived"]))
			}
		}
		return keys
	}(); len(keys) != 1 || keys[0] != "true" {
		t.Fatalf("a brand token's workspace catalog is its brand's: %v", keys)
	}

	// Catalog changes record their brand.
	created := named(got(), "field.created")
	if len(created) < 3 || created[0].Props["brand_id"] != alpha {
		t.Fatalf("field.created: %v", created)
	}
}

// The event inbox holds and approves names per brand, and approving a
// held name needs customers.manage only.
func TestBrandCatalogEvents(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	alpha := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)

	q := jobs.New(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.PollInterval = 20 * time.Millisecond
	e.srv.RegisterJobs(q)
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { q.Run(runCtx, 2); close(done) }() //nolint:errcheck
	t.Cleanup(func() { cancel(); <-done })

	send := func(brand string, ev map[string]any) string {
		t.Helper()
		return owner.must(202, "POST", "/v1/brands/"+brand+"/events", ev)["events"].([]any)[0].(map[string]any)["id"].(string)
	}
	settled := func(brand, id string) map[string]any {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			ev := owner.must(200, "GET", "/v1/brands/"+brand+"/events/"+id, nil)
			if ev["status"] != "pending" {
				return ev
			}
			if time.Now().After(deadline) {
				t.Fatalf("still pending: %v", ev)
			}
			time.Sleep(30 * time.Millisecond)
		}
	}
	a1 := send(alpha, map[string]any{"type": "track", "userId": "u_1", "event": "Order Completed"})
	b1 := send(bravo, map[string]any{"type": "track", "userId": "u_1", "event": "Trial Started"})
	if ev := settled(alpha, a1); ev["status"] != "held" {
		t.Fatalf("alpha: %v", ev)
	}
	if ev := settled(bravo, b1); ev["status"] != "held" {
		t.Fatalf("bravo: %v", ev)
	}
	// Each brand's catalog holds its own quarantined names.
	names := func(brand, status string) []string {
		var out []string
		path := "/v1/brands/" + brand + "/catalog/events"
		if status != "" {
			path += "?status=" + status
		}
		for _, d := range owner.must(200, "GET", path, nil)["data"].([]any) {
			out = append(out, d.(map[string]any)["name"].(string))
		}
		return out
	}
	if a, b := names(alpha, "quarantined"), names(bravo, "quarantined"); !slices.Equal(a, []string{"order_completed"}) || !slices.Equal(b, []string{"trial_started"}) {
		t.Fatalf("quarantined: alpha %v, bravo %v", a, b)
	}

	// Someone who fixes data (customers.manage) approves a held name on the
	// brand route; changing what the event means still needs catalog.manage.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Alpha data", "scopes": []string{"customers.manage", "customers.read"}, "brand": "alpha"})
	fixer := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	fixer.must(403, "PATCH", "/v1/brands/"+alpha+"/catalog/events/order_completed", map[string]any{"status": "approved"})
	fixer.must(404, "POST", "/v1/brands/"+bravo+"/catalog/events/trial_started/approve", nil)
	if d := fixer.must(200, "POST", "/v1/brands/"+alpha+"/catalog/events/order_completed/approve", nil); d["status"] != "approved" {
		t.Fatalf("approved: %v", d)
	}
	fixer.must(200, "POST", "/v1/brands/"+alpha+"/catalog/events/order_completed/approve", nil)
	fixer.must(400, "POST", "/v1/brands/"+alpha+"/catalog/events/Not%20A%20Name/approve", nil)
	if r := fixer.must(202, "POST", "/v1/brands/"+alpha+"/events/replay", map[string]any{"status": "held", "name": "order_completed"}); r["replayed"] != 1.0 {
		t.Fatalf("replay: %v", r)
	}
	if ev := settled(alpha, a1); ev["status"] != "accepted" {
		t.Fatalf("replayed: %v", ev)
	}
	// Bravo's name is still held: Alpha's approval is Alpha's.
	if b := names(bravo, "approved"); slices.Contains(b, "order_completed") || slices.Contains(b, "trial_started") {
		t.Fatalf("bravo approved: %v", b)
	}
	owner.must(200, "POST", "/v1/brands/"+bravo+"/catalog/events/trial_started/approve", nil)
	if a := names(alpha, ""); slices.Contains(a, "trial_started") {
		t.Fatalf("bravo's name in alpha's catalog: %v", a)
	}

	// Each brand has its own catalog: Bravo sending Alpha's name holds it
	// in Bravo's catalog, Bravo approves it on its own, and Alpha's entry
	// stays as it was.
	b2 := send(bravo, map[string]any{"type": "track", "userId": "u_1", "event": "Order Completed"})
	if ev := settled(bravo, b2); ev["status"] != "held" {
		t.Fatalf("bravo's order_completed: %v", ev)
	}
	if b := names(bravo, "quarantined"); !slices.Equal(b, []string{"order_completed"}) {
		t.Fatalf("bravo quarantined: %v", b)
	}
	if d := owner.must(200, "POST", "/v1/brands/"+bravo+"/catalog/events/order_completed/approve", nil); d["status"] != "approved" {
		t.Fatalf("bravo approves the same name: %v", d)
	}
	if r := owner.must(202, "POST", "/v1/brands/"+bravo+"/events/replay", map[string]any{"status": "held", "name": "order_completed"}); r["replayed"] != 1.0 {
		t.Fatalf("bravo replay: %v", r)
	}
	if ev := settled(bravo, b2); ev["status"] != "accepted" {
		t.Fatalf("bravo replayed: %v", ev)
	}
	owner.must(201, "POST", "/v1/brands/"+alpha+"/catalog/events", map[string]any{"name": "signup_done"})
	owner.must(201, "POST", "/v1/brands/"+bravo+"/catalog/events", map[string]any{"name": "signup_done"})
	if a, b := names(alpha, "approved"), names(bravo, "approved"); !slices.Contains(a, "order_completed") || !slices.Contains(b, "order_completed") ||
		!slices.Contains(a, "signup_done") || !slices.Contains(b, "signup_done") || slices.Contains(a, "trial_started") {
		t.Fatalf("approved: alpha %v, bravo %v", a, b)
	}

	approved := named(got(), "event_definition.approved")
	if len(approved) != 3 || approved[0].Props["brand_id"] != alpha || approved[1].Props["brand_id"] != bravo || approved[2].Props["brand_id"] != bravo {
		t.Fatalf("event_definition.approved: %v", approved)
	}
	for _, ev := range named(got(), "api.called") {
		if ev.ErrorCode == "event_name_in_other_brand" {
			t.Fatalf("a name another brand defines was refused: %v", ev)
		}
	}
}
