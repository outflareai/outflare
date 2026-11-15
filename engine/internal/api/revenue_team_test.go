package api_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// The sales team's quarter adds up per person and per manager, and a
// commission plan's rules pay each person line by line: the first rule
// that applies, tiers by share of quota, a refund taken back, a manager's
// override, and an approved month that no longer moves.
func TestRevenueTeamAndCommissions(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	num := func(m map[string]any, pairs ...any) {
		t.Helper()
		for i := 0; i < len(pairs); i += 2 {
			if m[pairs[i].(string)] != float64(pairs[i+1].(int)) {
				t.Fatalf("%s = %v, want %v (%v)", pairs[i], m[pairs[i].(string)], pairs[i+1], m)
			}
		}
	}
	captured := track.Capture(t)
	has := func(name string) bool {
		for _, ev := range captured() {
			if ev.Name == name {
				return true
			}
		}
		return false
	}

	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "bea@example.com", "role": "viewer"})
	bea := &client{t: t, h: e.h}
	beaID := bea.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Bea", "password": "beas long password"})["user_id"].(string)

	// The directory: Jo manages Bea, an account executive.
	jo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Jo", "title": "Sales manager", "user_id": me})["id"].(string)
	beaStaff := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Bea", "title": "Account Executive", "user_id": beaID, "manager_id": jo})["id"].(string)

	// Nobody is on the team until someone has a quota, numbers or a plan.
	team := owner.must(200, "GET", base+"/revenue/team", nil)
	if len(team["members"].([]any)) != 0 || team["can_manage"] != true {
		t.Fatalf("empty team: %v", team)
	}
	q := team["quarter"].(string)

	// Acme is Bea's. A 10,000 deal with it is won today; Jo sourced it.
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "attributes": map[string]any{"owner": beaID}})["id"].(string)
	deal := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme seats",
		"attributes": map[string]any{"associated_company": acme, "sourced_by": me, "value": map[string]any{"amount_minor": 1000000, "currency": "USD"}}})["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+deal, map[string]any{"attributes": map[string]any{"stage": "won"}})
	// Acme pays 49.00 and gets 10.00 back.
	exec(`INSERT INTO transactions (workspace_id, brand_id, record_id, kind, amount_minor, brand_amount_minor, source, source_id, occurred_at) VALUES
		($1, $2, $3, 'payment', 4900, 4900, 'stripe', 'in_1', now()), ($1, $2, $3, 'refund', 1000, 1000, 'stripe', 're_1', now())`, ws, brand, acme)

	// Bea's quota is 20,000: she is at 50 percent.
	owner.must(400, "PUT", base+"/revenue/team/quotas", map[string]any{"quarter": "soon", "quotas": []map[string]any{{"staff_id": beaStaff, "target_minor": 1}}})
	owner.must(400, "PUT", base+"/revenue/team/quotas", map[string]any{"quarter": q, "quotas": []map[string]any{{"staff_id": acme, "target_minor": 1}}})
	owner.must(200, "PUT", base+"/revenue/team/quotas", map[string]any{"quarter": q, "quotas": []map[string]any{{"staff_id": beaStaff, "target_minor": 2000000}}})
	if !has("sales_quota.updated") {
		t.Fatal("no sales_quota.updated event")
	}
	team = owner.must(200, "GET", base+"/revenue/team?quarter="+q, nil)
	members := team["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members: %v", members)
	}
	byName := map[string]map[string]any{}
	for _, m := range members {
		byName[m.(map[string]any)["name"].(string)] = m.(map[string]any)
	}
	num(byName["Bea"], "quota_minor", 2000000, "won_minor", 1000000, "forecast_minor", 1000000, "attainment", 50, "reports", 0)
	if byName["Bea"]["team"] != nil || byName["Bea"]["manager_id"] != jo {
		t.Fatalf("Bea: %v", byName["Bea"])
	}
	num(byName["Jo"], "won_minor", 0, "reports", 1)
	num(byName["Jo"]["team"].(map[string]any), "quota_minor", 2000000, "won_minor", 1000000, "attainment", 50)
	num(team["total"].(map[string]any), "won_minor", 1000000, "quota_minor", 2000000)
	num(team["unassigned"].(map[string]any), "won_minor", 0)
	// Bea sees herself; she cannot set quotas.
	hers := bea.must(200, "GET", base+"/revenue/team", nil)
	if m := hers["members"].([]any); len(m) != 1 || m[0].(map[string]any)["name"] != "Bea" || hers["total"] != nil || hers["can_manage"] != false {
		t.Fatalf("Bea's view: %v", hers)
	}
	bea.must(403, "PUT", base+"/revenue/team/quotas", map[string]any{"quarter": q, "quotas": []map[string]any{{"staff_id": beaStaff, "target_minor": 1}}})

	// A plan for account executives: big deals first, then every deal with
	// a better rate from half of quota, and money collected with clawback.
	rules := []map[string]any{
		{"id": "big", "name": "Big deals", "pays_on": "deal_won", "who": "owner", "percent": 15,
			"conditions": []map[string]any{{"attribute": "amount", "op": "gt", "value": 50000}}},
		{"id": "standard", "name": "Every deal", "pays_on": "deal_won", "who": "owner", "percent": 10,
			"tiers": []map[string]any{{"from_attainment": 50, "percent": 12}, {"from_attainment": 100, "percent": 20}}},
		{"id": "collected", "name": "First 30 days, inbound", "pays_on": "payment", "who": "owner", "percent": 5, "clawback": true, "days_to": 30, "source": "inbound", "customer": "new"},
		{"id": "later", "name": "After 30 days", "pays_on": "payment", "who": "owner", "percent": 1, "days_from": 31},
	}
	owner.must(400, "POST", base+"/commission-plans", map[string]any{"name": "Bad", "rules": []map[string]any{{"id": "x", "name": "X", "pays_on": "deal_won", "who": "nobody", "percent": 5}}})
	owner.must(400, "POST", base+"/commission-plans", map[string]any{"name": "Bad", "positions": []string{"role:chief"}})
	bea.must(403, "POST", base+"/commission-plans", map[string]any{"name": "Mine"})
	plan := owner.must(201, "POST", base+"/commission-plans", map[string]any{"name": "Account executives", "positions": []string{"account executive"}, "rules": rules})
	if plan["status"] != "draft" || len(plan["rules"].([]any)) != 4 || plan["lapse_days"] != float64(90) || !has("commission_plan.created") {
		t.Fatalf("plan: %v", plan)
	}
	planID := plan["id"].(string)
	month := time.Now().UTC().Format("2006-01")
	// A draft earns nothing.
	if got := owner.must(200, "GET", base+"/commissions", nil); len(got["statements"].([]any)) != 0 || got["month"] != month {
		t.Fatalf("draft plan: %v", got)
	}
	owner.must(200, "PATCH", base+"/commission-plans/"+planID, map[string]any{"status": "active"})
	// Jo's plan: 500.00 for a deal she sourced, and 2 percent of what her
	// reports win, on top.
	owner.must(201, "POST", base+"/commission-plans", map[string]any{"name": "Managers", "status": "active", "staff_ids": []string{jo}, "rules": []map[string]any{
		{"id": "sourced", "name": "Sourced a deal", "pays_on": "deal_won", "who": "sourced_by", "flat_minor": 50000},
		{"id": "override", "name": "Team override", "pays_on": "deal_won", "who": "manager", "percent": 2, "stack": true},
	}})
	if n := len(owner.must(200, "GET", base+"/commission-plans", nil)["data"].([]any)); n != 2 {
		t.Fatalf("plans: %d", n)
	}

	got := owner.must(200, "GET", base+"/commissions?month="+month, nil)
	sts := map[string]map[string]any{}
	for _, s := range got["statements"].([]any) {
		sts[s.(map[string]any)["name"].(string)] = s.(map[string]any)
	}
	if len(sts) != 2 || got["currency"] != "USD" {
		t.Fatalf("statements: %v", got)
	}
	// Bea: 12 percent of 10,000 (the tier from 50 percent of quota), 5
	// percent of 49.00, less 5 percent of the 10.00 refunded.
	num(sts["Bea"], "total_minor", 120000+245-50, "attainment", 50, "quota_minor", 2000000)
	lines := sts["Bea"]["lines"].([]any)
	if len(lines) != 3 || sts["Bea"]["status"] != "open" {
		t.Fatalf("Bea's lines: %v", lines)
	}
	dealLine := lines[0].(map[string]any)
	for _, l := range lines {
		if l.(map[string]any)["event"].(map[string]any)["kind"] == "deal" {
			dealLine = l.(map[string]any)
		}
	}
	if dealLine["rule_id"] != "standard" || dealLine["rule_name"] != "Every deal" || dealLine["percent"] != float64(12) || dealLine["tier_from"] != float64(50) ||
		dealLine["plan_name"] != "Account executives" || dealLine["event"].(map[string]any)["deal_kind"] != "new" || dealLine["event"].(map[string]any)["record_name"] != "Acme" {
		t.Fatalf("deal line: %v", dealLine)
	}
	// Jo: 500.00 for sourcing it and 2 percent of 10,000 as Bea's manager.
	num(sts["Jo"], "total_minor", 50000+20000)
	num(got, "total_minor", 120000+245-50+50000+20000)
	// Bea sees only her own.
	if hers := bea.must(200, "GET", base+"/commissions", nil); len(hers["statements"].([]any)) != 1 || hers["total_minor"] != float64(120000+245-50) || hers["can_manage"] != false {
		t.Fatalf("Bea's commissions: %v", hers)
	}
	owner.must(400, "GET", base+"/commissions?month=2026-13", nil)

	// Approved, the month keeps its lines whatever the plan becomes.
	bea.must(403, "PUT", base+"/commissions/"+beaStaff, map[string]any{"month": month, "status": "approved"})
	owner.must(404, "PUT", base+"/commissions/"+acme, map[string]any{"month": month, "status": "approved"})
	st := owner.must(200, "PUT", base+"/commissions/"+beaStaff, map[string]any{"month": month, "status": "approved"})
	if st["status"] != "approved" || st["approved_at"] == nil || st["paid_at"] != nil || !has("commission.approved") {
		t.Fatalf("approved: %v", st)
	}
	rules[1]["tiers"] = []map[string]any{}
	owner.must(200, "PATCH", base+"/commission-plans/"+planID, map[string]any{"rules": rules})
	read := func(name string) map[string]any {
		t.Helper()
		for _, s := range owner.must(200, "GET", base+"/commissions?month="+month, nil)["statements"].([]any) {
			if s.(map[string]any)["name"] == name {
				return s.(map[string]any)
			}
		}
		t.Fatalf("no statement for %s", name)
		return nil
	}
	num(read("Bea"), "total_minor", 120000+245-50)
	if st := owner.must(200, "PUT", base+"/commissions/"+beaStaff, map[string]any{"month": month, "status": "paid"}); st["status"] != "paid" || st["paid_at"] == nil {
		t.Fatalf("paid: %v", st)
	}
	// Reopened, it is worked out again: 10 percent now.
	if st := owner.must(200, "PUT", base+"/commissions/"+beaStaff, map[string]any{"month": month, "status": "open"}); st["status"] != "open" || st["total_minor"] != float64(100000+245-50) {
		t.Fatalf("reopened: %v", st)
	}
	if !has("commission.paid") || !has("commission.reopened") || !has("commission_plan.updated") {
		t.Fatal("missing commission events")
	}

	owner.must(204, "DELETE", base+"/commission-plans/"+planID, nil)
	owner.must(404, "DELETE", base+"/commission-plans/"+planID, nil)
	if !has("commission_plan.deleted") {
		t.Fatal("no commission_plan.deleted event")
	}
	if got := owner.must(200, "GET", base+"/commissions", nil); len(got["statements"].([]any)) != 1 {
		t.Fatalf("after deleting the plan: %v", got)
	}
}

// Someone in the team directory with no sign-in can own records: their
// deals count on the sales team page, and keep counting for them once they
// get a sign-in.
func TestOwnerWithoutSignIn(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	sam := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Sam", "title": "Account Executive"})["id"].(string)
	linked := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Jo", "user_id": me})["id"].(string)

	// A directory id only stands for someone with no sign-in, and a
	// stranger's id is refused.
	owner.must(400, "POST", base+"/records", map[string]any{"type": "account", "name": "Nope", "attributes": map[string]any{"owner": linked}})
	owner.must(400, "POST", base+"/records", map[string]any{"type": "account", "name": "Nope", "attributes": map[string]any{"owner": "00000000-0000-4000-8000-000000000009"}})

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme", "attributes": map[string]any{"owner": sam}})["id"].(string)
	sum := owner.must(200, "GET", base+"/records/"+acme+"/summary", nil)
	if o, _ := sum["owner"].(map[string]any); o == nil || o["name"] != "Sam" {
		t.Fatalf("summary owner: %v", sum["owner"])
	}
	deal := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme seats",
		"attributes": map[string]any{"associated_company": acme, "value": map[string]any{"amount_minor": 500000, "currency": "USD"}}})["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+deal, map[string]any{"attributes": map[string]any{"stage": "won"}})

	won := func() float64 {
		t.Helper()
		team := owner.must(200, "GET", base+"/revenue/team", nil)
		for _, m := range team["members"].([]any) {
			if m := m.(map[string]any); m["staff_id"] == sam {
				return m["won_minor"].(float64)
			}
		}
		t.Fatalf("Sam is not on the team: %v", team["members"])
		return 0
	}
	if got := won(); got != 500000 {
		t.Fatalf("Sam won %v, want 500000", got)
	}

	// Sam gets a sign-in: what they owned still counts for them.
	inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": "sam@example.com", "role": "viewer"})
	samUser := (&client{t: t, h: e.h}).must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Sam", "password": "sams long password"})["user_id"].(string)
	owner.must(200, "PATCH", "/v1/workspaces/"+ws+"/staff/"+sam, map[string]any{"user_id": samUser})
	if got := won(); got != 500000 {
		t.Fatalf("with a sign-in Sam won %v, want 500000", got)
	}
}

// A deal with a second owner is split between the two: for what each has
// won, and for what each earns on it.
func TestDealWithTwoOwners(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	sam := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Sam", "title": "Account Executive"})["id"].(string)
	kim := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Kim", "title": "Account Executive"})["id"].(string)
	owner.must(201, "POST", base+"/commission-plans", map[string]any{"name": "Reps", "status": "active", "staff_ids": []string{sam, kim}, "rules": []map[string]any{
		{"id": "won", "name": "Deals won", "pays_on": "deal_won", "who": "owner", "percent": 10},
	}})
	deal := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme seats",
		"attributes": map[string]any{"owner": sam, "co_owner": kim, "co_owner_percent": 30, "value": map[string]any{"amount_minor": 1000000, "currency": "USD"}}})["id"].(string)
	owner.must(200, "PATCH", base+"/records/"+deal, map[string]any{"attributes": map[string]any{"stage": "won"}})

	team := owner.must(200, "GET", base+"/revenue/team", nil)
	won := map[string]float64{}
	for _, m := range team["members"].([]any) {
		m := m.(map[string]any)
		won[m["staff_id"].(string)] = m["won_minor"].(float64)
	}
	if won[sam] != 700000 || won[kim] != 300000 {
		t.Fatalf("won: Sam %v, Kim %v; want 700000 and 300000", won[sam], won[kim])
	}

	got := owner.must(200, "GET", base+"/commissions", nil)
	for _, s := range got["statements"].([]any) {
		s := s.(map[string]any)
		want, split := 70000.0, 70.0
		if s["staff_id"] == kim {
			want, split = 30000, 30
		}
		lines := s["lines"].([]any)
		if s["total_minor"] != want || len(lines) != 1 || lines[0].(map[string]any)["split_percent"] != split {
			t.Fatalf("%v earns %v on %v, want %v at %v percent", s["name"], s["total_minor"], lines, want, split)
		}
	}
	if n := len(got["statements"].([]any)); n != 2 {
		t.Fatalf("statements: %d", n)
	}

	code, ct, body := owner.text(base + "/commissions?format=csv")
	if code != 200 || !strings.HasPrefix(ct, "text/csv") || !strings.Contains(body, "Kim,Account Executive,open") || !strings.Contains(body, ",10,30,300.00,USD") {
		t.Fatalf("csv: %d %s\n%s", code, ct, body)
	}
	owner.must(400, "GET", base+"/commissions?format=pdf", nil)
}

// The team page says how each person handles the signals given to them.
func TestTeamSignals(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	me := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	jo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/staff", map[string]any{"name": "Jo", "user_id": me})["id"].(string)
	q := owner.must(200, "GET", base+"/revenue/team", nil)["quarter"]
	owner.must(200, "PUT", base+"/revenue/team/quotas", map[string]any{"quarter": q, "quotas": []map[string]any{{"staff_id": jo, "target_minor": 100000}}})
	owner.must(200, "GET", base+"/signal-types", nil)
	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	globex := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Globex"})["id"].(string)
	first := owner.must(201, "POST", base+"/signals", map[string]any{"record_id": acme, "type": "upsell"})["id"].(string)
	second := owner.must(201, "POST", base+"/signals", map[string]any{"record_id": globex, "type": "upsell"})["id"].(string)
	owner.must(200, "PATCH", base+"/signals/"+first, map[string]any{"status": "accepted"})
	owner.must(200, "PATCH", base+"/signals/"+first, map[string]any{"status": "done", "outcome": "won"})
	owner.must(200, "PATCH", base+"/signals/"+second, map[string]any{"status": "accepted"})

	team := owner.must(200, "GET", base+"/revenue/team", nil)
	for _, m := range team["members"].([]any) {
		if m := m.(map[string]any); m["staff_id"] == jo {
			sg := m["signals"].(map[string]any)
			if sg["open"] != 1.0 || sg["late"] != 0.0 || sg["handled"] != 1.0 || sg["won"] != 1.0 || sg["median_hours"] == nil {
				t.Fatalf("signals: %v", sg)
			}
			return
		}
	}
	t.Fatalf("Jo is not on the team: %v", team["members"])
}
