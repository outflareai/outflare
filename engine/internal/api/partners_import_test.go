package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeTolt is a Tolt account with one program. It answers two objects a
// page, so imports follow the cursor.
type fakeTolt struct {
	mu   sync.Mutex
	data map[string][]map[string]any
	// ignoreCursor answers the first page every time, as a platform that
	// pages some other way would.
	ignoreCursor bool
	// nested answers these paths as {"data": {"data": [...]}} with has_more
	// a string, as Tolt's customers list does; html answers them with a
	// web page and a 200.
	nested, html map[string]bool
	// fullPages answers the limit asked for (100) instead of two a page.
	fullPages bool
	// unlisted partners are left out of /partners, as Tolt leaves out
	// partners who haven't onboarded, but read at /partners/{id}.
	unlisted map[string]map[string]any
	// capAt stops a list walked by cursor after that many objects, saying
	// there are no more, as a list with a hard cap would; asking after a
	// plain object id goes on from there.
	capAt int
	// hidden adds to the active partners' total_count: partners the list
	// never gives.
	hidden int
	// wholeTotal gives the whole partner list's total_count whatever the
	// status asked.
	wholeTotal bool
	// refuseStatus answers 400 to a partner list filtered by this status,
	// as an account without it might.
	refuseStatus string
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
	if id, ok := strings.CutPrefix(r.URL.Path, "/partners/"); ok {
		p, ok := f.unlisted[id]
		if !ok {
			for _, o := range f.data["/partners"] {
				if o["id"] == id {
					p = o
				}
			}
		}
		if p == nil {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": p}) //nolint:errcheck
		return
	}
	objs, ok := f.data[r.URL.Path]
	if !ok && r.URL.Path != "/clicks" { // no clicks unless a test sets some
		w.WriteHeader(404)
		return
	}
	if r.URL.Path != "/programs" && r.URL.Query().Get("program_id") != "prg_1" {
		w.WriteHeader(400)
		return
	}
	// Partners are listed by status, as Tolt's v2 list filters them.
	if st := r.URL.Query().Get("status"); st != "" && st == f.refuseStatus {
		w.WriteHeader(400)
		return
	}
	if st := r.URL.Query().Get("status"); st != "" {
		var only []map[string]any
		for _, o := range objs {
			if o["status"] == st {
				only = append(only, o)
			}
		}
		objs = only
	}
	// A cursor is cur_<last id>~<where its walk began>, so a capped walk
	// stops capAt objects after it began.
	start, origin := 0, 0
	raw := r.URL.Query().Get("starting_after")
	after, from, _ := strings.Cut(strings.TrimPrefix(raw, "cur_"), "~")
	if after != "" && !f.ignoreCursor {
		for i, o := range objs {
			if o["id"] == after {
				start = i + 1
			}
		}
		origin, _ = strconv.Atoi(from)
		if !strings.HasPrefix(raw, "cur_") {
			origin = start
		}
	}
	total := len(objs)
	switch {
	case r.URL.Path == "/partners" && f.wholeTotal:
		total = len(f.data["/partners"]) + f.hidden
	case r.URL.Path == "/partners" && r.URL.Query().Get("status") == "active":
		total += f.hidden
	}
	if f.capAt > 0 {
		objs = objs[:min(len(objs), origin+f.capAt)]
	}
	size := 2
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); f.fullPages && err == nil {
		size = n
	}
	end := min(start+size, len(objs))
	next := ""
	if end < len(objs) && end > 0 {
		next = "cur_" + objs[end-1]["id"].(string) + "~" + strconv.Itoa(origin)
	}
	switch {
	case f.html[r.URL.Path]:
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte("<html><body>Not here. Write to help@example.com</body></html>")) //nolint:errcheck
	case f.nested[r.URL.Path]:
		json.NewEncoder(w).Encode(map[string]any{"success": true, "total_count": len(objs), //nolint:errcheck
			"data": map[string]any{"data": objs[start:end]}, "has_more": strconv.FormatBool(end < len(objs)), "next_cursor": next})
	default:
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": objs[start:end], "has_more": end < len(objs), "next_cursor": next, //nolint:errcheck
			"total_count": total})
	}
}

func TestPartnerToltImport(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	day := func(d int) string { return time.Date(2026, 3, d, 12, 0, 0, 0, time.UTC).Format(time.RFC3339) }

	tolt := &fakeTolt{data: map[string][]map[string]any{}}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd"})
	tolt.set("/groups",
		map[string]any{"id": "grp_1", "name": "Standard", "is_default": true},
		map[string]any{"id": "grp_2", "name": "Gold"})
	tolt.set("/flows",
		map[string]any{"id": "flw_1", "name": "Base", "group_id": "grp_1", "trigger": "new_payment",
			"action": map[string]any{"type": "commission", "commission_type": "percentage", "value": 20}, "approval": map[string]any{"type": "auto_after", "days": 30}},
		map[string]any{"id": "flw_2", "name": "Gold rate", "group_id": "grp_2", "trigger": "new_payment",
			"action":       map[string]any{"type": "commission", "commission_type": "percentage", "value": "25"},
			"conditions":   map[string]any{"operator": "and", "groups": []any{map[string]any{"operator": "or", "conditions": []any{map[string]any{"attribute": "partner_transactions_count", "operator": "greater_than", "value": "5"}}}}},
			"custom_rates": []any{map[string]any{"partner_id": "p1", "value": 30}}},
		map[string]any{"id": "flw_3", "name": "Odd", "trigger": "new_payment", "action": map[string]any{"type": "commission", "commission_type": "percentage", "value": 10},
			"conditions": []any{map[string]any{"attribute": "partner_mood", "operator": "equals", "value": "happy"}}})
	tolt.set("/partners",
		map[string]any{"id": "p1", "first_name": "Ana", "last_name": "Lopez", "email": "ana@acme.example", "status": "active", "group_id": "grp_2", "created_at": day(1)},
		map[string]any{"id": "p2", "first_name": "Ben", "email": "ben@acme.example", "status": "pending"},
		map[string]any{"id": "p3", "first_name": "Nobody", "status": "active"},
		map[string]any{"id": "p4", "first_name": "Cid", "email": "cid@acme.example", "status": "active"},
		map[string]any{"id": "p6", "first_name": "Eve", "email": "eve@acme.example", "status": "rejected"})
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
		map[string]any{"id": "cus_c1", "partner": map[string]any{"id": "p1"}, "email": "cara@example.com", "customer_id": "u-1", "created_at": day(2)},
		map[string]any{"id": "c2", "partner_id": "p2", "customer_id": "u-2", "created_at": day(3)})
	tolt.set("/transactions",
		map[string]any{"id": "t1", "customer_id": "cus_c1", "amount": 4900, "billing_type": "subscription", "created_at": day(2)},
		map[string]any{"id": "t2", "customer_id": "cus_c1", "amount": 4900, "billing_type": "subscription", "status": "refunded", "created_at": day(4)},
		map[string]any{"id": "t3", "customer_id": "c2", "amount": "1000", "billing_type": "one_time", "created_at": day(5)})
	tolt.set("/clicks",
		map[string]any{"id": "k1", "partner_id": "p1", "referrer": "https://blog.example.org/post?utm=x", "created_at": day(1)},
		map[string]any{"id": "k2", "partner_id": "p1", "created_at": day(2)},
		map[string]any{"id": "k3", "partner_id": "p9", "created_at": day(2)})
	tolt.set("/commissions",
		map[string]any{"id": "m1", "partner_id": "p1", "customer_id": "cus_c1", "transaction_id": "t1", "amount": 980, "status": "paid",
			"created_at": day(2), "paid_at": day(20), "payout_id": "po_1"},
		map[string]any{"id": "m2", "partner_id": "p1", "customer_id": "cus_c1", "transaction_id": "t2", "amount": 980, "status": "rejected", "created_at": day(4)},
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
	if p := k["partners"]; p["seen"] != 5.0 || p["created"] != 4.0 || p["skipped"] != 1.0 {
		t.Fatalf("partners = %v", p)
	}
	if want := map[string]int{"no_email": 1, "code_changed": 1, "extra_link": 1, "link_param": 1, "partner_missing": 2, "partner_unavailable": 1, "flow_condition_unsupported": 1}; jsonString(reasons(first)) != jsonString(want) {
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
	// Tolt's v2 API has no payout details: partners give theirs on the portal.
	ana := owner.must(200, "GET", base+"/partners?program=affiliates&status=active", nil)["data"].([]any)
	var anaID string
	for _, p := range ana {
		if m := p.(map[string]any); m["code"] == "ANA" {
			anaID = m["id"].(string)
			if m["payout_method"] != "" {
				t.Fatalf("payout_method = %v", m["payout_method"])
			}
		}
	}
	// Every status was read, each as it is in Tolt.
	if n := len(owner.must(200, "GET", base+"/partners?program=affiliates&status=rejected", nil)["data"].([]any)); n != 1 {
		t.Fatalf("rejected partners = %d", n)
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
	// Her clicks in Tolt count here, with the referring host only.
	var host string
	e.pool.QueryRow(ctx, `SELECT coalesce(max(referrer_host), '') FROM partner_clicks WHERE workspace_id = $1`, ws).Scan(&host) //nolint:errcheck
	if st["clicks"] != 2.0 || host != "blog.example.org" {
		t.Fatalf("Ana's clicks %v, referrer %q", st["clicks"], host)
	}
	if st["paid_cents"] != 980.0 || st["approved_cents"] != 1000.0 {
		t.Fatalf("Ana's stats = %v", st)
	}

	// Tolt's groups and flows came over: Ana is in Gold, Standard is the
	// default, and the Gold flow kept its condition and her own rate.
	prog := owner.must(200, "GET", base+"/partner-programs/affiliates", nil)
	groupIDs := map[string]string{}
	for _, g := range prog["groups"].([]any) {
		groupIDs[g.(map[string]any)["name"].(string)] = g.(map[string]any)["id"].(string)
	}
	if prog["default_group_id"] != groupIDs["Standard"] || groupIDs["Gold"] == "" {
		t.Fatalf("groups = %v, default %v", groupIDs, prog["default_group_id"])
	}
	if a := owner.must(200, "GET", base+"/partners/"+anaID, nil); a["group_id"] != groupIDs["Gold"] {
		t.Fatalf("Ana's group = %v", a["group_id"])
	}
	flowsByName := map[string]map[string]any{}
	for _, f := range prog["rules"].([]any) {
		flowsByName[f.(map[string]any)["name"].(string)] = f.(map[string]any)
	}
	gr := flowsByName["Gold rate"]
	if base := flowsByName["Base"]; base == nil || base["approval"] != "after_days" || base["approval_days"] != 30.0 || base["group_id"] != groupIDs["Standard"] ||
		gr == nil || gr["percent_bp"] != 2500.0 || len(gr["overrides"].([]any)) != 1 || len(gr["conditions"].(map[string]any)["groups"].([]any)) != 1 || flowsByName["Odd"] != nil {
		t.Fatalf("flows = %v", flowsByName)
	}

	// The program's totals are the sum over every partner, whatever a page holds.
	totals := owner.must(200, "GET", base+"/partner-programs/affiliates/stats", nil)
	var sum = map[string]float64{}
	all := owner.must(200, "GET", base+"/partners?program=affiliates&limit=1", nil)
	for page := all; ; {
		for _, p := range page["data"].([]any) {
			for k, v := range p.(map[string]any)["stats"].(map[string]any) {
				sum[k] += v.(float64)
			}
		}
		next, _ := page["next_before"].(string)
		if next == "" {
			break
		}
		page = owner.must(200, "GET", base+"/partners?program=affiliates&limit=1&before="+url.QueryEscape(next), nil)
	}
	for _, k := range []string{"customers", "sales_cents", "pending_cents", "paid_cents", "balance_cents"} {
		if totals[k] != sum[k] {
			t.Fatalf("totals %s = %v, partners add up to %v", k, totals[k], sum[k])
		}
	}
	if pt := totals["partners"].(map[string]any); pt["total"] != 4.0 || pt["active"] != 2.0 || pt["pending"] != 1.0 {
		t.Fatalf("partners = %v", pt)
	}
	payouts := owner.must(200, "GET", base+"/partner-payouts?program=affiliates", nil)["data"].([]any)
	if len(payouts) != 1 || payouts[0].(map[string]any)["partner_name"] != "Ana Lopez" {
		t.Fatalf("payouts = %v", payouts)
	}
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Friends", "kind": "referral"})
	if n := len(owner.must(200, "GET", base+"/partner-payouts?program=friends", nil)["data"].([]any)); n != 0 {
		t.Fatalf("another program's payouts = %d", n)
	}

	// After the switch the backend reports renewals with Tolt's own customer
	// id, which tolt.signup() returned and it stored: Ana is still credited.
	renewal := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "cus_c1", "amount_cents": 4900, "external_id": "inv-after"})
	if renewal["credited"] != true || renewal["partner_id"] != anaID {
		t.Fatalf("renewal = %v", renewal)
	}
	// An earlier run didn't keep the alias: the next run adds it.
	if _, err := e.pool.Exec(ctx, `DELETE FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'cus_c1'`, ws); err != nil {
		t.Fatal(err)
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
	if c := k["clicks"]; c["created"] != 0.0 || c["unchanged"] != 2.0 || c["skipped"] != 1.0 {
		t.Fatalf("clicks again = %v", c)
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
	var alias int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'cus_c1'`, ws).Scan(&alias) //nolint:errcheck
	if alias != 1 {
		t.Fatalf("cus_c1 aliases after the second run = %d", alias)
	}
	var finished int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partner_import.finished'`, ws).Scan(&finished) //nolint:errcheck
	if finished != 2 {
		t.Fatalf("finished events = %d", finished)
	}
}

// TestPartnerImportPagingStuck: a platform that sends the same page again
// stops the import with pagination_stuck instead of reading forever.
func TestPartnerImportPagingStuck(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	tolt := &fakeTolt{data: map[string][]map[string]any{}, ignoreCursor: true}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd"})
	tolt.set("/links")
	tolt.set("/partners",
		map[string]any{"id": "p1", "first_name": "Ana", "email": "ana@acme.example", "status": "active"},
		map[string]any{"id": "p2", "first_name": "Ben", "email": "ben@acme.example", "status": "active"},
		map[string]any{"id": "p3", "first_name": "Cid", "email": "cid@acme.example", "status": "active"})
	srv := httptest.NewServer(tolt)
	defer srv.Close()
	e.integrations.Client.BaseURLs["tolt"] = srv.URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})
	imports := base + "/partner-programs/affiliates/imports"
	imp := owner.must(201, "POST", imports, map[string]any{})
	e.runCRMImports(t)
	for range 500 {
		v := owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
		if v["status"] == "failed" {
			var code string
			e.pool.QueryRow(context.Background(), `SELECT data->>'error_code' FROM audit_events WHERE workspace_id = $1 AND action = 'partner_import.stopped'`, ws).Scan(&code) //nolint:errcheck
			if code != "pagination_stuck" {
				t.Fatalf("error_code = %q (%v)", code, v["error"])
			}
			return
		}
		if v["status"] == "done" {
			t.Fatalf("import = %v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the import did not stop")
}

// TestPartnerImportUnreadableKind: a list nested as Tolt's customers list
// is still read, and a kind Tolt answers with something that isn't a list
// is noted while the others come over.
func TestPartnerImportUnreadableKind(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	tolt := &fakeTolt{data: map[string][]map[string]any{},
		nested: map[string]bool{"/customers": true}, html: map[string]bool{"/promotion-codes": true}}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd"})
	tolt.set("/links", map[string]any{"id": "l1", "partner_id": "p1", "param": "ref", "value": "ANA"})
	tolt.set("/partners", map[string]any{"id": "p1", "first_name": "Ana", "email": "ana@acme.example", "status": "active"})
	tolt.set("/promotion-codes", map[string]any{"id": "pc1", "partner_id": "p1", "code": "ANA10"})
	tolt.set("/customers",
		map[string]any{"id": "cus_1", "partner_id": "p1", "customer_id": "u-1"},
		map[string]any{"id": "cus_2", "partner_id": "p1", "customer_id": "u-2"},
		map[string]any{"id": "cus_3", "partner_id": "p1", "customer_id": "u-3"})
	tolt.set("/transactions", map[string]any{"id": "t1", "customer_id": "cus_3", "amount": 4900})
	tolt.set("/commissions", map[string]any{"id": "m1", "partner_id": "p1", "customer_id": "cus_3", "transaction_id": "t1", "amount": 980, "status": "approved"})
	srv := httptest.NewServer(tolt)
	defer srv.Close()
	e.integrations.Client.BaseURLs["tolt"] = srv.URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})
	imports := base + "/partner-programs/affiliates/imports"
	imp := owner.must(201, "POST", imports, map[string]any{})
	e.runCRMImports(t)
	var v map[string]any
	for range 500 {
		v = owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
		if v["status"] == "done" || v["status"] == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v["status"] != "done" {
		t.Fatalf("import = %v", v)
	}
	sum := v["summary"].(map[string]any)
	for _, k := range sum["kinds"].([]any) {
		m := k.(map[string]any)
		want := map[string]float64{"customers": 3, "transactions": 1, "commissions": 1, "promo_codes": 0}[m["kind"].(string)]
		if m["kind"] != "links" && m["kind"] != "partners" && m["created"] != want {
			t.Fatalf("%v created %v, want %v", m["kind"], m["created"], want)
		}
	}
	// This fake lists no groups or flows (404), as some Tolt accounts
	// don't: each is noted and the run goes on.
	issues := sum["issues"].([]any)
	if len(issues) != 3 || issues[0].(map[string]any)["reason"] != "groups_unavailable" || issues[1].(map[string]any)["reason"] != "flows_unavailable" {
		t.Fatalf("issues = %v", issues)
	}
	is := issues[2].(map[string]any)
	msg, _ := is["message"].(string)
	if is["kind"] != "promo_codes" || is["reason"] != "unreadable" ||
		!strings.Contains(msg, "promo codes") || !strings.Contains(msg, "HTTP 200") || !strings.Contains(msg, "text/html") || strings.Contains(msg, "@") {
		t.Fatalf("issue = %v", is)
	}
}

// TestPartnerImportSources: the programs in the Tolt account are listed to
// pick from, and an import with none named takes the only active one.
func TestPartnerImportSources(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	tolt := &fakeTolt{data: map[string][]map[string]any{}}
	tolt.set("/programs",
		map[string]any{"id": "prg_old", "name": "Old program", "currency": "usd", "status": "archived"},
		map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
	tolt.set("/links")
	tolt.set("/partners", map[string]any{"id": "p1", "first_name": "Ana", "email": "ana@acme.example", "status": "active"})
	for _, k := range []string{"/promotion-codes", "/customers", "/transactions", "/commissions"} {
		tolt.set(k)
	}
	srv := httptest.NewServer(tolt)
	defer srv.Close()
	e.integrations.Client.BaseURLs["tolt"] = srv.URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	sources := base + "/partner-programs/affiliates/import-sources"
	owner.must(409, "GET", sources, nil) // Tolt isn't connected
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})
	got := owner.must(200, "GET", sources, nil)
	list := got["data"].([]any)
	if len(list) != 2 || got["suggested"] != "prg_1" || list[0].(map[string]any)["status"] != "inactive" {
		t.Fatalf("sources = %v", got)
	}
	imports := base + "/partner-programs/affiliates/imports"
	imp := owner.must(201, "POST", imports, map[string]any{})
	e.runCRMImports(t)
	for range 500 {
		v := owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
		if v["status"] == "done" || v["status"] == "failed" {
			if v["status"] != "done" || v["source_program"] != "prg_1" {
				t.Fatalf("import = %v", v)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the import did not finish")
}

// TestPartnerImportManyPartners: a thousand partners over ten pages all
// come over, and a partner the list leaves out (not onboarded) is read by
// id when a customer names them.
func TestPartnerImportManyPartners(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	tolt := &fakeTolt{data: map[string][]map[string]any{}, fullPages: true, unlisted: map[string]map[string]any{
		"p_new":  {"id": "p_new", "first_name": "Ivy", "email": "ivy@acme.example", "status": "invited"},
		"p_link": {"id": "p_link", "first_name": "Lou", "email": "lou@acme.example", "status": "active"}}}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
	var partners, links []map[string]any
	for i := range 1000 {
		id := fmt.Sprintf("p%04d", i)
		partners = append(partners, map[string]any{"id": id, "first_name": "Partner", "email": id + "@acme.example", "status": "active"})
		links = append(links, map[string]any{"id": "l" + id, "partner_id": id, "param": "ref", "value": "CODE" + id})
	}
	links = append(links, map[string]any{"id": "lp_new", "partner_id": "p_new", "param": "ref", "value": "IVY"},
		map[string]any{"id": "lp_link", "partner_id": "p_link", "param": "ref", "value": "LOU"})
	tolt.set("/partners", partners...)
	tolt.set("/links", links...)
	tolt.set("/promotion-codes")
	tolt.set("/customers",
		map[string]any{"id": "cus_1", "partner_id": "p0999", "customer_id": "u-1"},
		map[string]any{"id": "cus_2", "partner_id": "p_new", "customer_id": "u-2"},
		map[string]any{"id": "cus_3", "partner_id": "p_gone", "customer_id": "u-3"})
	tolt.set("/transactions")
	tolt.set("/commissions")
	srv := httptest.NewServer(tolt)
	defer srv.Close()
	e.integrations.Client.BaseURLs["tolt"] = srv.URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})
	imports := base + "/partner-programs/affiliates/imports"
	imp := owner.must(201, "POST", imports, map[string]any{})
	e.runCRMImports(t)
	var v map[string]any
	for range 1500 {
		v = owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
		if v["status"] == "done" || v["status"] == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if v["status"] != "done" {
		t.Fatalf("import = %v", v)
	}
	sum := v["summary"].(map[string]any)
	for _, k := range sum["kinds"].([]any) {
		m := k.(map[string]any)
		if m["kind"] == "partners" && (m["seen"] != 1002.0 || m["created"] != 1002.0) {
			t.Fatalf("partners = %v", m)
		}
		if m["kind"] == "customers" && (m["created"] != 2.0 || m["skipped"] != 1.0) {
			t.Fatalf("customers = %v", m)
		}
	}
	var members int
	var ivy string
	e.pool.QueryRow(ctx, `SELECT count(*), coalesce(max(status) FILTER (WHERE code = 'IVY'), '') FROM partner_memberships WHERE workspace_id = $1`, ws).Scan(&members, &ivy) //nolint:errcheck
	if members != 1002 || ivy != "pending" {
		t.Fatalf("members %d, the unlisted partner %q", members, ivy)
	}
	reasons := map[string]int{}
	for _, i := range sum["issues"].([]any) {
		reasons[i.(map[string]any)["reason"].(string)]++
	}
	if reasons["partner_unavailable"] != 1 || reasons["partner_missing"] != 1 {
		t.Fatalf("issues = %v", reasons)
	}
}

// TestPartnerImportHeldUntilSwitchOver: while Tolt still pays a program,
// commissions imported from it are neither approved after the hold nor put
// in a payout here; once it switches over they are.
func TestPartnerImportHeldUntilSwitchOver(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	tolt := &fakeTolt{data: map[string][]map[string]any{}}
	tolt.set("/programs", map[string]any{"id": "prg_1", "name": "Northpeak partners", "currency": "usd", "status": "active"})
	tolt.set("/links", map[string]any{"id": "l1", "partner_id": "p1", "param": "ref", "value": "ANA"})
	tolt.set("/partners", map[string]any{"id": "p1", "first_name": "Ana", "email": "ana@acme.example", "status": "active"})
	tolt.set("/promotion-codes")
	tolt.set("/customers", map[string]any{"id": "cus_1", "partner_id": "p1", "customer_id": "u-1"})
	tolt.set("/transactions", map[string]any{"id": "t1", "customer_id": "cus_1", "amount": "10000"})
	tolt.set("/commissions",
		map[string]any{"id": "m1", "partner_id": "p1", "customer_id": "cus_1", "transaction_id": "t1", "amount": "5000", "status": "approved"},
		map[string]any{"id": "m2", "partner_id": "p1", "customer_id": "cus_1", "amount": "3000", "status": "pending"})
	srv := httptest.NewServer(tolt)
	defer srv.Close()
	e.integrations.Client.BaseURLs["tolt"] = srv.URL
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate"})
	owner.must(200, "PATCH", base+"/partner-programs/affiliates", map[string]any{"auto_approve": true, "min_payout_cents": 0})
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "tolt", "credentials": map[string]string{"api_key": "tlt_test_not_a_real_key"}})
	imports := base + "/partner-programs/affiliates/imports"
	imp := owner.must(201, "POST", imports, map[string]any{})
	e.runCRMImports(t)
	for range 500 {
		v := owner.must(200, "GET", imports+"/"+imp["id"].(string), nil)
		if v["status"] == "done" {
			var k map[string]any
			for _, x := range v["summary"].(map[string]any)["kinds"].([]any) {
				if m := x.(map[string]any); m["kind"] == "partners" {
					k = m
				}
			}
			if k == nil || k["by_status"].(map[string]any)["active"] != 1.0 {
				t.Fatalf("partners = %v", k)
			}
			break
		}
		if v["status"] == "failed" {
			t.Fatalf("import = %v", v)
		}
		time.Sleep(20 * time.Millisecond)
	}
	prog := owner.must(200, "GET", base+"/partner-programs/affiliates", nil)
	if prog["imported_from"] != "tolt" || prog["switched_over_at"] != nil {
		t.Fatalf("program = %v %v", prog["imported_from"], prog["switched_over_at"])
	}
	if _, err := e.pool.Exec(ctx, `UPDATE partner_rewards SET hold_until = now() - interval '1 day' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	statuses := func() string {
		var s string
		e.pool.QueryRow(ctx, `SELECT string_agg(status || ':' || (payout_id IS NOT NULL)::text, ',' ORDER BY amount_cents) FROM partner_rewards WHERE workspace_id = $1`, ws).Scan(&s) //nolint:errcheck
		return s
	}
	if err := e.srv.ApproveDuePartnerRewards(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})["data"].([]any)); n != 0 {
		t.Fatalf("payouts before switch-over = %d", n)
	}
	if got := statuses(); got != "pending:false,approved:false" {
		t.Fatalf("before switch-over: %s", got)
	}
	sw := owner.must(200, "POST", base+"/partner-programs/affiliates/switch-over", nil)
	if sw["switched_over_at"] == nil {
		t.Fatalf("switch-over = %v", sw)
	}
	owner.must(409, "POST", base+"/partner-programs/affiliates/switch-over", nil)
	if err := e.srv.ApproveDuePartnerRewards(ctx); err != nil {
		t.Fatal(err)
	}
	// Tolt gave no payout details, so Ana's payout waits until she adds them.
	if res := owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"}); len(res["data"].([]any)) != 0 || res["waiting_for_details"] != 1.0 {
		t.Fatalf("payouts without details = %v", res)
	}
	ana := owner.must(200, "GET", base+"/partners?program=affiliates", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(200, "PATCH", base+"/partners/"+ana, map[string]any{"payout_method": "PayPal ana@acme.example"})
	if n := len(owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": "affiliates"})["data"].([]any)); n != 1 {
		t.Fatalf("payouts after switch-over = %d", n)
	}
	if got := statuses(); got != "approved:true,approved:true" {
		t.Fatalf("after switch-over: %s", got)
	}
}
