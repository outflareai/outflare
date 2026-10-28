package api_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestPartnerAffiliateProgram(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "handle": "geonode"})
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{
		"name": "Geonode affiliates", "kind": "affiliate", "brand": "geonode", "landing_url": "https://geonode.example/pricing",
	})
	rules := prog["rules"].([]any)
	if prog["slug"] != "geonode-affiliates" || prog["hold_days"] != float64(30) || len(rules) != 1 || rules[0].(map[string]any)["percent_bp"] != float64(2000) {
		t.Fatalf("program = %v", prog)
	}
	owner.must(409, "POST", base+"/partner-programs", map[string]any{"name": "Geonode affiliates", "kind": "affiliate"})
	owner.must(400, "POST", base+"/partner-programs", map[string]any{"name": "X", "kind": "loyalty"})
	owner.must(400, "PUT", base+"/partner-programs/geonode-affiliates/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "lead", "kind": "percent", "percent_bp": 1000}}})
	owner.must(400, "PATCH", base+"/partner-programs/geonode-affiliates", map[string]any{"kind": "referral"})

	// A partner is a customer record, with a code, a short link and a campaign link.
	ana := owner.must(201, "POST", base+"/partners", map[string]any{"program": "geonode-affiliates", "email": "ana@example.com", "name": "Ana Lima", "payout_method": "PayPal: ana@example.com"})
	if ana["code"] != "ana" || ana["status"] != "active" || ana["name"] != "Ana Lima" || ana["email"] != "ana@example.com" || ana["payout_method"] != "PayPal: ana@example.com" {
		t.Fatalf("partner = %v", ana)
	}
	// The payout method is stored sealed.
	var sealed []byte
	if err := e.pool.QueryRow(ctx, `SELECT payout_method FROM partner_memberships WHERE id = $1`, ana["id"]).Scan(&sealed); err != nil || len(sealed) == 0 || strings.Contains(string(sealed), "PayPal") {
		t.Fatalf("payout_method stored as %q, %v", sealed, err)
	}
	if !strings.HasSuffix(ana["link"].(string), "/r/"+ws+"/ana") ||
		!strings.Contains(ana["campaign_link"].(string), "utm_source=partner&utm_medium=affiliate&utm_campaign=ana&ofc=") ||
		!strings.HasSuffix(ana["campaign_link"].(string), "&ref=ana") {
		t.Fatalf("links = %v / %v", ana["link"], ana["campaign_link"])
	}
	anaID := ana["id"].(string)
	owner.must(409, "POST", base+"/partners", map[string]any{"program": "geonode-affiliates", "email": "ana@example.com"})
	owner.must(409, "POST", base+"/partners", map[string]any{"program": "geonode-affiliates", "email": "other@example.com", "code": "ANA"})
	owner.must(400, "PATCH", base+"/partners/"+anaID, map[string]any{"code": "ana2"})

	// The short link counts the click and lands tagged, with the sub-id as utm_content.
	code, loc := owner.rawUA("GET", "/r/"+ws+"/ana?sub=yt-review&to=/blog/proxies", "Mozilla/5.0")
	if code != http.StatusFound || !strings.HasPrefix(loc, "https://geonode.example/blog/proxies?utm_source=partner") ||
		!strings.Contains(loc, "utm_content=yt-review") || !strings.Contains(loc, "&ref=ana&ref_click=") {
		t.Fatalf("redirect %d %s", code, loc)
	}
	u, _ := url.Parse(loc)
	click := u.Query().Get("ref_click")
	// Another site in ?to lands on the landing page instead.
	if _, loc := owner.rawUA("GET", "/r/"+ws+"/ana?to=https://evil.example/x", "Mozilla/5.0"); !strings.HasPrefix(loc, "https://geonode.example/pricing?") {
		t.Fatalf("open redirect: %s", loc)
	}
	if code, _ := owner.rawUA("GET", "/r/"+ws+"/nobody", "Mozilla/5.0"); code != http.StatusNotFound {
		t.Fatalf("unknown code = %d", code)
	}

	// The backend reports a sign-up with the click id, then a sale.
	conv := "/v1/workspaces/" + ws + "/partner-conversions"
	lead := owner.must(201, "POST", conv, map[string]any{"type": "lead", "customer_id": "u-1", "email": "bo@example.com", "click_id": click})
	if lead["credited"] != true || lead["partner_id"] != anaID || len(lead["rewards"].([]any)) != 0 || lead["conversion"].(map[string]any)["type"] != "signup" {
		t.Fatalf("lead = %v", lead)
	}
	if again := owner.must(200, "POST", conv, map[string]any{"type": "lead", "customer_id": "u-1"}); again["duplicate"] != true {
		t.Fatalf("second lead = %v", again)
	}
	// No ref this time: the referral is already bound.
	sale := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-1", "amount_cents": 10000, "external_id": "ch_1", "product_id": "residential"})
	rw := sale["rewards"].([]any)
	if sale["credited"] != true || len(rw) != 1 || rw[0].(map[string]any)["amount_cents"] != float64(2000) || rw[0].(map[string]any)["status"] != "pending" {
		t.Fatalf("sale = %v", sale)
	}
	reward := rw[0].(map[string]any)["id"].(string)
	if again := owner.must(200, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-1", "amount_cents": 10000, "external_id": "ch_1"}); again["duplicate"] != true {
		t.Fatalf("retried sale = %v", again)
	}
	// A customer nobody referred is answered, not refused.
	if none := owner.must(200, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-2", "amount_cents": 500}); none["credited"] != false || none["reason"] != "no_partner" {
		t.Fatalf("unreferred = %v", none)
	}
	// Every reported conversion is a customer event attribution reads,
	// credited to a partner or not; a retry is stored once.
	var events int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded'
		AND (properties->>'payment_id' = 'ch_1' AND (properties->>'amount_minor')::int = 10000 OR properties->>'payment_id' IS NULL)`, ws).Scan(&events); err != nil || events != 2 {
		t.Fatalf("payment.succeeded events = %d, %v", events, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'signed_up'`, ws).Scan(&events); err != nil || events != 1 {
		t.Fatalf("signed_up events = %d, %v", events, err)
	}
	owner.must(400, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-1"})
	owner.must(400, "POST", conv, map[string]any{"type": "custom", "customer_id": "u-1"})
	owner.must(400, "POST", conv, map[string]any{"type": "custom", "event_name": "payment.succeeded", "customer_id": "u-1"})
	owner.must(400, "POST", conv, map[string]any{"type": "refund", "customer_id": "u-1"})
	// The partner buying through their own link is not credited.
	if self := owner.must(200, "POST", conv, map[string]any{"type": "sale", "email": "ana@example.com", "ref": "ana", "amount_cents": 900}); self["reason"] != "self_referral" {
		t.Fatalf("self = %v", self)
	}

	// Approving logs the cost; a partial refund takes back its share.
	owner.must(200, "POST", base+"/partner-rewards/"+reward+"/approve", map[string]any{})
	var spend int64
	if err := e.pool.QueryRow(ctx, `SELECT amount_cents FROM spend_entries WHERE workspace_id = $1 AND ref = $2 AND channel = 'affiliates'`, ws, "partner_reward:"+reward).Scan(&spend); err != nil || spend != 2000 {
		t.Fatalf("spend = %d, %v", spend, err)
	}
	ref := owner.must(201, "POST", conv, map[string]any{"type": "refund", "refund_of": "ch_1", "amount_cents": 5000, "external_id": "re_1"})
	adj := ref["rewards"].([]any)
	if len(adj) != 1 || adj[0].(map[string]any)["amount_cents"] != float64(-1000) || adj[0].(map[string]any)["status"] != "approved" {
		t.Fatalf("refund = %v", ref)
	}
	if err := e.pool.QueryRow(ctx, `SELECT amount_cents FROM spend_entries WHERE ref = $1`, "partner_reward:"+reward).Scan(&spend); err != nil || spend != 1000 {
		t.Fatalf("spend after refund = %d, %v", spend, err)
	}
	if r := owner.must(200, "POST", conv, map[string]any{"type": "refund", "refund_of": "ch_404"}); r["reason"] != "sale_not_found" {
		t.Fatalf("unknown refund = %v", r)
	}

	// Promo codes credit too.
	owner.must(201, "POST", base+"/partners/"+anaID+"/promo-codes", map[string]any{"code": "ANA20"})
	if p := owner.must(201, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-3", "promo_code": "ana20", "amount_cents": 5000}); p["partner_id"] != anaID {
		t.Fatalf("promo = %v", p)
	}

	// A fee and a payout, marked paid with a reference.
	owner.must(201, "POST", base+"/partners/"+anaID+"/fees", map[string]any{"amount_cents": 50000, "note": "October video"})
	owner.must(400, "POST", base+"/partners/"+anaID+"/adjustments", map[string]any{"amount_cents": -100})
	p := owner.must(200, "GET", base+"/partners/"+anaID, nil)
	st := p["stats"].(map[string]any)
	if st["balance_cents"] != float64(51000) || st["pending_cents"] != float64(1000) || st["clicks"] != float64(2) || st["customers"] != float64(2) {
		t.Fatalf("stats = %v", st)
	}
	payouts := owner.must(200, "POST", base+"/partner-payouts", map[string]any{})["data"].([]any)
	if len(payouts) != 1 || payouts[0].(map[string]any)["amount_cents"] != float64(51000) || payouts[0].(map[string]any)["status"] != "due" {
		t.Fatalf("payouts = %v", payouts)
	}
	po := payouts[0].(map[string]any)["id"].(string)
	if again := owner.must(200, "POST", base+"/partner-payouts", map[string]any{})["data"].([]any); len(again) != 0 {
		t.Fatalf("second run = %v", again)
	}
	paid := owner.must(200, "POST", base+"/partner-payouts/"+po+"/paid", map[string]any{"note": "Wise batch", "reference": "WISE-123"})
	if paid["status"] != "paid" || paid["reference"] != "WISE-123" {
		t.Fatalf("paid = %v", paid)
	}
	owner.must(400, "POST", base+"/partner-payouts/"+po+"/cancel", map[string]any{})
	if st := owner.must(200, "GET", base+"/partners/"+anaID, nil)["stats"].(map[string]any); st["paid_cents"] != float64(51000) || st["balance_cents"] != float64(0) {
		t.Fatalf("after payout = %v", st)
	}

	// Lists.
	if l := owner.must(200, "GET", base+"/partner-conversions?partner_id="+anaID+"&type=sale", nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("conversions = %v", l)
	}
	if l := owner.must(200, "GET", base+"/partner-referrals?partner_id="+anaID, nil)["data"].([]any); len(l) != 2 {
		t.Fatalf("referrals = %v", l)
	}
	if l := owner.must(200, "GET", base+"/partner-rewards?status=paid", nil)["data"].([]any); len(l) != 3 {
		t.Fatalf("paid rewards = %v", l)
	}

	// A server token with only partners.conversions reports conversions and nothing else.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Backend", "scopes": []string{"partners.conversions"}})["token"].(string)
	backend := &client{t: t, h: e.h, bearer: tok}
	backend.must(200, "POST", conv, map[string]any{"type": "sale", "customer_id": "u-9", "amount_cents": 100})
	backend.must(403, "POST", base+"/partner-programs", map[string]any{"name": "Y", "kind": "affiliate"})

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, n := range []string{"partner_program.created", "partner.created", "partner_referral.created", "partner_conversion.created",
		"partner_reward.created", "partner_reward.approved", "partner_reward.reversed", "partner_payout.created", "partner_payout.paid"} {
		if !names[n] {
			t.Errorf("no %s event", n)
		}
	}
}

func TestPartnerReferralProgram(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// Two-sided, approved at once: the webhook tells the backend to grant both.
	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{
		"name": "Refer a friend", "kind": "referral", "landing_url": "https://northpeak.example/", "hold_days": 0,
	})
	if len(prog["rules"].([]any)) != 2 {
		t.Fatalf("preset = %v", prog["rules"])
	}
	owner.must(200, "PUT", base+"/partner-programs/refer-a-friend/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "sale", "kind": "action", "action": "grant_credit", "action_data": map[string]any{"gb": 10}, "recurrence": "first"},
		map[string]any{"trigger": "sale", "side": "referred", "kind": "action", "action": "grant_credit", "action_data": map[string]any{"gb": 5}, "recurrence": "first"},
	}})
	rec := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Cara", "identities": []any{map[string]any{"namespace": "email", "value": "cara@example.com"}}})
	cara := owner.must(201, "POST", base+"/partners", map[string]any{"program": "refer-a-friend", "record_id": rec["id"]})

	// The friend visited with Cara's code before signing up: the visit proposes, the sale binds.
	friend := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "identities": []any{map[string]any{"namespace": "user_id", "value": "f-1"}}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, last_seen_at, ref, landing_url)
		VALUES ($1, $2, 'v1', now() - interval '3 days', now() - interval '3 days', $3, 'https://northpeak.example/')`, ws, friend["id"], cara["code"]); err != nil {
		t.Fatal(err)
	}
	sale := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "f-1", "amount_cents": 2000, "external_id": "o-1"})
	rw := sale["rewards"].([]any)
	if sale["partner_id"] != cara["id"] || len(rw) != 2 {
		t.Fatalf("sale = %v", sale)
	}
	for _, x := range rw {
		w := x.(map[string]any)
		if w["status"] != "approved" || w["kind"] != "action" || w["action"] != "grant_credit" {
			t.Fatalf("reward = %v", w)
		}
	}
	var ref string
	if err := e.pool.QueryRow(ctx, `SELECT via FROM partner_referrals WHERE record_id = $1`, friend["id"]).Scan(&ref); err != nil || ref != "visit" {
		t.Fatalf("via = %q, %v", ref, err)
	}
	// The webhook data names the friend by the backend's own user id.
	var uid string
	if err := e.pool.QueryRow(ctx, `SELECT data->>'recipient_customer_id' FROM audit_events WHERE workspace_id = $1 AND action = 'partner_reward.approved'
		AND data->>'side' = 'referred'`, ws).Scan(&uid); err != nil || uid != "f-1" {
		t.Fatalf("recipient_customer_id = %q, %v", uid, err)
	}
	first := rw[0].(map[string]any)["id"].(string)
	owner.must(200, "POST", base+"/partner-rewards/"+first+"/fulfil", map[string]any{"note": "granted"})
	owner.must(400, "POST", base+"/partner-rewards/"+first+"/fulfil", map[string]any{})
	// A second sale earns nothing more: first sale only.
	if s := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "f-1", "amount_cents": 2000, "external_id": "o-2"}); len(s["rewards"].([]any)) != 0 {
		t.Fatalf("second sale = %v", s)
	}
	// A full refund of the first sale reverses the actions already given.
	r := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "refund", "refund_of": "o-1"})
	if len(r["rewards"].([]any)) != 2 {
		t.Fatalf("refund = %v", r)
	}

	// A custom conversion earns by its own event name.
	owner.must(200, "PUT", base+"/partner-programs/refer-a-friend/rules", map[string]any{"rules": []any{
		map[string]any{"trigger": "trial_started", "kind": "fixed", "amount_cents": 300},
	}})
	trial := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "custom", "event_name": "trial_started", "customer_id": "f-1", "external_id": "t-1"})
	if rw := trial["rewards"].([]any); len(rw) != 1 || rw[0].(map[string]any)["amount_cents"] != float64(300) || trial["conversion"].(map[string]any)["event_name"] != "trial_started" {
		t.Fatalf("custom = %v", trial)
	}
	var trials int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'trial_started'`, ws).Scan(&trials); err != nil || trials != 1 {
		t.Fatalf("trial_started events = %d, %v", trials, err)
	}

	// The postback URL takes the same conversion by URL.
	pb := owner.must(201, "POST", base+"/partner-programs/refer-a-friend/postback", map[string]any{})
	pbURL, _ := url.Parse(pb["url"].(string))
	anon := &client{t: t, h: e.h}
	res := anon.must(201, "GET", pbURL.Path+"?type=sale&ref="+cara["code"].(string)+"&user_id=f-2&amount=49.90&order_id=o-3", nil)
	if res["credited"] != true || res["conversion"].(map[string]any)["amount_cents"] != float64(4990) || res["conversion"].(map[string]any)["source"] != "postback" {
		t.Fatalf("postback = %v", res)
	}
	if code, _ := anon.raw("POST", pbURL.Path, "application/x-www-form-urlencoded", "type=sale&ref=nobody&user_id=f-4&amount=1"); code != 200 {
		t.Fatalf("form postback = %d", code)
	}
	anon.must(404, "GET", "/v1/partner-postbacks/ofb_not_a_real_token?type=sale", nil)
	// Rotating it retires the old URL.
	owner.must(201, "POST", base+"/partner-programs/refer-a-friend/postback", map[string]any{})
	anon.must(404, "GET", pbURL.Path+"?type=lead&user_id=f-5", nil)

	// A suspended partner's link still lands, untagged and uncounted.
	owner.must(200, "PATCH", base+"/partners/"+cara["id"].(string), map[string]any{"status": "suspended"})
	if _, loc := owner.rawUA("GET", "/r/"+ws+"/"+cara["code"].(string), "Mozilla/5.0"); loc != "https://northpeak.example/" {
		t.Fatalf("suspended link = %s", loc)
	}
	var clicks int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM partner_clicks WHERE workspace_id = $1`, ws).Scan(&clicks); err != nil || clicks != 0 {
		t.Fatalf("clicks = %d, %v", clicks, err)
	}
}

// A partial refund inside the hold adds a held adjustment; whichever of the
// two is approved first, the cost logged is the net amount.
func TestPartnerRefundDuringHold(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Affiliates", "kind": "affiliate", "landing_url": "https://northpeak.example/"})
	owner.must(201, "POST", base+"/partners", map[string]any{"program": "affiliates", "email": "dee@example.com", "code": "dee"})
	sale := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-1", "ref": "dee", "amount_cents": 10000, "external_id": "s-1"})
	orig := sale["rewards"].([]any)[0].(map[string]any)["id"].(string)
	ref := owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "refund", "refund_of": "s-1", "amount_cents": 2500})
	adj := ref["rewards"].([]any)[0].(map[string]any)
	if adj["amount_cents"] != float64(-500) || adj["status"] != "pending" {
		t.Fatalf("adjustment = %v", adj)
	}
	owner.must(200, "POST", base+"/partner-rewards/"+adj["id"].(string)+"/approve", map[string]any{})
	owner.must(200, "POST", base+"/partner-rewards/"+orig+"/approve", map[string]any{})
	var spend int64
	if err := e.pool.QueryRow(context.Background(), `SELECT amount_cents FROM spend_entries WHERE ref = $1`, "partner_reward:"+orig).Scan(&spend); err != nil || spend != 1500 {
		t.Fatalf("spend = %d, %v", spend, err)
	}
	if st := owner.must(200, "GET", base+"/partners?program=affiliates", nil)["data"].([]any)[0].(map[string]any)["stats"].(map[string]any); st["balance_cents"] != float64(1500) {
		t.Fatalf("balance = %v", st)
	}
}
