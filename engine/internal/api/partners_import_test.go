package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// fakeTolt is a Tolt account with one program. It answers two objects a
// page, so imports follow the cursor.
type fakeTolt struct {
	mu   sync.Mutex
	data map[string][]map[string]any
}

func (f *fakeTolt) set(path string, objs ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.data[path] = objs
}

func (f *fakeTolt) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tlt_test_not_a_real_key" {
		w.WriteHeader(401)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	objs, ok := f.data[r.URL.Path]
	if !ok {
		w.WriteHeader(404)
		return
	}
	if r.URL.Path != "/programs" && r.URL.Query().Get("program_id") != "prg_1" {
		w.WriteHeader(400)
		return
	}
	start := 0
	if after := r.URL.Query().Get("starting_after"); after != "" {
		for i, o := range objs {
			if o["id"] == after {
				start = i + 1
			}
		}
	}
	end := min(start+2, len(objs))
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": objs[start:end], "has_more": end < len(objs)}) //nolint:errcheck
}

func TestPartnerToltImport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	day := func(d int) string { return time.Date(2026, 3, d, 12, 0, 0, 0, time.UTC).Format(time.RFC3339) }

	tolt := &fakeTolt{data: map[string][]map[string]any{}}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd"})
	tolt.set("/partners",
		map[string]any{"id": "p1", "first_name": "Ana", "last_name": "Lopez", "email": "ana@acme.example", "status": "active",
			"payout_method": "paypal", "payout_details": "ana@acme.example", "created_at": day(1)},
		map[string]any{"id": "p2", "first_name": "Ben", "email": "ben@acme.example", "status": "pending"},
		map[string]any{"id": "p3", "first_name": "Nobody", "status": "active"},
		map[string]any{"id": "p4", "first_name": "Cid", "email": "cid@acme.example", "status": "active"})
	tolt.set("/links",
		map[string]any{"id": "l1", "partner_id": "p1", "param": "ref", "value": "ANA"},
		map[string]any{"id": "l2", "partner_id": "p2", "param": "via", "value": "BEN"},
		map[string]any{"id": "l3", "partner_id": "p4", "param": "ref", "value": "has space"},
		map[string]any{"id": "l4", "partner_id": "p1", "param": "ref", "value": "ANA2"},
		map[string]any{"id": "l5", "partner_id": "p2", "param": "aff", "value": "BEN2"})
	tolt.set("/promotion-codes",
		map[string]any{"id": "pc1", "partner_id": "p1", "code": "ANA10"},
		map[string]any{"id": "pc2", "partner_id": "p9", "code": "GHOST"})
	tolt.set("/customers",
		map[string]any{"id": "c1", "partner": map[string]any{"id": "p1"}, "email": "cara@example.com", "customer_id": "u-1", "created_at": day(2)},
		map[string]any{"id": "c2", "partner_id": "p2", "customer_id": "u-2", "created_at": day(3)})
	tolt.set("/transactions",
		map[string]any{"id": "t1", "customer_id": "c1", "amount": 4900, "billing_type": "subscription", "created_at": day(2)},
		map[string]any{"id": "t2", "customer_id": "c1", "amount": 4900, "billing_type": "subscription", "status": "refunded", "created_at": day(4)},
		map[string]any{"id": "t3", "customer_id": "c2", "amount": "1000", "billing_type": "one_time", "created_at": day(5)})
	tolt.set("/commissions",
		map[string]any{"id": "m1", "partner_id": "p1", "customer_id": "c1", "transaction_id": "t1", "amount": 980, "status": "paid",
			"created_at": day(2), "paid_at": day(20), "payout_id": "po_1"},
		map[string]any{"id": "m2", "partner_id": "p1", "customer_id": "c1", "transaction_id": "t2", "amount": 980, "status": "rejected", "created_at": day(4)},
		map[string]any{"id": "m3", "partner_id": "p2", "customer_id": "c2", "transaction_id": "t3", "amount": 200, "status": "pending", "created_at": day(5)},
		map[string]any{"id": "m4", "partner_id": "p1", "amount": 1000, "status": "approved", "created_at": day(6)})
	srv := httptest.NewServer(tolt)
	defer srv.Close()
	e.integrations.Client.BaseURLs["tolt"] = srv.URL

	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	imports := base + "/partner-programs/affiliates/imports"
	owner.must(409, "POST", imports, map[string]any{}) // Tolt isn't connected
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})

	run := func() map[string]any {
		t.Helper()
		imp := owner.must(201, "POST", imports, map[string]any{})
		owner.must(409, "POST", imports, map[string]any{}) // one at a time
		e.runCRMImports(t)
		for range 500 {
			v := owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
			if v["status"] == "done" || v["status"] == "failed" {
				if v["status"] != "done" {
					t.Fatalf("import = %v", v)
				}
				return v
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("the import did not finish")
		return nil
	}
	kinds := func(v map[string]any) map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, k := range v["summary"].(map[string]any)["kinds"].([]any) {
			m := k.(map[string]any)
			out[m["kind"].(string)] = m
		}
		return out
	}
	reasons := func(v map[string]any) map[string]int {
		out := map[string]int{}
		for _, i := range v["summary"].(map[string]any)["issues"].([]any) {
			out[i.(map[string]any)["reason"].(string)]++
		}
		return out
	}

	first := run()
	if first["source_program"] != "prg_1" {
		t.Fatalf("source_program = %v", first["source_program"])
	}
	k := kinds(first)
	if p := k["partners"]; p["seen"] != 4.0 || p["created"] != 3.0 || p["skipped"] != 1.0 {
		t.Fatalf("partners = %v", p)
	}
	if want := map[string]int{"no_email": 1, "code_changed": 1, "extra_link": 1, "link_param": 1, "partner_missing": 1}; jsonString(reasons(first)) != jsonString(want) {
		t.Fatalf("issues = %v", reasons(first))
	}
	codes := map[string]string{}
	rows, _ := e.pool.Query(ctx, `SELECT i.value, m.code, m.status FROM partner_memberships m JOIN record_identities i ON i.record_id = m.record_id AND i.namespace = 'email'
		WHERE m.workspace_id = $1`, ws)
	for rows.Next() {
		var email, code, status string
		rows.Scan(&email, &code, &status) //nolint:errcheck
		codes[email] = code + ":" + status
	}
	if codes["ana@acme.example"] != "ANA:active" || codes["ben@acme.example"] != "BEN:pending" || codes["cid@acme.example"] == "" || codes["cid@acme.example"] == "has space:active" {
		t.Fatalf("codes = %v", codes)
	}
	// Ana's payout details came over, sealed.
	ana := owner.must(200, "GET", base+"/partners?program=affiliates&status=active", nil)["data"].([]any)
	var anaID string
	for _, p := range ana {
		if m := p.(map[string]any); m["code"] == "ANA" {
			anaID = m["id"].(string)
			if m["payout_method"] != "paypal: ana@acme.example" {
				t.Fatalf("payout_method = %v", m["payout_method"])
			}
		}
	}

	var sales, refunds, referrals, promo int
	e.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE type = 'sale'), count(*) FILTER (WHERE type = 'refund') FROM partner_conversions
		WHERE workspace_id = $1 AND source = 'import'`, ws).Scan(&sales, &refunds) //nolint:errcheck
	e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_referrals WHERE workspace_id = $1 AND via = 'import'`, ws).Scan(&referrals)                         //nolint:errcheck
	e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_promo_codes WHERE workspace_id = $1 AND code = 'ANA10' AND sync_status = 'local'`, ws).Scan(&promo) //nolint:errcheck
	if sales != 3 || refunds != 1 || referrals != 2 || promo != 1 {
		t.Fatalf("sales %d, refunds %d, referrals %d, promo codes %d", sales, refunds, referrals, promo)
	}
	// Imported sales are not customer events: the workspace's billing told attribution already.
	var events int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name LIKE 'payment.%'`, ws).Scan(&events) //nolint:errcheck
	if events != 0 {
		t.Fatalf("payment events = %d", events)
	}

	statuses := func() map[string]string {
		out := map[string]string{}
		rows, _ := e.pool.Query(ctx, `SELECT f.external_id, w.status, coalesce(p.status, '') FROM partner_rewards w
			JOIN partner_import_refs f ON f.local_id = w.id AND f.kind = 'commission' LEFT JOIN partner_payouts p ON p.id = w.payout_id
			WHERE w.workspace_id = $1`, ws)
		for rows.Next() {
			var id, s, payout string
			rows.Scan(&id, &s, &payout) //nolint:errcheck
			out[id] = s + "/" + payout
		}
		return out
	}
	if got := statuses(); jsonString(got) != jsonString(map[string]string{"m1": "paid/paid", "m2": "rejected/", "m3": "pending/", "m4": "approved/"}) {
		t.Fatalf("rewards = %v", got)
	}
	// Approved and paid commissions are spend on the day they were earned.
	var spend int64
	var earliest time.Time
	e.pool.QueryRow(ctx, `SELECT coalesce(sum(amount_cents), 0), min(occurred_at) FROM spend_entries WHERE workspace_id = $1 AND provider = 'partners'`, ws).
		Scan(&spend, &earliest) //nolint:errcheck
	if spend != 1980 || !earliest.Equal(time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("spend %d from %v", spend, earliest)
	}
	// Importing sends no reward webhooks.
	var hooks int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action LIKE 'partner_reward.%'`, ws).Scan(&hooks) //nolint:errcheck
	if hooks != 0 {
		t.Fatalf("reward events = %d", hooks)
	}
	st := owner.must(200, "GET", base+"/partners/"+anaID, nil)["stats"].(map[string]any)
	if st["paid_cents"] != 980.0 || st["approved_cents"] != 1000.0 {
		t.Fatalf("Ana's stats = %v", st)
	}

	// Switch-over day: a run brings what changed there and nothing twice.
	tolt.mu.Lock()
	tolt.data["/partners"] = append(tolt.data["/partners"], map[string]any{"id": "p5", "first_name": "Dee", "email": "dee@acme.example", "status": "active"})
	tolt.data["/partners"][1]["status"] = "active"
	tolt.data["/commissions"][2]["status"] = "approved"
	tolt.mu.Unlock()
	second := run()
	k = kinds(second)
	if p := k["partners"]; p["created"] != 1.0 || p["updated"] != 1.0 {
		t.Fatalf("partners again = %v", p)
	}
	if c := k["commissions"]; c["created"] != 0.0 || c["updated"] != 1.0 || c["unchanged"] != 3.0 {
		t.Fatalf("commissions again = %v", c)
	}
	if c := k["transactions"]; c["created"] != 0.0 || c["unchanged"] != 3.0 {
		t.Fatalf("transactions again = %v", c)
	}
	if got := statuses(); got["m3"] != "approved/" || len(got) != 4 {
		t.Fatalf("rewards again = %v", got)
	}
	list := owner.must(200, "GET", imports, nil)["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("imports = %d", len(list))
	}
	var finished int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partner_import.finished'`, ws).Scan(&finished) //nolint:errcheck
	if finished != 2 {
		t.Fatalf("finished events = %d", finished)
	}
}
