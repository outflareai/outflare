package api_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestLinkOrderSettings(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders/settings"
	otherRoot := "/v1/brands/" + otherBrand + "/link-orders/settings"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)

	got := owner.must(200, "GET", root, nil)
	if got["approver_user_id"] != nil || got["equivalent_dr_band"] != 10.0 || got["updated_at"] != nil || got["updated_by"] != nil {
		t.Fatalf("defaults: %v", got)
	}

	saved := owner.must(200, "PATCH", root, map[string]any{"approver_user_id": ownerID, "equivalent_dr_band": 10})
	if saved["approver_user_id"] != ownerID || saved["equivalent_dr_band"] != 10.0 || saved["updated_at"] == nil || saved["updated_by"] == nil {
		t.Fatalf("saved: %v", saved)
	}
	if len(named(events(), "link_orders.settings_updated")) == 0 {
		t.Fatal("missing settings_updated event")
	}

	// A second brand starts from the defaults; settings are per brand.
	other := owner.must(200, "GET", otherRoot, nil)
	if other["approver_user_id"] != nil {
		t.Fatalf("other brand inherited approver: %v", other)
	}

	admin, adminID := joinAs(t, owner, ws, "ada@acme.example", map[string]any{"role": "admin"})
	op, opID := joinAs(t, owner, ws, "op@acme.example", map[string]any{"role": "operator"})
	op.must(200, "GET", root, nil)
	op.must(403, "PATCH", root, map[string]any{"equivalent_dr_band": 20})
	owner.must(400, "PATCH", root, map[string]any{"approver_user_id": opID})
	owner.must(400, "PATCH", root, map[string]any{"equivalent_dr_band": 0})
	owner.must(400, "PATCH", root, map[string]any{"equivalent_dr_band": 51})
	owner.must(400, "PATCH", root, map[string]any{"approver_user_id": "not-a-uuid"})
	owner.must(400, "PATCH", root, map[string]any{"approver_user_id": "00000000-0000-0000-0000-000000000000"})

	// Another admin may change settings (the designated approver is for
	// Approve and Paid, which this pull request does not add).
	admin.must(200, "PATCH", root, map[string]any{"approver_user_id": adminID, "equivalent_dr_band": 15})
	cleared := owner.must(200, "PATCH", root, map[string]any{"approver_user_id": nil})
	if cleared["approver_user_id"] != nil || cleared["equivalent_dr_band"] != 15.0 {
		t.Fatalf("clear: %v", cleared)
	}

	token := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Coverage token", "scopes": []string{"coverage.read", "coverage.manage"}})
	reader := &client{t: t, h: e.h, bearer: token["token"].(string)}
	reader.must(200, "GET", root, nil)
	reader.must(403, "PATCH", root, map[string]any{"equivalent_dr_band": 10})

	owner.must(404, "GET", "/v1/brands/00000000-0000-0000-0000-000000000000/link-orders/settings", nil)
}

func TestLinkOrderSchemaConstraints(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Example publisher", "brand_id": brand})
	pub := owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/publishers", map[string]any{"record_id": rec["id"], "domain": "publisher.example"})
	ctx := context.Background()

	var profile string
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_donor_profiles (workspace_id, brand_id, publisher_id, niches, source)
		VALUES ($1, $2, $3, ARRAY['saas'], 'csv') RETURNING id`, ws, brand, pub["id"]).Scan(&profile); err != nil {
		t.Fatal(err)
	}
	_, err := e.pool.Exec(ctx, `INSERT INTO coverage_donor_profiles (workspace_id, brand_id, publisher_id)
		VALUES ($1, $2, $3)`, ws, brand, pub["id"])
	if !pgCode(err, "23505") {
		t.Fatalf("duplicate profile: %v", err)
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO coverage_donor_profiles (workspace_id, brand_id, publisher_id)
		VALUES ($1, $2, $3)`, ws, brand, "00000000-0000-0000-0000-000000000000")
	if !pgCode(err, "23503") {
		t.Fatalf("missing publisher: %v", err)
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO coverage_link_order_settings (workspace_id, brand_id, equivalent_dr_band)
		VALUES ($1, $2, 0)`, ws, brand)
	if !pgCode(err, "23514") {
		t.Fatalf("band check: %v", err)
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO coverage_link_orders (workspace_id, brand_id, publisher_id, donor_profile_id, target_url, status, currency)
		VALUES ($1, $2, $3, $4, 'https://northpeak.example/', 'draft', 'USD')`, ws, brand, pub["id"], profile)
	if !pgCode(err, "23514") {
		t.Fatalf("status check: %v", err)
	}
	_, err = e.pool.Exec(ctx, `INSERT INTO coverage_link_orders (workspace_id, brand_id, publisher_id, donor_profile_id, target_url, currency)
		VALUES ($1, $2, $3, $4, 'https://northpeak.example/', 'GBP')`, ws, brand, pub["id"], profile)
	if !pgCode(err, "23514") {
		t.Fatalf("currency check: %v", err)
	}
	var orderID, eventID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_link_orders (workspace_id, brand_id, publisher_id, donor_profile_id, target_url, status, currency, donor_amount_minor)
		VALUES ($1, $2, $3, $4, 'https://northpeak.example/', 'requested', 'EUR', 15000) RETURNING id`,
		ws, brand, pub["id"], profile).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_link_orders (workspace_id, brand_id, publisher_id, donor_profile_id, target_url, replaces_order_id)
		VALUES ($1, $2, $3, $4, 'https://northpeak.example/blog', $5) RETURNING id`,
		ws, brand, pub["id"], profile, orderID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO coverage_link_order_events (workspace_id, brand_id, order_id, to_status, payload)
		VALUES ($1, $2, $3, 'requested', '{}') RETURNING id`, ws, brand, orderID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
}

func pgCode(err error, code string) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && pe.Code == code
}

func TestLinkOrderCatalogAndCSV(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	otherRoot := "/v1/brands/" + other + "/link-orders"

	created := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "Donor.Example", "pbn": false, "niches": []string{"saas"},
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 15000}},
	})
	if created["domain"] != "donor.example" || created["source"] != "manual" {
		t.Fatalf("create: %v", created)
	}
	prices := created["prices"].([]any)
	if len(prices) != 1 || prices[0].(map[string]any)["amount_minor"] != 15000.0 {
		t.Fatalf("prices: %v", created["prices"])
	}
	id := created["id"].(string)

	// Same domain upserts rather than adding a second publisher.
	again := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "https://www.donor.example/page",
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 16000}},
	})
	if again["id"] != id || again["publisher_id"] != created["publisher_id"] {
		t.Fatalf("upsert should keep the same row: %v", again)
	}
	if again["prices"].([]any)[0].(map[string]any)["amount_minor"] != 16000.0 {
		t.Fatalf("price not updated: %v", again["prices"])
	}

	owner.must(200, "PATCH", root+"/catalog/items/"+id, map[string]any{"pbn": true, "notes": "manual flag"})
	got := owner.must(200, "GET", root+"/catalog/items/"+id, nil)
	if got["pbn"] != true || got["notes"] != "manual flag" {
		t.Fatalf("patch: %v", got)
	}

	csv := "domain,currency,amount_minor,pbn,niches\n" +
		"ok.example,USD,15000,false,saas\n" +
		",USD,1\n" +
		"not a domain,USD,1\n" +
		"ok.example,USD,18000,false,saas\n"
	code, out := owner.raw("POST", root+"/catalog/import", "text/csv", csv)
	if code != 200 {
		t.Fatalf("import status %d: %v", code, out)
	}
	if out["created"] != 1.0 || out["updated"] != 1.0 {
		t.Fatalf("import counts: %v", out)
	}
	errs := out["errors"].([]any)
	if len(errs) != 2 {
		t.Fatalf("want 2 row errors, got %v", out["errors"])
	}

	list := owner.must(200, "GET", root+"/catalog", nil)
	if list["next_cursor"] != nil {
		t.Fatalf("unexpected next_cursor: %v", list["next_cursor"])
	}
	if len(list["data"].([]any)) != 2 {
		t.Fatalf("list: %v", list)
	}

	// A catalog id from another brand is 404, not visible.
	otherItem := owner.must(201, "POST", otherRoot+"/catalog", map[string]any{"domain": "other-brand.example"})
	owner.must(404, "GET", root+"/catalog/items/"+otherItem["id"].(string), nil)
	owner.must(404, "GET", "/v1/brands/00000000-0000-0000-0000-000000000000/link-orders/catalog", nil)
}

func TestLinkOrderStatusMachineAndPayGates(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	otherBrand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	admin, _ := joinAs(t, owner, ws, "ada@acme.example", map[string]any{"role": "admin"})
	op, _ := joinAs(t, owner, ws, "op@acme.example", map[string]any{"role": "operator"})
	owner.must(200, "PATCH", root+"/settings", map[string]any{"approver_user_id": ownerID})

	donor := owner.must(201, "POST", root+"/catalog", map[string]any{
		"domain": "donor.example",
		"prices": []map[string]any{{"currency": "USD", "amount_minor": 15000}},
	})
	alt := owner.must(201, "POST", root+"/catalog", map[string]any{"domain": "alt.example"})
	order := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/page",
	})
	id := order["id"].(string)
	path := root + "/" + id
	if order["status"] != "requested" || order["guarantee_days"] != 30.0 {
		t.Fatalf("created: %v", order)
	}

	// Forbidden transitions from requested.
	owner.must(409, "POST", path+"/approve", nil)
	owner.must(409, "POST", path+"/publish", map[string]any{"published_url": "https://donor.example/post"})
	owner.must(409, "POST", path+"/pay", nil)
	owner.must(409, "POST", path+"/verify", nil)
	owner.must(409, "POST", path+"/lost", nil)
	owner.must(409, "POST", path+"/negotiate", nil)

	quoted := owner.must(200, "POST", path+"/quote", map[string]any{"amount_minor": 15000, "currency": "USD"})
	if quoted["status"] != "quoted" || quoted["quoted_amount_minor"] != 15000.0 {
		t.Fatalf("quoted: %v", quoted)
	}
	owner.must(200, "POST", path+"/negotiate", map[string]any{"notes": "asking"})
	owner.must(200, "POST", path+"/quote", map[string]any{"amount_minor": 12000, "currency": "USD"})

	// Approve over a tight USD budget fails; a EUR budget is unused.
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "Link orders USD", "currency": "USD", "amount_minor": 10000,
		"starts_on": "2000-01-01", "ends_on": "2099-12-31", "tags": "link_orders",
	})
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "Link orders EUR", "currency": "EUR", "amount_minor": 900000,
		"starts_on": "2000-01-01", "ends_on": "2099-12-31", "tags": "link_orders",
	})
	owner.must(409, "POST", path+"/approve", nil)

	// Raise the USD cap and approve. The frozen amount is the last quote.
	owner.must(201, "POST", "/v1/brands/"+brand+"/coverage/budgets", map[string]any{
		"title": "Link orders USD raised", "currency": "USD", "amount_minor": 50000,
		"starts_on": "2000-01-02", "ends_on": "2099-12-31", "tags": "link_orders",
	})
	admin.must(403, "POST", path+"/approve", nil)
	op.must(403, "POST", path+"/approve", nil)
	approved := owner.must(200, "POST", path+"/approve", nil)
	if approved["status"] != "approved" || approved["donor_amount_minor"] != 12000.0 || approved["currency"] != "USD" {
		t.Fatalf("approved: %v", approved)
	}
	// The frozen amount cannot be quoted again.
	owner.must(409, "POST", path+"/quote", map[string]any{"amount_minor": 1, "currency": "USD"})

	pub := owner.must(200, "POST", path+"/publish", map[string]any{
		"published_url": "https://donor.example/post", "proof_url": "https://donor.example/proof.png",
	})
	if pub["status"] != "published" || pub["placement_id"] == nil || pub["published_url"] == "" {
		t.Fatalf("published: %v", pub)
	}
	placement := pub["placement_id"].(string)

	// Paid is blocked until a passing live-link check is on the order.
	owner.must(409, "POST", path+"/pay", nil)
	owner.must(202, "POST", path+"/check", nil)

	// A failing check is not enough.
	if _, err := e.pool.Exec(context.Background(), `UPDATE coverage_link_orders SET last_check_at = now(), last_check_live = false, last_check_target_match = true WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	owner.must(409, "POST", path+"/pay", nil)

	if _, err := e.pool.Exec(context.Background(), `UPDATE coverage_link_orders SET last_check_at = now(), last_check_live = true, last_check_target_match = true WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	// Operator, another admin, and an API token cannot mark it paid.
	op.must(403, "POST", path+"/pay", nil)
	admin.must(403, "POST", path+"/pay", nil)
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{
		"name": "Pay bot", "scopes": []string{"coverage.read", "coverage.manage", "coverage.finance.read", "coverage.finance.manage"},
	})
	bot := &client{t: t, h: e.h, bearer: tok["token"].(string)}
	bot.must(403, "POST", path+"/pay", nil)
	agent := owner.must(201, "POST", base+"/api-tokens", map[string]any{
		"name": "Agent", "scopes": []string{"coverage.read", "coverage.manage", "coverage.finance.manage", "actions.read"}, "action_gate": true,
	})
	(&client{t: t, h: e.h, bearer: agent["token"].(string)}).must(403, "POST", path+"/pay", nil)

	paid := owner.must(200, "POST", path+"/pay", nil)
	if paid["status"] != "paid" || paid["payment_id"] == nil || paid["invoice_id"] == nil {
		t.Fatalf("paid: %v", paid)
	}
	var amount int64
	var kind string
	if err := e.pool.QueryRow(context.Background(), `SELECT amount_minor, kind FROM coverage_payments WHERE id = $1`, paid["payment_id"]).Scan(&amount, &kind); err != nil {
		t.Fatal(err)
	}
	if amount != 12000 || kind != "payment" {
		t.Fatalf("payment row: amount=%d kind=%s", amount, kind)
	}
	// A second pay is refused; the payment row stays as it was.
	owner.must(409, "POST", path+"/pay", nil)

	verified := owner.must(200, "POST", path+"/verify", nil)
	if verified["status"] != "verified" {
		t.Fatalf("verified: %v", verified)
	}

	// Republishing the same page from a second order reuses the placement.
	order2 := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor["id"], "target_url": "https://northpeak.example/page",
	})
	path2 := root + "/" + order2["id"].(string)
	// A EUR quote takes the stored ECB reference rate for the invoice date
	// (today); without one it would not be saved. The table is shared by
	// every test, so this writes the same 1.1 the drilldown and tracker
	// tests use (and leave in place).
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO exchange_rates (currency, day, usd_per_unit) VALUES ('EUR', current_date, 1.1)
		ON CONFLICT (currency, day) DO UPDATE SET usd_per_unit = EXCLUDED.usd_per_unit`); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", path2+"/quote", map[string]any{"amount_minor": 5000, "currency": "EUR"})
	owner.must(200, "POST", path2+"/approve", nil)
	pub2 := owner.must(200, "POST", path2+"/publish", map[string]any{"published_url": "https://donor.example/post"})
	if pub2["placement_id"] != placement {
		t.Fatalf("second publish created a new placement: %v vs %s", pub2["placement_id"], placement)
	}

	// Summary keeps USD and EUR apart.
	sum := owner.must(200, "GET", root+"/summary", nil)
	spent := moneyByCurrency(sum["spent"].([]any))
	committed := moneyByCurrency(sum["committed"].([]any))
	if spent["USD"] != 12000 || committed["EUR"] != 5000 {
		t.Fatalf("summary mixed or wrong: %v", sum)
	}
	if _, ok := spent["EUR"]; ok {
		t.Fatalf("EUR should not be in spent: %v", sum)
	}
	budget := owner.must(200, "GET", root+"/budget", nil)
	if len(budget["budgets"].([]any)) < 2 {
		t.Fatalf("budget: %v", budget)
	}
	op.must(403, "GET", root+"/budget", nil)

	// Brand isolation.
	owner.must(404, "GET", "/v1/brands/"+otherBrand+"/link-orders/"+id, nil)

	hist := owner.must(200, "GET", path+"/history", nil)
	if len(hist["data"].([]any)) < 5 {
		t.Fatalf("history: %v", hist)
	}

	lost := owner.must(200, "POST", path+"/lost", map[string]any{"notes": "page gone"})
	if lost["status"] != "lost" {
		t.Fatalf("lost: %v", lost)
	}
	repl := owner.must(200, "POST", path+"/replace", map[string]any{"donor_profile_id": alt["id"]})
	if repl["status"] != "requested" || repl["replaces_order_id"] != id {
		t.Fatalf("replace: %v", repl)
	}
	// One open replacement at a time; a cycle is rejected by refusing a second open one.
	owner.must(409, "POST", path+"/replace", map[string]any{"donor_profile_id": alt["id"]})
	owner.must(409, "POST", path+"/credit", nil) // open replacement still exists

	// After the replacement is itself lost, a credit is allowed (no open replacement).
	replPath := root + "/" + repl["id"].(string)
	owner.must(200, "POST", replPath+"/quote", map[string]any{"amount_minor": 1000, "currency": "USD"})
	owner.must(200, "POST", replPath+"/approve", nil)
	owner.must(200, "POST", replPath+"/publish", map[string]any{"published_url": "https://alt.example/post"})
	if _, err := e.pool.Exec(context.Background(), `UPDATE coverage_link_orders SET last_check_at = now(), last_check_live = true, last_check_target_match = true WHERE id = $1`, repl["id"]); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", replPath+"/lost", nil)
	credited := owner.must(200, "POST", path+"/credit", map[string]any{"reason": "no equivalent"})
	if credited["credit_payment_id"] == nil {
		t.Fatalf("credit: %v", credited)
	}
}

func moneyByCurrency(rows []any) map[string]float64 {
	out := map[string]float64{}
	for _, r := range rows {
		m := r.(map[string]any)
		out[m["currency"].(string)] = m["amount_minor"].(float64)
	}
	return out
}
