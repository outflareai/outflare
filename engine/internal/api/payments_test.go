package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// fakeStripe is a small Stripe account for the key "rk_test_key": three
// customers listed in two pages, one more only readable on its own, three
// subscriptions, three payments (and one that failed), one refund, and
// after the first sync two changes: Ann moves to the Team plan and part of
// her first payment is refunded.
func fakeStripe(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	price := func(id, product string, amount int64, interval string) map[string]any {
		return map[string]any{"id": id, "product": product, "currency": "usd", "unit_amount": amount,
			"recurring": map[string]any{"interval": interval, "interval_count": 1}}
	}
	sub := func(id, customer, status string, p map[string]any) map[string]any {
		return map[string]any{"id": id, "customer": customer, "status": status, "created": 1767225600,
			"items": map[string]any{"data": []any{map[string]any{"price": p, "quantity": 1}}}}
	}
	cust := func(id, email, name, userID string) map[string]any {
		md := map[string]string{}
		if userID != "" {
			md["user_id"] = userID
		}
		return map[string]any{"id": id, "email": email, "name": name, "metadata": md, "created": 1767225600}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer rk_test_key" {
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Stripe-Version") == "" {
			w.WriteHeader(400)
			return
		}
		q := r.URL.Query()
		list := func(data []any, more bool) any { return map[string]any{"data": data, "has_more": more} }
		var out any
		switch r.URL.Path {
		case "/v1/account":
			w.WriteHeader(403) // a restricted key can't read it
			return
		case "/v1/customers":
			switch {
			case q.Get("limit") == "1":
				out = list([]any{map[string]any{"livemode": false}}, true)
			case q.Get("starting_after") == "":
				out = list([]any{cust("cus_ann", "ann@acme.example", "Ann Lee", "u_ann"), cust("cus_bo", "Bo@Northpeak.example", "Bo", "u_bo")}, true)
			default:
				out = list([]any{cust("cus_cy", "cy@acme.example", "Cy", "")}, false)
			}
		case "/v1/customers/cus_zed":
			out = cust("cus_zed", "", "Zed", "")
		case "/v1/products/prod_pro":
			out = map[string]any{"name": "Pro"}
		case "/v1/products/prod_team":
			out = map[string]any{"name": "Team"}
		case "/v1/subscriptions":
			if q.Get("status") != "all" {
				w.WriteHeader(400)
				return
			}
			out = list([]any{
				sub("sub_ann", "cus_ann", "active", price("price_pro", "prod_pro", 4900, "month")),
				sub("sub_bo", "cus_bo", "trialing", price("price_pro_y", "prod_pro", 120000, "year")),
				sub("sub_cy", "cus_cy", "active", price("price_pro", "prod_pro", 4900, "month")),
			}, false)
		case "/v1/charges":
			ch := func(id, customer, status string, amount, created int64) map[string]any {
				return map[string]any{"id": id, "customer": customer, "amount": amount, "currency": "usd", "created": created,
					"status": status, "paid": status == "succeeded", "invoice": "in_" + id}
			}
			out = list([]any{
				ch("ch_1", "cus_ann", "succeeded", 4900, 1767225600),
				ch("ch_2", "cus_ann", "succeeded", 4900, 1769904000),
				ch("ch_3", "cus_cy", "failed", 4900, 1769904000),
				ch("ch_4", "cus_zed", "succeeded", 2500, 1769904000),
			}, false)
		case "/v1/refunds":
			if c := q.Get("charge"); c != "" {
				out = list([]any{map[string]any{"id": "re_2", "charge": c, "amount": 1000, "currency": "usd", "created": 1772323200, "status": "succeeded"}}, false)
				break
			}
			out = list([]any{map[string]any{"id": "re_1", "charge": map[string]any{"id": "ch_2", "customer": "cus_ann"},
				"amount": 4900, "currency": "usd", "created": 1770000000, "status": "succeeded"}}, false)
		case "/v1/events":
			switch {
			case q.Get("limit") == "1":
				out = list([]any{map[string]any{"id": "evt_1"}}, true)
			case q.Get("ending_before") == "evt_1":
				// Newest first, as Stripe lists them.
				out = list([]any{
					map[string]any{"id": "evt_3", "type": "charge.refunded", "created": 1772323200,
						"data": map[string]any{"object": map[string]any{"id": "ch_1", "customer": "cus_ann", "status": "succeeded", "paid": true, "amount": 4900, "currency": "usd"}}},
					map[string]any{"id": "evt_2", "type": "customer.subscription.updated", "created": 1772323100,
						"data": map[string]any{"object": sub("sub_ann", "cus_ann", "active", price("price_team", "prod_team", 9900, "month"))}},
				}, false)
			case q.Get("ending_before") == "evt_3":
				out = list([]any{}, false)
			default:
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":{"code":"resource_missing"}}`))
				return
			}
		default:
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"resource_missing"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStripePaymentsSync(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.integrations.Client.BaseURLs["stripe"] = fakeStripe(t).URL
	ctx := context.Background()

	// Ann is already a user of the product (her user id); Bo has proved his
	// email. A billing email must not claim Bo's record.
	ann := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ann",
		"identities": []any{map[string]any{"namespace": "user_id", "value": "u_ann"}}})
	bo := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo",
		"identities": []any{map[string]any{"namespace": "email", "value": "bo@northpeak.example", "verified": true}}})

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "stripe", "credentials": map[string]string{"api_key": "rk_test_key"}})
	id := i["id"].(string)
	if acct := i["account"].(map[string]any); acct["name"] != "Stripe (test mode)" {
		t.Fatalf("account: %v", acct)
	}
	path := base + "/integrations/" + id + "/payment-sync"
	if v := owner.must(200, "GET", path, nil); v["phase"] != "backfill" || v["last_run_at"] != nil {
		t.Fatalf("before: %v", v)
	}
	if v := owner.must(202, "POST", path, nil); v["queued"] != true {
		t.Fatalf("queue: %v", v)
	}

	if err := e.srv.SyncPayments(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	v := owner.must(200, "GET", path, nil)
	totals := v["totals"].(map[string]any)
	if v["phase"] != "changes" || v["last_error"] != nil || v["backfilled_at"] == nil ||
		totals["customers"] != 4.0 || totals["subscriptions"] != 4.0 || totals["payments"] != 3.0 || totals["refunds"] != 2.0 || totals["records_created"] != 3.0 {
		t.Fatalf("after the first sync: %v", v)
	}

	// The first sync lists everything, then reads what changed since it
	// started: Ann moved to the Team plan and part of her first payment was
	// refunded. Her Stripe customer joined her record through her user id.
	a := owner.must(200, "GET", base+"/records/"+ann["id"].(string), nil)
	attrs := a["attributes"].(map[string]any)
	money := func(v any) (float64, string) {
		m, _ := v.(map[string]any)
		amount, _ := m["amount_minor"].(float64)
		cur, _ := m["currency"].(string)
		return amount, cur
	}
	if n, c := money(attrs["mrr"]); n != 9900 || c != "USD" {
		t.Fatalf("mrr: %v", attrs)
	}
	if n, _ := money(attrs["lifetime_revenue"]); n != 3900 { // 2 x 49, less refunds of 49 and 10
		t.Fatalf("lifetime revenue: %v", attrs)
	}
	if attrs["billing_plan"] != "Team" || attrs["billing_status"] != "active" || attrs["customer_since"] != "2026-01-01T00:00:00Z" {
		t.Fatalf("billing fields: %v", attrs)
	}
	if a["attribute_sources"].(map[string]any)["mrr"] != "first_party" || a["name"] != "Ann" {
		t.Fatalf("sources and name: %v", a)
	}
	ids := map[string]bool{}
	for _, x := range a["identities"].([]any) {
		x := x.(map[string]any)
		ids[x["namespace"].(string)+":"+x["value"].(string)+":"+map[bool]string{true: "v", false: "u"}[x["verified"].(bool)]] = true
	}
	if !ids["stripe.customer:cus_ann:v"] || !ids["email:ann@acme.example:u"] {
		t.Fatalf("Ann's identities: %v", ids)
	}

	// Bo's billing email is a different person until someone proves it: his
	// own record is untouched, and the Stripe customer is a new person.
	b := owner.must(200, "GET", base+"/records/"+bo["id"].(string), nil)
	if len(b["identities"].([]any)) != 1 || b["attributes"].(map[string]any)["billing_status"] != nil {
		t.Fatalf("Bo was claimed: %v", b)
	}
	var boStripe, boStatus, boPlan string
	var boMRR float64
	if err := e.pool.QueryRow(ctx, `SELECT r.id::text, r.attributes->>'billing_status', r.attributes->>'billing_plan', (r.attributes->'mrr'->>'amount_minor')::float
		FROM records r JOIN record_identities i ON i.record_id = r.id WHERE i.namespace = 'stripe.customer' AND i.value = 'cus_bo'`).
		Scan(&boStripe, &boStatus, &boPlan, &boMRR); err != nil {
		t.Fatal(err)
	}
	if boStripe == bo["id"] || boStatus != "trialing" || boPlan != "Pro" || boMRR != 0 {
		t.Fatalf("Bo's Stripe record: %s %s %s %v", boStripe, boStatus, boPlan, boMRR)
	}

	// Billing fields are Outflare's own: nobody writes them.
	if got := owner.must(400, "PATCH", base+"/records/"+ann["id"].(string), map[string]any{"attributes": map[string]any{"billing_plan": "Free"}}); got["error"] == nil {
		t.Fatal("wrote a computed field")
	}

	// Syncing again stores nothing twice.
	if err := e.srv.SyncPayments(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	rows, err := e.pool.Query(ctx, `SELECT name, count(*) FROM customer_events WHERE workspace_id = $1 AND record_id = $2 GROUP BY 1`, ws, ann["id"])
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		var n int
		_ = rows.Scan(&name, &n)
		counts[name] = n
	}
	if counts["payment.succeeded"] != 2 || counts["payment.refunded"] != 2 || counts["subscription.started"] != 1 || counts["subscription.changed"] != 1 {
		t.Fatalf("Ann's timeline: %v", counts)
	}
	var audits int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'payments.synced'`, ws).Scan(&audits)
	if audits != 1 {
		t.Fatalf("payments.synced audits: %d", audits)
	}

	// A forgotten change cursor starts over, without doubling anything.
	if _, err := e.pool.Exec(ctx, `UPDATE payment_syncs SET changes_cursor = 'evt_gone' WHERE integration_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.SyncPayments(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if v := owner.must(200, "GET", path, nil); v["phase"] != "backfill" {
		t.Fatalf("after a lost cursor: %v", v)
	}

	// Other connections have no payment sync.
	sl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "smartlead", "credentials": map[string]string{"api_key": "sl-key"}})
	owner.must(404, "GET", base+"/integrations/"+sl["id"].(string)+"/payment-sync", nil)

	// The loop queues the connection once per interval.
	if err := e.srv.SchedulePayments(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestStripeRefusedKeyMarksTheConnection(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	e.integrations.Client.BaseURLs["stripe"] = fakeStripe(t).URL
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "stripe", "credentials": map[string]string{"api_key": "rk_test_key"}})
	id := i["id"].(string)
	// The key is revoked in Stripe after it was connected.
	e.integrations.Client.BaseURLs["stripe"] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) })).URL
	if err := e.srv.SyncPayments(context.Background(), ws, id); err != nil {
		t.Fatal(err)
	}
	v := owner.must(200, "GET", base+"/integrations/"+id+"/payment-sync", nil)
	if v["last_error"] == nil {
		t.Fatalf("sync: %v", v)
	}
	if got := owner.must(200, "GET", base+"/integrations/"+id, nil); got["status"] != "error" {
		t.Fatalf("integration: %v", got)
	}
	owner.must(409, "POST", base+"/integrations/"+id+"/payment-sync", nil)
}

// syncedStripe connects the fake Stripe account and syncs it once.
func syncedStripe(t *testing.T, e *env, owner *client, ws string) string {
	t.Helper()
	e.integrations.Client.BaseURLs["stripe"] = fakeStripe(t).URL
	i := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "stripe", "credentials": map[string]string{"api_key": "rk_test_key"}})
	if err := e.srv.SyncPayments(context.Background(), ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	return i["id"].(string)
}

func stripeRecord(t *testing.T, e *env, customer string) string {
	t.Helper()
	var id string
	if err := e.pool.QueryRow(context.Background(), `SELECT record_id::text FROM record_identities WHERE namespace = 'stripe.customer' AND value = $1 AND valid_to IS NULL`,
		customer).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// A field a workspace made before the billing fields existed keeps its
// key, and payments leave it alone.
func TestPaymentsKeepAWorkspacesOwnField(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO field_definitions (workspace_id, record_type, key, label, value_type)
		VALUES ($1, 'person', 'mrr', 'Our MRR', 'number')`, ws); err != nil {
		t.Fatal(err)
	}
	// And its own billing statuses, with a field that uses them.
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO option_sets (workspace_id, key, name, options)
		VALUES ($1, 'billing_status', 'Our statuses', '[{"key": "paying", "label": "Paying"}, {"key": "lapsed", "label": "Lapsed"}]')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(context.Background(), `INSERT INTO field_definitions (workspace_id, record_type, key, label, value_type, option_set)
		VALUES ($1, 'person', 'billing_status', 'Our status', 'select', 'billing_status')`, ws); err != nil {
		t.Fatal(err)
	}
	ann := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ann", "attributes": map[string]any{"mrr": 12, "billing_status": "paying"},
		"identities": []any{map[string]any{"namespace": "user_id", "value": "u_ann"}}})
	syncedStripe(t, e, owner, ws)

	cat := owner.must(200, "GET", base+"/catalog/types/person", nil)
	n := map[string]int{}
	for _, f := range cat["fields"].([]any) {
		f := f.(map[string]any)
		key, _ := f["key"].(string)
		n[key]++
		if (key == "mrr" || key == "billing_status") && f["builtin"] != false {
			t.Fatalf("%s field: %v", key, f)
		}
	}
	if n["mrr"] != 1 || n["billing_status"] != 1 {
		t.Fatalf("fields: %v", n)
	}
	sets := 0
	for _, o := range owner.must(200, "GET", base+"/catalog", nil)["option_sets"].([]any) {
		if o := o.(map[string]any); o["key"] == "billing_status" {
			sets++
			if o["name"] != "Our statuses" {
				t.Fatalf("option set: %v", o)
			}
		}
	}
	if sets != 1 {
		t.Fatalf("%d billing_status option sets", sets)
	}
	a := owner.must(200, "GET", base+"/records/"+ann["id"].(string), nil)
	if attrs := a["attributes"].(map[string]any); attrs["mrr"] != 12.0 || attrs["billing_status"] != "paying" || attrs["billing_plan"] != "Team" {
		t.Fatalf("attributes: %v", attrs)
	}
	owner.must(200, "PATCH", base+"/records/"+ann["id"].(string), map[string]any{"attributes": map[string]any{"mrr": 13, "billing_status": "lapsed"}})
}

// Merging two paying people moves their subscriptions and works their
// billing fields out again from both. Billing ids and emails another
// person holds are listed as duplicates, never merged.
func TestPaymentsFollowAMerge(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	ann := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ann",
		"identities": []any{map[string]any{"namespace": "user_id", "value": "u_ann"}}})
	// Someone already holds Cy's billing email, verified.
	cyMail := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cy",
		"identities": []any{map[string]any{"namespace": "email", "value": "cy@acme.example", "verified": true}}})
	// Bo's Stripe customer is already a person, and the user id in its
	// metadata belongs to someone else.
	boStripe := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo (billing)",
		"identities": []any{map[string]any{"namespace": "stripe.customer", "value": "cus_bo", "verified": true}}})
	boUser := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Bo",
		"identities": []any{map[string]any{"namespace": "user_id", "value": "u_bo"}}})
	id := syncedStripe(t, e, owner, ws)

	v := owner.must(200, "GET", base+"/integrations/"+id+"/payment-sync", nil)
	if v["totals"].(map[string]any)["id_conflicts"] != 1.0 {
		t.Fatalf("totals: %v", v)
	}
	pairs := map[string]string{}
	rows, err := e.pool.Query(ctx, `SELECT record_a::text, record_b::text, reason, status FROM record_merge_candidates WHERE workspace_id = $1`, ws)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var x, y, reason, status string
		_ = rows.Scan(&x, &y, &reason, &status)
		pairs[x+y] = reason + ":" + status
		pairs[y+x] = reason + ":" + status
	}
	cy := stripeRecord(t, e, "cus_cy")
	if pairs[cy+cyMail["id"].(string)] != "email:open" || !strings.HasPrefix(pairs[boStripe["id"].(string)+boUser["id"].(string)], "user_id:") {
		t.Fatalf("duplicates: %v", pairs)
	}
	if stripeRecord(t, e, "cus_bo") != boStripe["id"] {
		t.Fatal("Bo's billing customer moved")
	}

	m := owner.must(200, "POST", base+"/records/"+ann["id"].(string)+"/merge", map[string]any{"from": cy})
	if m["moved"].(map[string]any)["subscriptions"] != 1.0 {
		t.Fatalf("moved: %v", m)
	}
	attrs := m["record"].(map[string]any)["attributes"].(map[string]any)
	if mrr := attrs["mrr"].(map[string]any); mrr["amount_minor"] != 14800.0 || attrs["billing_status"] != "active" || attrs["billing_plan"] != "Team" {
		t.Fatalf("after the merge: %v", attrs)
	}
	var left int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM payment_subscriptions WHERE record_id = $1`, cy).Scan(&left)
	if left != 0 {
		t.Fatalf("%d subscriptions left on the merged record", left)
	}
}
