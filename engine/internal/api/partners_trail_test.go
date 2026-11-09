package api_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// trailEnv is a brand with an affiliate program, for the reward proof tests.
type trailEnv struct {
	e       *env
	owner   *client
	ws      string
	base    string
	program string
}

func newTrailEnv(t *testing.T) *trailEnv {
	t.Helper()
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	brand := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	base := "/v1/brands/" + brand
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "slug": "affiliates", "kind": "affiliate",
		"landing_url": "https://northpeak.example/", "hold_days": 0, "auto_approve": false})["id"].(string)
	owner.must(200, "PUT", base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"name": "Base rate", "trigger": "sale", "kind": "percent", "percent_bp": 2000}}})
	return &trailEnv{e: e, owner: owner, ws: ws, base: base, program: prog}
}

func (te *trailEnv) sale(body map[string]any) string {
	return te.owner.must(201, "POST", te.base+"/partner-conversions", body)["rewards"].([]any)[0].(map[string]any)["id"].(string)
}

func (te *trailEnv) proof(id string) map[string]any {
	return te.owner.must(200, "GET", te.base+"/partner-rewards/"+id+"/proof", nil)
}

// gaps is a proof's gaps as step: reason.
func gaps(proof map[string]any) map[string]string {
	out := map[string]string{}
	for _, x := range proof["gaps"].([]any) {
		g := x.(map[string]any)
		if out[g["step"].(string)] != "" || g["message"] == "" {
			panic(fmt.Sprintf("gap %v", g))
		}
		out[g["step"].(string)] = g["reason"].(string)
	}
	return out
}

func (te *trailEnv) partner(code, name string) string {
	return te.owner.must(201, "POST", te.base+"/partners", map[string]any{"program": "affiliates", "email": code + "@example.com", "name": name, "code": code})["id"].(string)
}

// imported writes what a Tolt import writes for one commission: the
// partner's, customer's and commission's source ids, an imported sale and
// its reward. It returns the reward's id.
func (te *trailEnv) imported(t *testing.T, partner, n, email, status string, cents int64) string {
	t.Helper()
	ctx := context.Background()
	rec := te.owner.must(201, "POST", te.base+"/records", map[string]any{"type": "person", "name": "Imported " + n,
		"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
	var reward string
	err := te.e.pool.QueryRow(ctx, `WITH f AS (
			INSERT INTO partner_referrals (workspace_id, program_id, membership_id, record_id, via, status, first_sale_at, created_at)
			VALUES ($1, $2, $3, $4, 'import', 'customer', now() - interval '20 days', now() - interval '30 days') RETURNING id),
		v AS (
			INSERT INTO partner_conversions (workspace_id, program_id, referral_id, record_id, type, external_id, amount_cents, currency, source, occurred_at)
			SELECT $1, $2, f.id, $4, 'sale', 'tolt:txn_' || $5, $6 * 5, 'USD', 'import', now() - interval '20 days' FROM f RETURNING id),
		w AS (
			INSERT INTO partner_rewards (workspace_id, program_id, membership_id, side, recipient_id, kind, conversion_id, amount_cents, currency,
				status, hold_until, note, approved_at, created_at)
			SELECT $1, $2, $3, 'partner', m.record_id, 'cash', v.id, $6, 'USD', $7, now() - interval '20 days', 'Imported from Tolt',
				CASE WHEN $7 = 'pending' THEN NULL ELSE now() - interval '19 days' END, now() - interval '20 days'
			FROM v, partner_memberships m WHERE m.id = $3 RETURNING id),
		refs AS (
			INSERT INTO partner_import_refs (workspace_id, program_id, connector, kind, external_id, local_id)
			SELECT $1, $2, 'tolt', 'customer', 'cust_' || $5, f.id FROM f
			UNION ALL SELECT $1, $2, 'tolt', 'commission', 'comm_' || $5, w.id FROM w
			UNION ALL SELECT $1, $2, 'tolt', 'partner', 'part_' || $3, $3::uuid
			ON CONFLICT DO NOTHING)
		SELECT id::text FROM w`, te.ws, te.program, partner, rec, n, cents, status).Scan(&reward)
	if err != nil {
		t.Fatal(err)
	}
	return reward
}

// TestPartnerConversionsNameTheirPartner: every conversion says whose it
// is, and q finds conversions by partner code or email, customer email or
// the sale's own id.
func TestPartnerConversionsNameTheirPartner(t *testing.T) {
	te := newTrailEnv(t)
	te.partner("ana", "Ana Partner")
	te.partner("bo", "")
	te.owner.must(201, "POST", te.base+"/records", map[string]any{"type": "person", "name": "Cara Customer", "identities": []any{
		map[string]any{"namespace": "user_id", "value": "c-1"}, map[string]any{"namespace": "email", "value": "cara@example.com"}}})
	sale := te.owner.must(201, "POST", te.base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-1",
		"ref": "ana", "amount_cents": 10000, "external_id": "ch_100"})["conversion"].(map[string]any)
	if sale["partner_code"] != "ana" || sale["partner_name"] != "Ana Partner" || sale["customer_email"] != "cara@example.com" || sale["customer_name"] != "Cara Customer" {
		t.Fatalf("conversion = %v", sale)
	}
	te.owner.must(201, "POST", te.base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-2", "email": "dan@example.com",
		"ref": "bo", "amount_cents": 5000, "external_id": "ch_200"})

	find := func(q string) []string {
		t.Helper()
		var ids []string
		for _, x := range te.owner.must(200, "GET", te.base+"/partner-conversions?q="+url.QueryEscape(q), nil)["data"].([]any) {
			c := x.(map[string]any)
			ids = append(ids, c["partner_code"].(string)+"/"+c["external_id"].(string))
		}
		return ids
	}
	for q, want := range map[string]string{"ana": "ana/ch_100", "ANA@example": "ana/ch_100", "cara@": "ana/ch_100", "ch_2": "bo/ch_200", "dan@example.com": "bo/ch_200"} {
		if got := find(q); len(got) != 1 || got[0] != want {
			t.Errorf("q=%s found %v, want %s", q, got, want)
		}
	}
	for _, q := range []string{"%", "ch\\_", "nobody"} {
		if got := find(q); len(got) != 0 {
			t.Errorf("q=%s found %v", q, got)
		}
	}
	if got := find("example.com"); len(got) != 2 {
		t.Errorf("q=example.com found %v", got)
	}
	list := te.owner.must(200, "GET", te.base+"/partner-conversions?q=bo", nil)["data"].([]any)
	if c := list[0].(map[string]any); c["partner_name"] != "" || c["customer_name"] != "" || c["customer_email"] != "dan@example.com" {
		t.Fatalf("bo's conversion = %v", c)
	}
	te.owner.must(400, "GET", te.base+"/partner-conversions?q="+url.QueryEscape(string(make([]byte, 201))), nil)
}

// TestPartnerStatsHeldAtSource: the totals say how much of what is pending
// and approved the old platform still pays, until the program switches
// over.
func TestPartnerStatsHeldAtSource(t *testing.T) {
	te := newTrailEnv(t)
	ana := te.partner("ana", "Ana Partner")
	te.imported(t, ana, "1", "imp1@example.com", "pending", 700)
	te.imported(t, ana, "2", "imp2@example.com", "approved", 300)
	te.owner.must(201, "POST", te.base+"/partners/"+ana+"/fees", map[string]any{"amount_cents": 50, "note": "video"})

	held := func(st map[string]any) (float64, float64, float64, float64) {
		h := st["held_at_source"].(map[string]any)
		return st["pending_cents"].(float64), st["approved_cents"].(float64), h["pending_cents"].(float64), h["approved_cents"].(float64)
	}
	stats := te.owner.must(200, "GET", te.base+"/partners/"+ana, nil)["stats"].(map[string]any)
	if p, a, hp, ha := held(stats); p != 700 || a != 350 || hp != 700 || ha != 300 || stats["balance_cents"] != 50.0 {
		t.Fatalf("partner stats = %v", stats)
	}
	prog := te.owner.must(200, "GET", te.base+"/partner-programs/affiliates/stats", nil)
	if p, a, hp, ha := held(prog); p != 700 || a != 350 || hp != 700 || ha != 300 {
		t.Fatalf("program stats = %v", prog)
	}
	te.owner.must(200, "POST", te.base+"/partner-programs/affiliates/switch-over", nil)
	prog = te.owner.must(200, "GET", te.base+"/partner-programs/affiliates/stats", nil)
	if p, a, hp, ha := held(prog); p != 700 || a != 350 || hp != 0 || ha != 0 || prog["balance_cents"] != 350.0 {
		t.Fatalf("program stats after switch-over = %v", prog)
	}
}

// TestPartnerRewardProof: a reward's proof shows each link of the chain
// that credited, priced, decided and paid it, and says why a link is
// missing, once per step.
func TestPartnerRewardProof(t *testing.T) {
	te := newTrailEnv(t)
	ctx := context.Background()
	pool := te.e.pool
	ana := te.partner("ana", "Ana Partner")
	te.owner.must(200, "PATCH", te.base+"/partners/"+ana, map[string]any{"payout_method": "PayPal ana@acme.example", "payout_type": "paypal"})
	bo := te.partner("bo", "Bo Partner")
	te.owner.must(201, "POST", te.base+"/partners/"+bo+"/promo-codes", map[string]any{"code": "BO20"})

	var click string
	if err := pool.QueryRow(ctx, `INSERT INTO partner_clicks (workspace_id, membership_id, sub_id, referrer_host) VALUES ($1, $2, 'spring', 'blog.example')
		RETURNING id::text`, te.ws, ana).Scan(&click); err != nil {
		t.Fatal(err)
	}
	te.owner.must(201, "POST", te.base+"/records", map[string]any{"type": "person", "name": "Cleo Buyer",
		"identities": []any{map[string]any{"namespace": "user_id", "value": "c-click"}}})
	byClick := te.sale(map[string]any{"type": "sale", "customer_id": "c-click", "click_id": click, "amount_cents": 100000, "external_id": "ch_click"})
	byRef := te.sale(map[string]any{"type": "sale", "customer_id": "c-ref", "email": "rita@example.com", "ref": "ana", "sub_id": "news",
		"amount_cents": 20000, "external_id": "ch_ref", "payment_source": "stripe"})
	byPromo := te.sale(map[string]any{"type": "sale", "customer_id": "c-promo", "promo_code": "bo20", "amount_cents": 20000, "external_id": "ch_promo"})

	visit := func(uid string) (reward, session string) {
		t.Helper()
		rec := te.owner.must(201, "POST", te.base+"/records", map[string]any{"type": "person", "name": "Vera " + uid, "identities": []any{
			map[string]any{"namespace": "user_id", "value": uid}}})["id"].(string)
		if err := pool.QueryRow(ctx, `INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, last_seen_at, ref, landing_url, country)
			VALUES ($1, $2, 'anon-' || $3, now() - interval '2 days', now() - interval '2 days', 'ana', 'https://northpeak.example/pricing', 'DE')
			RETURNING id::text`, te.ws, rec, uid).Scan(&session); err != nil {
			t.Fatal(err)
		}
		return te.sale(map[string]any{"type": "sale", "customer_id": uid, "amount_cents": 10000, "external_id": "ch_" + uid}), session
	}
	visitKept, keptSession := visit("v-kept")
	visitLater, laterSession := visit("v-later")
	visitGone, goneSession := visit("v-gone")
	if _, err := pool.Exec(ctx, `UPDATE partner_referrals SET session_id = NULL WHERE session_id = $1`, laterSession); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM site_sessions WHERE id = $1`, goneSession); err != nil {
		t.Fatal(err)
	}
	imported := te.imported(t, ana, "9", "imp9@example.com", "approved", 400)

	te.owner.must(200, "POST", te.base+"/partner-rewards/"+byClick+"/approve", map[string]any{})
	te.owner.must(200, "POST", te.base+"/partner-rewards/"+byRef+"/approve", map[string]any{})
	te.owner.must(200, "POST", te.base+"/partner-rewards/"+byPromo+"/reject", map[string]any{"note": "duplicate order"})
	payout := te.owner.must(200, "POST", te.base+"/partner-payouts", map[string]any{"partner_id": ana})["data"].([]any)[0].(map[string]any)["id"].(string)
	te.owner.must(200, "POST", te.base+"/partner-payouts/"+payout+"/paid", map[string]any{"reference": "PP-123"})

	p := te.proof(byClick)
	credit := p["credit"].(map[string]any)
	if c := credit["click"].(map[string]any); credit["via"] != "click" || c["id"] != click || c["sub_id"] != "spring" || c["referrer_host"] != "blog.example" ||
		c["country"] != "" || credit["visit"] != nil || credit["sub_id"] != "spring" {
		t.Fatalf("click credit = %v", credit)
	}
	conv := p["conversion"].(map[string]any)
	if conv["external_id"] != "ch_click" || conv["amount_cents"] != 100000.0 || conv["transaction_id"] == nil || conv["source"] != "api" {
		t.Fatalf("click sale = %v", conv)
	}
	if r := p["rule"].(map[string]any); r["name"] != "Base rate" || r["percent_bp"] != 2000.0 || r["kept"] != true {
		t.Fatalf("click rule = %v", r)
	}
	if d := p["decision"].(map[string]any); d["status"] != "approved" || d["by"] != "manual" || d["actor_type"] != "human" || d["actor_name"] != "Jack" || d["at"] == nil {
		t.Fatalf("click decision = %v", d)
	}
	if o := p["payout"].(map[string]any); o["id"] != payout || o["status"] != "paid" || o["reference"] != "PP-123" || o["method"] != "paypal" || o["paid_at"] == nil {
		t.Fatalf("click payout = %v", o)
	}
	if c := p["customer"].(map[string]any); c["customer_id"] != "c-click" || c["record_id"] != p["reward"].(map[string]any)["customer_record_id"] {
		t.Fatalf("click customer = %v", c)
	}
	if g := gaps(p); len(g) != 0 || p["imported"] != nil || p["partner"].(map[string]any)["email"] != "ana@example.com" {
		t.Fatalf("click proof = %v", p)
	}

	p = te.proof(byRef)
	if c := p["credit"].(map[string]any); c["via"] != "ref" || c["ref"] != "ana" || c["sub_id"] != "news" || c["click"] != nil {
		t.Fatalf("ref credit = %v", c)
	}
	if c := p["customer"].(map[string]any); c["email"] != "rita@example.com" || p["conversion"].(map[string]any)["payment_source"] != "stripe" {
		t.Fatalf("ref customer = %v", p)
	}
	if g := gaps(p); len(g) != 1 || g["credit"] != "credited_by_ref" || p["payout"].(map[string]any)["id"] != payout {
		t.Fatalf("ref gaps = %v", g)
	}

	p = te.proof(byPromo)
	if c := p["credit"].(map[string]any); c["via"] != "promo_code" || c["promo_code"] != "BO20" || c["ref"] != "" {
		t.Fatalf("promo credit = %v", c)
	}
	if d := p["decision"].(map[string]any); d["status"] != "rejected" || d["by"] != "manual" || d["note"] != "duplicate order" || d["actor_name"] != "Jack" {
		t.Fatalf("promo decision = %v", d)
	}
	if g := gaps(p); len(g) != 2 || g["credit"] != "credited_by_promo_code" || g["payout"] != "not_in_payout" {
		t.Fatalf("promo gaps = %v", g)
	}

	p = te.proof(visitKept)
	v := p["credit"].(map[string]any)["visit"].(map[string]any)
	if v["id"] != keptSession || v["recorded"] != true || v["landing_url"] != "https://northpeak.example/pricing" || v["country"] != "DE" || v["ref"] != "ana" {
		t.Fatalf("kept visit = %v", v)
	}
	if g := gaps(p); len(g) != 2 || g["decision"] != "pending" || g["payout"] != "pending" || p["decision"] != nil {
		t.Fatalf("kept visit gaps = %v", g)
	}
	p = te.proof(visitLater)
	if v := p["credit"].(map[string]any)["visit"].(map[string]any); v["id"] != laterSession || v["recorded"] != false {
		t.Fatalf("matched visit = %v", v)
	}
	if g := gaps(p); g["credit"] != "visit_not_kept" {
		t.Fatalf("matched visit gaps = %v", g)
	}
	p = te.proof(visitGone)
	if c := p["credit"].(map[string]any); c["via"] != "visit" || c["visit"] != nil || gaps(p)["credit"] != "visit_deleted" {
		t.Fatalf("deleted visit = %v", p)
	}

	p = te.proof(imported)
	if i := p["imported"].(map[string]any); i["connector"] != "tolt" || i["commission_id"] != "comm_9" || i["customer_id"] != "cust_9" ||
		i["partner_id"] != "part_"+ana || i["transaction_id"] != "txn_9" || i["payout_id"] != "" {
		t.Fatalf("imported ids = %v", i)
	}
	if r := p["reward"].(map[string]any); r["source"] != "imported" || r["imported_from"] != "tolt" || r["held_at_source"] != true {
		t.Fatalf("imported row = %v", r)
	}
	if d := p["decision"].(map[string]any); d["by"] != "import" || d["status"] != "approved" {
		t.Fatalf("imported decision = %v", d)
	}
	if g := gaps(p); len(g) != 3 || g["credit"] != "imported" || g["rule"] != "imported" || g["payout"] != "imported" {
		t.Fatalf("imported gaps = %v", g)
	}

	// The rules are saved again: a kept copy stays, one without a copy
	// shows the rule as it is now, and one whose rule is gone says so.
	te.owner.must(200, "PUT", te.base+"/partner-programs/affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"name": "New rate", "trigger": "sale", "kind": "fixed", "amount_cents": 500}}})
	current := te.sale(map[string]any{"type": "sale", "customer_id": "c-new", "ref": "ana", "amount_cents": 20000, "external_id": "ch_new"})
	if _, err := pool.Exec(ctx, `UPDATE partner_rewards SET rule = NULL WHERE id = ANY($1::uuid[])`, []string{current, byRef}); err != nil {
		t.Fatal(err)
	}
	if r := te.proof(byClick)["rule"].(map[string]any); r["name"] != "Base rate" || r["kept"] != true || r["kind"] != "percent" {
		t.Fatalf("kept rule after edit = %v", r)
	}
	if r := te.proof(current)["rule"].(map[string]any); r["name"] != "New rate" || r["kept"] != false || r["amount_cents"] != 500.0 {
		t.Fatalf("current rule = %v", r)
	}
	if p := te.proof(byRef); p["rule"] != nil || gaps(p)["rule"] != "rule_replaced" {
		t.Fatalf("replaced rule = %v", p)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM audit_events WHERE target_id = $1 AND action = 'partner_reward.approved'`, byRef); err != nil {
		t.Fatal(err)
	}
	if p := te.proof(byRef); p["decision"].(map[string]any)["by"] != "" || p["decision"].(map[string]any)["at"] == nil || gaps(p)["decision"] != "decided_before_history" {
		t.Fatalf("decision before history = %v", p)
	}

	other := te.owner.must(201, "POST", "/v1/workspaces/"+te.ws+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	inOther := "/v1/brands/" + other
	te.owner.must(201, "POST", inOther+"/partner-programs", map[string]any{"name": "Bravo partners", "slug": "bravo-partners", "kind": "affiliate"})
	cy := te.owner.must(201, "POST", inOther+"/partners", map[string]any{"program": "bravo-partners", "email": "cy@example.com", "code": "cy"})["id"].(string)
	fee := te.owner.must(201, "POST", inOther+"/partners/"+cy+"/fees", map[string]any{"amount_cents": 500, "note": "video"})["id"].(string)
	te.owner.must(404, "GET", te.base+"/partner-rewards/"+fee+"/proof", nil)
	te.owner.must(404, "GET", inOther+"/partner-rewards/"+byClick+"/proof", nil)
	te.owner.must(404, "GET", te.base+"/partner-rewards/not-an-id/proof", nil)
	p = te.owner.must(200, "GET", inOther+"/partner-rewards/"+fee+"/proof", nil)
	if g := gaps(p); g["credit"] != "no_sale" || g["referral"] != "no_sale" || g["customer"] != "no_sale" || g["conversion"] != "no_sale" || g["rule"] != "no_rule" {
		t.Fatalf("fee gaps = %v", g)
	}
	if p["credit"] != nil || p["customer"] != nil || p["reward"].(map[string]any)["customer_record_id"] != nil {
		t.Fatalf("fee proof = %v", p)
	}
}

// TestPartnerRewardRows: the Rewards list says whose each reward is, ties
// a refund's reversal to the reward it takes back, and finds rewards by
// partner, customer or sale, and by where they came from.
func TestPartnerRewardRows(t *testing.T) {
	te := newTrailEnv(t)
	te.partner("ana", "Ana Partner")
	bo := te.partner("bo", "")
	sale := te.sale(map[string]any{"type": "sale", "customer_id": "c-1", "email": "cara@example.com", "ref": "ana", "amount_cents": 50000, "external_id": "ch_1"})
	te.owner.must(200, "POST", te.base+"/partner-rewards/"+sale+"/approve", map[string]any{})
	te.owner.must(201, "POST", te.base+"/partner-conversions", map[string]any{"type": "refund", "refund_of": "ch_1", "external_id": "re_1"})
	imported := te.imported(t, bo, "1", "imp1@example.com", "pending", 300)

	list := func(query string) map[string]map[string]any {
		t.Helper()
		out := map[string]map[string]any{}
		for _, x := range te.owner.must(200, "GET", te.base+"/partner-rewards?"+query, nil)["data"].([]any) {
			out[x.(map[string]any)["id"].(string)] = x.(map[string]any)
		}
		return out
	}
	all := list("")
	if len(all) != 3 {
		t.Fatalf("rewards = %v", all)
	}
	row := all[sale]
	reversals := row["reversed_by"].([]any)
	if len(reversals) != 1 || row["net_cents"] != 0.0 || row["amount_cents"] != 10000.0 || row["partner_code"] != "ana" || row["partner_name"] != "Ana Partner" ||
		row["customer_email"] != "cara@example.com" || row["source"] != "native" || row["imported_from"] != "" || row["held_at_source"] != false ||
		row["payout_status"] != nil {
		t.Fatalf("sale row = %v", row)
	}
	if s := row["sale"].(map[string]any); s["external_id"] != "ch_1" || s["amount_cents"] != 50000.0 || s["type"] != "sale" {
		t.Fatalf("sale = %v", s)
	}
	if d := row["decision"].(map[string]any); d["by"] != "manual" || d["actor_name"] != "Jack" {
		t.Fatalf("sale decision = %v", d)
	}
	adj := all[reversals[0].(string)]
	if adj["reverses_id"] != sale || adj["amount_cents"] != -10000.0 || adj["kind"] != "adjustment" || adj["net_cents"] != -10000.0 ||
		adj["decision"].(map[string]any)["by"] != "refund" || adj["sale"].(map[string]any)["type"] != "refund" || adj["customer_record_id"] != row["customer_record_id"] {
		t.Fatalf("reversal row = %v", adj)
	}
	p := te.proof(sale)
	if r := p["reward"].(map[string]any); r["net_cents"] != 0.0 || len(r["reversed_by"].([]any)) != 1 {
		t.Fatalf("proof row = %v", r)
	}
	if g := gaps(te.proof(adj["id"].(string))); g["rule"] != "no_rule" || g["credit"] != "credited_by_ref" || g["payout"] != "not_paid_yet" {
		t.Fatalf("reversal gaps = %v", g)
	}
	if r := all[imported]; r["source"] != "imported" || r["imported_from"] != "tolt" || r["held_at_source"] != true || r["decision"] != nil || r["partner_name"] != "" {
		t.Fatalf("imported row = %v", r)
	}

	for query, want := range map[string][]string{
		"q=ana":                     {sale, adj["id"].(string)},
		"q=ch_1":                    {sale},
		"q=RE_1":                    {adj["id"].(string)},
		"q=cara%40":                 {sale, adj["id"].(string)},
		"q=bo%40example.com":        {imported},
		"q=imp1":                    {imported},
		"q=%25":                     {},
		"source=native":             {sale, adj["id"].(string)},
		"source=imported":           {imported},
		"source=held_at_source":     {imported},
		"source=native&kind=cash":   {sale},
		"q=ana&status=approved":     {sale, adj["id"].(string)},
		"source=imported&q=nothing": {},
	} {
		got := list(query)
		if len(got) != len(want) {
			t.Errorf("%s: %d rewards, want %d", query, len(got), len(want))
		}
		for _, id := range want {
			if got[id] == nil {
				t.Errorf("%s: missing %s", query, id)
			}
		}
	}
	page := te.owner.must(200, "GET", te.base+"/partner-rewards?limit=2", nil)
	if len(page["data"].([]any)) != 2 || page["next_cursor"] == nil {
		t.Fatalf("first page = %v", page)
	}
	rest := te.owner.must(200, "GET", te.base+"/partner-rewards?limit=2&cursor="+url.QueryEscape(page["next_cursor"].(string)), nil)
	if len(rest["data"].([]any)) != 1 || rest["next_cursor"] != nil {
		t.Fatalf("second page = %v", rest)
	}
	te.owner.must(400, "GET", te.base+"/partner-rewards?source=tolt", nil)

	te.owner.must(200, "POST", te.base+"/partner-programs/affiliates/switch-over", nil)
	if got := list("source=held_at_source"); len(got) != 0 {
		t.Fatalf("held after switch-over = %v", got)
	}
	if r := list("source=imported")[imported]; r["held_at_source"] != false {
		t.Fatalf("imported after switch-over = %v", r)
	}
}

// TestPartnerBulkRewardsHeldAtSource: approving in bulk leaves out imported
// rewards the old platform still pays unless asked, says what it touches
// by source, and a dry run changes nothing.
func TestPartnerBulkRewardsHeldAtSource(t *testing.T) {
	te := newTrailEnv(t)
	ctx := context.Background()
	ana := te.partner("ana", "Ana Partner")
	native := []string{
		te.sale(map[string]any{"type": "sale", "customer_id": "c-1", "ref": "ana", "amount_cents": 10000, "external_id": "ch_1"}),
		te.sale(map[string]any{"type": "sale", "customer_id": "c-2", "ref": "ana", "amount_cents": 10000, "external_id": "ch_2"}),
	}
	// More held rewards than one bulk call takes.
	if _, err := te.e.pool.Exec(ctx, `WITH w AS (
			INSERT INTO partner_rewards (workspace_id, program_id, membership_id, side, recipient_id, kind, amount_cents, currency, status, hold_until, note, created_at)
			SELECT $1, $2, m.id, 'partner', m.record_id, 'cash', 100, 'USD', 'pending', now() - interval '1 day', 'Imported from Tolt', now() - make_interval(days => 400 - g / 10)
			FROM partner_memberships m, generate_series(1, 1001) g WHERE m.id = $3 RETURNING id)
		INSERT INTO partner_import_refs (workspace_id, program_id, connector, kind, external_id, local_id)
		SELECT $1, $2, 'tolt', 'commission', 'comm_' || w.id, w.id FROM w`, te.ws, te.program, ana); err != nil {
		t.Fatal(err)
	}
	var held string
	if err := te.e.pool.QueryRow(ctx, `SELECT local_id::text FROM partner_import_refs WHERE kind = 'commission' LIMIT 1`).Scan(&held); err != nil {
		t.Fatal(err)
	}
	bulk := func(body map[string]any) (done float64, counts map[string]any, skipped map[string]int, more bool) {
		t.Helper()
		out := te.owner.must(200, "POST", te.base+"/partner-rewards/bulk", body)
		skipped = map[string]int{}
		for _, x := range out["skipped"].([]any) {
			skipped[x.(map[string]any)["reason"].(string)]++
		}
		return out["done"].(float64), out["counts"].(map[string]any), skipped, out["more"].(bool)
	}
	approved := func() int {
		var n int
		te.e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_rewards WHERE workspace_id = $1 AND status = 'approved'`, te.ws).Scan(&n) //nolint:errcheck
		return n
	}
	all := map[string]any{"action": "approve", "program": "affiliates"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range all {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}

	done, counts, skipped, more := bulk(with(map[string]any{"dry_run": true}))
	if done != 2 || counts["native"] != 2.0 || counts["imported"] != 0.0 || counts["held_at_source"] != 0.0 || skipped["held_at_source"] != 1000 || more || approved() != 0 {
		t.Fatalf("dry run = %v %v %v %v (approved %d)", done, counts, skipped, more, approved())
	}
	done, counts, _, more = bulk(with(map[string]any{"dry_run": true, "include_held_at_source": true}))
	if done != 1003 || counts["held_at_source"] != 1001.0 || counts["native"] != 2.0 || more || approved() != 0 {
		t.Fatalf("dry run with held = %v %v %v", done, counts, more)
	}
	done, counts, skipped, more = bulk(all)
	if done != 2 || counts["native"] != 2.0 || skipped["held_at_source"] != 1000 || more || approved() != 2 {
		t.Fatalf("approve all = %v %v %v %v", done, counts, skipped, more)
	}
	var audited int
	if err := te.e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partner_reward.approved'
		AND target_id = ANY($2) AND data->>'by' = 'manual'`, te.ws, native).Scan(&audited); err != nil || audited != 2 {
		t.Fatalf("approvals audited = %d, %v", audited, err)
	}
	if done, _, _, more = bulk(all); done != 0 || more {
		t.Fatalf("approve all again = %v %v", done, more)
	}
	done, _, skipped, _ = bulk(map[string]any{"action": "approve", "ids": []string{held}})
	if done != 0 || skipped["held_at_source"] != 1 {
		t.Fatalf("held by id = %v %v", done, skipped)
	}
	done, counts, _, _ = bulk(map[string]any{"action": "approve", "ids": []string{held}, "include_held_at_source": true})
	if done != 1 || counts["held_at_source"] != 1.0 || approved() != 3 {
		t.Fatalf("held by id, included = %v %v", done, counts)
	}

	te.owner.must(200, "POST", te.base+"/partner-programs/affiliates/switch-over", nil)
	done, counts, skipped, more = bulk(with(map[string]any{"dry_run": true}))
	if done != 1000 || counts["imported"] != 1000.0 || counts["held_at_source"] != 0.0 || len(skipped) != 0 || more {
		t.Fatalf("dry run after switch-over = %v %v %v %v", done, counts, skipped, more)
	}
	done, counts, _, _ = bulk(map[string]any{"action": "reject", "partner_id": ana, "dry_run": true})
	if done != 1000 || counts["imported"] != 1000.0 || approved() != 3 {
		t.Fatalf("reject dry run = %v %v", done, counts)
	}
	if out := te.owner.must(200, "POST", te.base+"/partner-rewards/bulk", map[string]any{"action": "approve", "ids": []string{"nope"}}); out["counts"] == nil {
		t.Fatalf("no ids = %v", out)
	}
}

// get answers a GET as the owner, whatever its content type.
func (te *trailEnv) get(path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	req.AddCookie(te.owner.cookie)
	rec := httptest.NewRecorder()
	te.e.h.ServeHTTP(rec, req)
	return rec
}

// TestPartnerProofExport: a payout's or a partner's proof downloads as
// CSV with a column for each link, or as JSON, stays in its brand, guards
// text against spreadsheet formulas, and is audited.
func TestPartnerProofExport(t *testing.T) {
	te := newTrailEnv(t)
	ctx := context.Background()
	ana := te.partner("ana", `=HYPERLINK("x")`)
	te.owner.must(200, "PATCH", te.base+"/partners/"+ana, map[string]any{"payout_method": "PayPal ana@acme.example", "payout_type": "paypal"})
	first := te.sale(map[string]any{"type": "sale", "customer_id": "c-1", "email": "cara@example.com", "ref": "ana", "amount_cents": 100000, "external_id": "@ch_1"})
	second := te.sale(map[string]any{"type": "sale", "customer_id": "c-2", "ref": "ana", "amount_cents": 50000, "external_id": "ch_2"})
	for _, id := range []string{first, second} {
		te.owner.must(200, "POST", te.base+"/partner-rewards/"+id+"/approve", map[string]any{})
	}
	payout := te.owner.must(200, "POST", te.base+"/partner-payouts", map[string]any{"partner_id": ana})["data"].([]any)[0].(map[string]any)["id"].(string)
	te.owner.must(200, "POST", te.base+"/partner-payouts/"+payout+"/paid", map[string]any{"reference": "+PP-1"})
	te.owner.must(201, "POST", te.base+"/partner-conversions", map[string]any{"type": "refund", "refund_of": "ch_2", "external_id": "re_2"})

	rec := te.get(te.base + "/partner-proofs/export?payout_id=" + payout)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/csv") ||
		rec.Header().Get("Content-Disposition") != `attachment; filename="outflare-reward-proofs-payout-`+payout+`.csv"` {
		t.Fatalf("csv = %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	rows, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := "reward_id,reward_kind,reward_status,amount,net_amount,currency,made_at,reverses_id,partner_id,partner_code,partner_name," +
		"credited_via,credited_at,ref,sub_id,promo_code,click_at,click_sub_id,click_country,visit_at,visit_landing_url,visit_country,visit_recorded," +
		"customer_record_id,customer_name,customer_email,customer_id,conversion_id,conversion_type,sale_amount,sale_currency,external_id," +
		"transaction_id,payment_source,conversion_source,occurred_at,rule_name,rule_rate,rule_recurrence,rule_kept," +
		"decided_status,decided_by,decided_actor,decided_at,decided_note,payout_id,payout_status,payout_paid_at,payout_method,payout_reference," +
		"imported_from,source_partner_id,source_customer_id,source_commission_id,source_transaction_id,source_payout_id,gaps"
	if got := strings.Join(rows[0], ","); got != want {
		t.Fatalf("header = %s", got)
	}
	if len(rows) != 3 {
		t.Fatalf("rows = %v", rows)
	}
	col := map[string]int{}
	for i, h := range rows[0] {
		col[h] = i
	}
	row := rows[1]
	for name, v := range map[string]string{"reward_id": first, "amount": "200.00", "net_amount": "200.00", "partner_name": `'=HYPERLINK("x")`,
		"partner_code": "ana", "credited_via": "ref", "ref": "ana", "customer_email": "cara@example.com", "customer_id": "c-1", "external_id": "'@ch_1",
		"sale_amount": "1000.00", "rule_name": "Base rate", "rule_rate": "20%", "rule_kept": "true", "decided_status": "approved", "decided_by": "manual",
		"decided_actor": "Jack", "payout_id": payout, "payout_status": "paid", "payout_method": "paypal", "payout_reference": "'+PP-1",
		"gaps": "credit: Your backend sent the partner's ref code with the customer, so there is no click or visit on record."} {
		if row[col[name]] != v {
			t.Errorf("%s = %q, want %q", name, row[col[name]], v)
		}
	}
	if _, err := time.Parse(time.RFC3339, row[col["payout_paid_at"]]); err != nil {
		t.Errorf("payout_paid_at = %q", row[col["payout_paid_at"]])
	}

	from, to := url.QueryEscape(time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)), url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	rec = te.get(te.base + "/partner-proofs/export?partner_id=" + ana + "&from=" + from + "&to=" + to)
	rows, _ = csv.NewReader(rec.Body).ReadAll()
	if rec.Code != 200 || len(rows) != 4 || rows[3][col["amount"]] != "-100.00" || rows[3][col["reverses_id"]] != second || rows[2][col["net_amount"]] != "0.00" {
		t.Fatalf("partner csv = %d %v", rec.Code, rows)
	}
	var js struct {
		Data []map[string]any `json:"data"`
	}
	rec = te.get(te.base + "/partner-proofs/export?format=json&partner_id=" + ana + "&to=" + from)
	if err := json.Unmarshal(rec.Body.Bytes(), &js); rec.Code != 200 || err != nil || len(js.Data) != 0 {
		t.Fatalf("empty json = %d %s", rec.Code, rec.Body)
	}
	rec = te.get(te.base + "/partner-proofs/export?format=json&payout_id=" + payout)
	if err := json.Unmarshal(rec.Body.Bytes(), &js); rec.Code != 200 || err != nil || len(js.Data) != 2 ||
		js.Data[0]["reward"].(map[string]any)["id"] != first || js.Data[0]["partner"].(map[string]any)["name"] != `=HYPERLINK("x")` {
		t.Fatalf("json = %d %s", rec.Code, rec.Body)
	}

	for _, q := range []string{"", "payout_id=" + payout + "&partner_id=" + ana, "payout_id=nope", "payout_id=" + payout + "&from=" + from,
		"partner_id=" + ana + "&from=" + to + "&to=" + from, "partner_id=" + ana + "&from=yesterday", "payout_id=" + payout + "&format=xlsx"} {
		if rec := te.get(te.base + "/partner-proofs/export?" + q); rec.Code != 400 {
			t.Errorf("%s: %d %s", q, rec.Code, rec.Body)
		}
	}
	other := te.owner.must(201, "POST", "/v1/workspaces/"+te.ws+"/brands", map[string]any{"name": "Bravo", "handle": "bravo"})["id"].(string)
	inOther := "/v1/brands/" + other
	te.owner.must(201, "POST", inOther+"/partner-programs", map[string]any{"name": "Bravo partners", "slug": "bravo-partners", "kind": "affiliate"})
	cy := te.owner.must(201, "POST", inOther+"/partners", map[string]any{"program": "bravo-partners", "email": "cy@example.com", "code": "cy"})["id"].(string)
	for _, path := range []string{inOther + "/partner-proofs/export?payout_id=" + payout, inOther + "/partner-proofs/export?partner_id=" + ana,
		te.base + "/partner-proofs/export?partner_id=" + cy} {
		if rec := te.get(path); rec.Code != 404 {
			t.Errorf("%s: %d", path, rec.Code)
		}
	}

	var audits []string
	r, err := te.e.pool.Query(ctx, `SELECT data->>'filter' || ':' || (data->>'format') || ':' || (data->>'rows') FROM audit_events
		WHERE workspace_id = $1 AND action = 'partner_proofs.exported' ORDER BY id`, te.ws)
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var a string
		r.Scan(&a) //nolint:errcheck
		audits = append(audits, a)
	}
	if got := strings.Join(audits, ","); got != "payout:csv:2,partner:csv:3,partner:json:0,payout:json:2" {
		t.Fatalf("audited = %s", got)
	}

	if _, err := te.e.pool.Exec(ctx, `INSERT INTO partner_rewards (workspace_id, program_id, membership_id, side, recipient_id, kind, amount_cents, currency, note)
		SELECT $1, $2, m.id, 'partner', m.record_id, 'fee', 1, 'USD', 'bulk' FROM partner_memberships m, generate_series(1, 10000) WHERE m.id = $3`,
		te.ws, te.program, ana); err != nil {
		t.Fatal(err)
	}
	if rec := te.get(te.base + "/partner-proofs/export?partner_id=" + ana); rec.Code != 400 || !strings.Contains(rec.Body.String(), "10,000") {
		t.Fatalf("too many = %d %s", rec.Code, rec.Body)
	}
}
