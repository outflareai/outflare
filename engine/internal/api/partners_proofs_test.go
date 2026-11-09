package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

// TestPartnerRewardKeepsItsRule: a reward keeps the rule that priced it,
// with the rate it paid, after the program's rules are saved again.
func TestPartnerRewardKeepsItsRule(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/", "hold_days": 0})
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@example.com", "code": "ana"})["id"].(string)
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"name": "Base rate", "trigger": "sale", "kind": "percent", "percent_bp": 2000,
			"overrides": []any{map[string]any{"partner_id": ana, "percent_bp": 3000}}},
	}})
	sale := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-1", "ref": "ana", "amount_cents": 10000, "external_id": "s-1"})
	reward := sale["rewards"].([]any)[0].(map[string]any)
	if reward["amount_cents"] != float64(3000) || reward["rule_id"] == nil {
		t.Fatalf("reward = %v", reward)
	}
	ruleID := reward["rule_id"].(string)

	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"name": "New rate", "trigger": "sale", "kind": "fixed", "amount_cents": 500},
	}})
	var gone *string
	var rule map[string]any
	if err := e.pool.QueryRow(ctx, `SELECT rule_id::text, rule FROM partner_rewards WHERE id = $1`, reward["id"]).Scan(&gone, &rule); err != nil {
		t.Fatal(err)
	}
	if gone != nil {
		t.Fatalf("rule_id survived the rules being replaced: %v", *gone)
	}
	if rule["id"] != ruleID || rule["name"] != "Base rate" || rule["trigger"] != "sale" || rule["side"] != "partner" || rule["kind"] != "percent" ||
		rule["percent_bp"] != float64(3000) || rule["amount_cents"] != nil || rule["recurrence"] != "every" || rule["partner_id"] != nil {
		t.Fatalf("rule snapshot = %v", rule)
	}

	// A fee no rule priced has none.
	fee := owner.must(201, "POST", base+"/partners/"+ana+"/fees", map[string]any{"amount_cents": 100, "note": "video"})["id"].(string)
	var none *string
	if err := e.pool.QueryRow(ctx, `SELECT rule::text FROM partner_rewards WHERE id = $1`, fee).Scan(&none); err != nil || none != nil {
		t.Fatalf("fee rule = %v, %v", none, err)
	}
}

// TestPartnerReferralEvidence: a referral keeps the website visit or the
// promo code that credited it.
func TestPartnerReferralEvidence(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "ana@example.com", "code": "ana"})
	bo := owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "bo@example.com", "code": "bo"})["id"].(string)
	owner.must(201, "POST", base+"/partners/"+bo+"/promo-codes", map[string]any{"code": "BO20"})

	friend := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Fern Friend", "identities": []any{
		map[string]any{"namespace": "user_id", "value": "f-1"}, map[string]any{"namespace": "email", "value": "fern@example.com"}}})
	var older, session string
	for i, days := range []int{9, 2} {
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, last_seen_at, ref, landing_url)
			VALUES ($1, $2, 'v1', now() - make_interval(days => $3), now() - make_interval(days => $3), 'ana', 'https://northpeak.example/') RETURNING id::text`,
			ws, friend["id"], days).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			older = id
		} else {
			session = id
		}
	}
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "f-1", "amount_cents": 2000, "external_id": "o-1"})
	var via, promo string
	var got *string
	if err := e.pool.QueryRow(ctx, `SELECT via, session_id::text, promo_code FROM partner_referrals WHERE record_id = $1`, friend["id"]).Scan(&via, &got, &promo); err != nil {
		t.Fatal(err)
	}
	if via != "visit" || got == nil || *got != session || *got == older || promo != "" {
		t.Fatalf("visit referral = %s %v %q (last visit %s)", via, got, promo, session)
	}

	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-2", "email": "cara@example.com", "promo_code": "bo20", "amount_cents": 2000, "external_id": "o-2"})
	if err := e.pool.QueryRow(ctx, `SELECT f.via, f.session_id::text, f.promo_code FROM partner_referrals f JOIN record_identities i ON i.record_id = f.record_id
		WHERE f.workspace_id = $1 AND i.namespace = 'user_id' AND i.value = 'c-2'`, ws).Scan(&via, &got, &promo); err != nil {
		t.Fatal(err)
	}
	if via != "promo_code" || got != nil || promo != "BO20" {
		t.Fatalf("promo referral = %s %v %q", via, got, promo)
	}

	refs := map[string]map[string]any{}
	for _, x := range owner.must(200, "GET", base+"/partner-referrals", nil)["data"].([]any) {
		refs[x.(map[string]any)["record_id"].(string)] = x.(map[string]any)
	}
	if f := refs[friend["id"].(string)]; f["customer_name"] != "Fern Friend" || f["customer_email"] != "fern@example.com" || f["via"] != "visit" || f["promo_code"] != "" {
		t.Fatalf("visit referral row = %v", f)
	}
	if len(refs) != 2 {
		t.Fatalf("referrals = %v", refs)
	}
	for id, f := range refs {
		if id != friend["id"] && (f["customer_name"] != "" || f["customer_email"] != "cara@example.com" || f["via"] != "promo_code" || f["promo_code"] != "BO20") {
			t.Fatalf("promo referral row = %v", f)
		}
	}

	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-3", "ref": "ana", "amount_cents": 2000, "external_id": "o-3"})
	if err := e.pool.QueryRow(ctx, `SELECT f.via, f.promo_code FROM partner_referrals f JOIN record_identities i ON i.record_id = f.record_id
		WHERE f.workspace_id = $1 AND i.namespace = 'user_id' AND i.value = 'c-3'`, ws).Scan(&via, &promo); err != nil || via != "ref" || promo != "" {
		t.Fatalf("ref referral = %s %q, %v", via, promo, err)
	}
}

// TestPartnerSearchAndSort: q and sort cover every partner the caller can
// see, not one page, and every sort pages without skipping or repeating.
func TestPartnerSearchAndSort(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	alpha := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	inAlpha, inBravo := "/v1/brands/"+alpha, "/v1/brands/"+bravo
	owner.must(201, "POST", inAlpha+"/partner-programs", map[string]any{"name": "Alpha partners", "slug": "alpha-partners", "kind": "affiliate"})
	owner.must(201, "POST", inBravo+"/partner-programs", map[string]any{"name": "Bravo partners", "slug": "bravo-partners", "kind": "affiliate"})
	other := owner.must(201, "POST", inBravo+"/partners", map[string]any{"program": "bravo-partners", "email": "olive.twin@acme.example", "name": "Olive Twin", "code": "oldie-twin"})

	oldest := owner.must(201, "POST", inAlpha+"/partners", map[string]any{"program": "alpha-partners", "email": "olive@acme.example", "name": "Olive Oldham", "code": "oldie"})
	oldestID := oldest["id"].(string)
	owner.must(201, "POST", inAlpha+"/partners/"+oldestID+"/promo-codes", map[string]any{"code": "SPRING_50"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO record_identities (workspace_id, record_id, namespace, value) VALUES ($1, $2, 'email', 'olive.work@northpeak.example')`,
		ws, oldest["record_id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO partner_import_refs (workspace_id, program_id, connector, kind, external_id, local_id)
		VALUES ($1, $2, 'tolt', 'partner', 'part_imported_7', $3)`, ws, oldest["program_id"], oldestID); err != nil {
		t.Fatal(err)
	}
	balances := map[string]int{"ana": 3000, "bo": 100, "cy": 5000, "dee": 0, "eve": 700, "fay": 0}
	for _, code := range []string{"ana", "bo", "cy", "dee", "eve", "fay"} {
		p := owner.must(201, "POST", inAlpha+"/partners", map[string]any{"program": "alpha-partners", "email": code + "@example.com", "name": strings.ToUpper(code[:1]) + code[1:] + " Partner", "code": code})
		if c := balances[code]; c > 0 {
			owner.must(201, "POST", inAlpha+"/partners/"+p["id"].(string)+"/fees", map[string]any{"amount_cents": c, "note": "video"})
		}
	}
	if _, err := e.pool.Exec(ctx, `UPDATE partner_memberships SET created_at = created_at - interval '400 days' WHERE id = $1`, oldestID); err != nil {
		t.Fatal(err)
	}

	pages := func(query string) []map[string]any {
		t.Helper()
		var out []map[string]any
		seen := map[string]bool{}
		for cursor, n := "", 0; ; n++ {
			page := owner.must(200, "GET", inAlpha+"/partners?limit=2&"+query+"&cursor="+url.QueryEscape(cursor), nil)
			for _, x := range page["data"].([]any) {
				p := x.(map[string]any)
				if seen[p["id"].(string)] {
					t.Fatalf("%s: %s twice", query, p["code"])
				}
				seen[p["id"].(string)] = true
				out = append(out, p)
			}
			cursor, _ = page["next_cursor"].(string)
			if cursor == "" || n > 10 {
				return out
			}
		}
	}
	codes := func(list []map[string]any) string {
		var cs []string
		for _, p := range list {
			cs = append(cs, p["code"].(string))
		}
		return strings.Join(cs, ",")
	}
	for sort, want := range map[string]string{
		"newest":  "fay,eve,dee,cy,bo,ana,oldie",
		"oldest":  "oldie,ana,bo,cy,dee,eve,fay",
		"code":    "ana,bo,cy,dee,eve,fay,oldie",
		"name":    "ana,bo,cy,dee,eve,fay,oldie",
		"balance": "cy,ana,eve,bo",
	} {
		got := codes(pages("sort=" + sort))
		if !strings.HasPrefix(got, want) || len(strings.Split(got, ",")) != 7 {
			t.Errorf("sort=%s: %s, want %s first of 7", sort, got, want)
		}
	}
	for _, sort := range []string{"clicks", "customers", "sales", "paid"} {
		if n := len(pages("sort=" + sort)); n != 7 {
			t.Errorf("sort=%s paged %d partners, want 7", sort, n)
		}
	}
	owner.must(400, "GET", inAlpha+"/partners?sort=random", nil)
	owner.must(400, "GET", inAlpha+"/partners?q="+strings.Repeat("x", 201), nil)

	for _, q := range []string{"oldie", "OLDIE", "olive@acme", "Oldham", "olive.work@northpeak", "spring_50", "part_imported_7", oldestID, oldest["record_id"].(string)} {
		got := pages("q=" + url.QueryEscape(q))
		if codes(got) != "oldie" {
			t.Errorf("q=%s found %s", q, codes(got))
		}
	}
	for _, q := range []string{"%", "o_d", "oldie-twin", other["id"].(string)} {
		if got := pages("q=" + url.QueryEscape(q)); len(got) != 0 {
			t.Errorf("q=%s found %s", q, codes(got))
		}
	}
	if got := pages("q=partner&sort=code"); codes(got) != "ana,bo,cy,dee,eve,fay" {
		t.Errorf("q=partner: %s", codes(got))
	}
	if got := owner.must(200, "GET", inBravo+"/partners?q=oldie", nil)["data"].([]any); len(got) != 1 || got[0].(map[string]any)["id"] != other["id"] {
		t.Errorf("bravo q=oldie: %v", got)
	}
}

// TestPartnerActivity: a partner's history names who did what, stays in
// the partner's brand, and never shows payout details.
func TestPartnerActivity(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	alpha := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Alpha", "handle": "alpha"})["id"].(string)
	bravo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	inAlpha, inBravo := "/v1/brands/"+alpha, "/v1/brands/"+bravo
	owner.must(201, "POST", inAlpha+"/partner-programs", map[string]any{"name": "Alpha partners", "slug": "alpha-partners", "kind": "affiliate"})
	owner.must(201, "POST", inBravo+"/partner-programs", map[string]any{"name": "Bravo partners", "slug": "bravo-partners", "kind": "affiliate"})
	ana := owner.must(201, "POST", inAlpha+"/partners", map[string]any{"program": "alpha-partners", "email": "ana@example.com", "code": "ana",
		"payout_method": "PayPal: first-secret-payout@example.com"})["id"].(string)
	bo := owner.must(201, "POST", inBravo+"/partners", map[string]any{"program": "bravo-partners", "email": "bo@example.com", "code": "bo"})["id"].(string)

	owner.must(200, "PATCH", inAlpha+"/partners/"+ana, map[string]any{"status": "suspended", "payout_method": "Wise: second-secret-payout", "note": "checked"})
	owner.must(200, "PATCH", inAlpha+"/partners/"+ana, map[string]any{"status": "active"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO audit_events (workspace_id, actor_type, action, target_type, target_id, data)
		VALUES ($1, 'system', 'partner.portal_updated', 'partner', $2, '{"payout_method": true, "payout_type": false, "postback_url": false, "notify": true}')`, ws, ana); err != nil {
		t.Fatal(err)
	}
	sale := owner.must(201, "POST", inAlpha+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-1", "ref": "ana", "amount_cents": 30000, "external_id": "s-1"})
	reward := sale["rewards"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(200, "POST", inAlpha+"/partner-rewards/"+reward+"/approve", map[string]any{})
	payout := owner.must(200, "POST", inAlpha+"/partner-payouts", map[string]any{})["data"].([]any)[0].(map[string]any)["id"].(string)
	owner.must(200, "POST", inAlpha+"/partner-payouts/"+payout+"/paid", map[string]any{"reference": "WISE-123", "note": "sent"})
	owner.must(201, "POST", inBravo+"/partners/"+bo+"/fees", map[string]any{"amount_cents": 500, "note": "video"})

	page := owner.must(200, "GET", inAlpha+"/partners/"+ana+"/activity", nil)
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "secret-payout") {
		t.Fatalf("payout details in activity: %s", raw)
	}
	rows := page["data"].([]any)
	byAction := map[string]map[string]any{}
	var actions []string
	for _, x := range rows {
		row := x.(map[string]any)
		actions = append(actions, row["action"].(string))
		if byAction[row["action"].(string)] == nil {
			byAction[row["action"].(string)] = row
		}
	}
	want := "partner_payout.paid,partner_payout.created,partner_reward.approved,partner_reward.created,partner_referral.created,partner.portal_updated,partner.updated,partner.updated,partner.created"
	if strings.Join(actions, ",") != want {
		t.Fatalf("actions = %s", strings.Join(actions, ","))
	}
	approved := byAction["partner_reward.approved"]
	if approved["actor_type"] != "human" || approved["actor_name"] != "Jack" || approved["target_id"] != reward ||
		approved["data"].(map[string]any)["by"] != "manual" || approved["data"].(map[string]any)["amount_cents"] != float64(6000) {
		t.Fatalf("approval = %v", approved)
	}
	if d := approved["data"].(map[string]any); d["partner_id"] != nil || d["recipient_id"] != nil || d["external_id"] != nil {
		t.Fatalf("approval data leaks unlisted keys: %v", d)
	}
	if d := byAction["partner_payout.paid"]["data"].(map[string]any); d["reference"] != "WISE-123" || len(d["reward_ids"].([]any)) != 1 {
		t.Fatalf("paid = %v", d)
	}
	if d := byAction["partner.portal_updated"]["data"].(map[string]any); fmt.Sprint(d["changed"]) != "[payout_method notify]" {
		t.Fatalf("portal update = %v", d)
	}
	first := rows[len(rows)-2].(map[string]any)["data"].(map[string]any)
	if fmt.Sprint(first["changed"]) != "[status note payout_method]" || first["status"] != "suspended" || first["previous_status"] != "active" {
		t.Fatalf("first update = %v", first)
	}
	if d := byAction["partner.created"]["data"].(map[string]any); d["status"] != "active" || byAction["partner.created"]["actor_name"] != "Jack" {
		t.Fatalf("created = %v", byAction["partner.created"])
	}

	one := owner.must(200, "GET", inAlpha+"/partners/"+ana+"/activity?limit=4", nil)
	if len(one["data"].([]any)) != 4 || one["next_cursor"] == nil {
		t.Fatalf("first page = %v", one)
	}
	rest := owner.must(200, "GET", inAlpha+"/partners/"+ana+"/activity?limit=100&cursor="+url.QueryEscape(one["next_cursor"].(string)), nil)
	if n := len(rest["data"].([]any)); n != len(rows)-4 || rest["next_cursor"] != nil || rest["data"].([]any)[0].(map[string]any)["id"] != rows[4].(map[string]any)["id"] {
		t.Fatalf("second page = %v", rest)
	}

	owner.must(404, "GET", inAlpha+"/partners/"+bo+"/activity", nil)
	if got := owner.must(200, "GET", inBravo+"/partners/"+bo+"/activity", nil)["data"].([]any); len(got) != 3 {
		t.Fatalf("bravo partner activity = %v", got)
	}
	owner.must(400, "GET", inAlpha+"/partners/"+ana+"/activity?cursor=bm90LW91cnM", nil)
}
