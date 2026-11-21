package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestAdConversionsSendBack(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	events := track.Capture(t)
	named := func(name string) (out []track.Event) {
		for _, e := range events() {
			if e.Name == name {
				out = append(out, e)
			}
		}
		return out
	}

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	// The platform says where conversions can go.
	targets := owner.must(200, "GET", base+"/ads/conversion-targets?account_id="+acct, nil)
	tl := targets["data"].([]any)
	if len(tl) != 1 || tl[0].(map[string]any)["id"] != "777" || len(targets["setup_needed"].([]any)) != 0 {
		t.Fatalf("targets: %v", targets)
	}
	owner.must(400, "GET", base+"/ads/conversion-targets?account_id=nope", nil)

	// A rule must point at a real target and event type.
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed", "target_id": "999"})
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed", "target_id": "777", "event_type": "Nope"})
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "Order Completed", "target_id": "777"})
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed",
		"target_id": "777", "event_type": "Purchase", "value_property": "total", "value_unit": "minor"})
	ruleID := rule["id"].(string)
	if rule["target_name"] != "Northpeak pixel" || rule["connector"] != "meta_ads" || rule["enabled"] != true {
		t.Fatalf("rule: %v", rule)
	}
	owner.must(409, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed", "target_id": "777", "event_type": "Purchase"})

	// Three buyers: one in the US who clicked an ad, one in Germany
	// without consent, one in Germany who granted it.
	person := func(name, email, country string) string {
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name,
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, brand_id, anonymous_id, record_id, click_ids, country, started_at, last_seen_at)
			VALUES ($1, outflare_first_brand($1), $2, $3, $4, $5, now() - interval '1 hour', now() - interval '1 hour')`,
			ws, "anon-"+name, id, map[string]string{"fbclid": "fb-" + name}, country); err != nil {
			t.Fatal(err)
		}
		return id
	}
	us := person("Ana", "ana@acme.example", "US")
	de := person("Ben", "ben@northpeak.example", "DE")
	ok := person("Cleo", "cleo@northpeak.example", "DE")
	owner.must(201, "POST", base+"/records/"+ok+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "granted"})
	for n, id := range []string{us, de, ok} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', 'order_completed', '{"total": 1250}', $3, now())`, ws, id, "msg-"+string(rune('a'+n))); err != nil {
			t.Fatal(err)
		}
	}

	metaSent.Lock()
	metaSent.events = nil
	metaSent.Unlock()
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 3 {
		t.Fatalf("match: %d %v", n, err)
	}
	if n, _ := adconv.Match(ctx, e.pool, ws); n != 0 {
		t.Fatalf("an event is queued once: %d", n)
	}
	res, err := sender.SendRule(ctx, ws, ruleID)
	if err != nil || res.Sent != 2 || res.Skipped != 1 || res.Failed != 0 {
		t.Fatalf("send: %+v %v", res, err)
	}
	metaSent.Lock()
	got := metaSent.events
	metaSent.Unlock()
	if len(got) != 2 {
		t.Fatalf("meta got %d events", len(got))
	}
	// The batch's order is the database's; find Ana's by its event id.
	var ev map[string]any
	for _, g := range got {
		if g["event_id"] == "msg-a" {
			ev = g
		}
	}
	if ev == nil {
		t.Fatalf("Ana's conversion was not sent: %v", got)
	}
	user := ev["user_data"].(map[string]any)
	if ev["event_name"] != "Purchase" || ev["action_source"] != "website" || ev["custom_data"].(map[string]any)["value"] != 12.5 ||
		!strings.HasPrefix(user["fbc"].(string), "fb.1.") || user["em"].([]any)[0] != connectors.HashID("ana@acme.example") {
		t.Fatalf("event sent: %v", ev)
	}
	// Nothing in the clear leaves for the platform.
	if raw, _ := json.Marshal(got); strings.Contains(string(raw), "@") {
		t.Fatalf("an email left unhashed: %s", raw)
	}

	sends := owner.must(200, "GET", base+"/ads/conversion-sends?rule_id="+ruleID, nil)["data"].([]any)
	byRecord := map[string]map[string]any{}
	for _, s := range sends {
		s := s.(map[string]any)
		byRecord[s["record_id"].(string)] = s
	}
	if byRecord[us]["status"] != "sent" || byRecord[ok]["status"] != "sent" || byRecord[de]["status"] != "skipped" ||
		!strings.Contains(byRecord[de]["reason"].(string), "EEA") || byRecord[us]["value_micros"].(float64) != 12_500_000 {
		t.Fatalf("send log: %v", sends)
	}
	// The log says which country each decision was made on, where that came
	// from, and where the conversion went.
	if byRecord[us]["country"] != "US" || byRecord[us]["country_source"] != "visit" || byRecord[de]["country"] != "DE" ||
		byRecord[us]["target_name"] != "Northpeak pixel" || byRecord[us]["ad_account_id"] != acct || byRecord[us]["record_name"] != "Ana" {
		t.Fatalf("send log details: %v", sends)
	}
	if keys := byRecord[us]["match_keys"].([]any); len(keys) < 2 {
		t.Fatalf("match keys: %v", keys)
	}
	if r := owner.must(200, "GET", base+"/ads/conversion-rules", nil)["data"].([]any)[0].(map[string]any); r["sent_30d"].(float64) != 2 || r["skipped_30d"].(float64) != 1 || r["last_sent_at"] == nil {
		t.Fatalf("rule stats: %v", r)
	}
	if e := named("ad_conversions.sent"); len(e) != 0 {
		t.Fatalf("SendRule alone emits nothing; the job does: %v", e)
	}

	// Buyers the site script never saw, whose purchase says nothing about
	// where they are: an earlier event of theirs that did, then the country
	// on their record, decide. With neither, the conversion is held.
	quiet := func(name, email string) string {
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name,
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
	}
	dee, eli, fay := quiet("Dee", "dee@acme.example"), quiet("Eli", "eli@acme.example"), quiet("Fay", "fay@acme.example")
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, context, message_id, occurred_at)
		VALUES ($1, outflare_first_brand($1), $2, 'track', 'signed_up', '{"location": {"country": "us"}}', 'msg-dee-0', now() - interval '2 days')`, ws, dee); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE records SET attributes = attributes || '{"country": "ca"}' WHERE workspace_id = $1 AND id = $2`, ws, eli); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{dee, eli, fay} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', 'order_completed', '{"total": 500}', $3, now())`, ws, id, "msg-quiet-"+id); err != nil {
			t.Fatal(err)
		}
	}
	adconv.Match(ctx, e.pool, ws)
	if res, err := sender.SendRule(ctx, ws, ruleID); err != nil || res.Sent != 2 || res.Skipped != 1 {
		t.Fatalf("country fallback: %+v %v", res, err)
	}
	for id, want := range map[string][3]string{dee: {"sent", "US", "earlier_event"}, eli: {"sent", "CA", "record"}, fay: {"skipped", "", ""}} {
		l := owner.must(200, "GET", base+"/ads/conversion-sends?record_id="+id, nil)["data"].([]any)
		if len(l) != 1 {
			t.Fatalf("sends for %s: %v", id, l)
		}
		if s := l[0].(map[string]any); s["status"] != want[0] || s["country"] != want[1] || s["country_source"] != want[2] {
			t.Fatalf("fallback for %v: %v", want, s)
		}
	}

	// opt_in: outside the EEA too, only with a grant.
	if s := owner.must(200, "GET", base+"/ads/settings", nil); s["consent_mode"] != "opt_out" {
		t.Fatalf("default settings: %v", s)
	}
	owner.must(400, "PUT", base+"/ads/settings", map[string]any{"consent_mode": "always"})
	owner.must(200, "PUT", base+"/ads/settings", map[string]any{"consent_mode": "opt_in"})
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, occurred_at)
		VALUES ($1, outflare_first_brand($1), $2, 'track', 'order_completed', '{}', 'msg-z', now())`, ws, us); err != nil {
		t.Fatal(err)
	}
	adconv.Match(ctx, e.pool, ws)
	if res, err := sender.SendRule(ctx, ws, ruleID); err != nil || res.Skipped != 1 || res.Sent != 0 {
		t.Fatalf("opt_in: %+v %v", res, err)
	}

	// collected: the brand says its site collects consent from everyone, so
	// Ben in Germany, with no grant on record, is sent as consented. Only a
	// person can say so, and someone who withdrew is still held.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "ads", "scopes": []string{"integrations.manage"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(403, "PUT", base+"/ads/settings", map[string]any{"consent_mode": "collected"})
	owner.must(200, "PUT", base+"/ads/settings", map[string]any{"consent_mode": "collected"})
	owner.must(201, "POST", base+"/records/"+ok+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "withdrawn"})
	for _, x := range [][2]string{{de, "msg-c1"}, {ok, "msg-c2"}, {fay, "msg-c3"}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, properties, message_id, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', 'order_completed', '{}', $3, now())`, ws, x[0], x[1]); err != nil {
			t.Fatal(err)
		}
	}
	adconv.Match(ctx, e.pool, ws)
	if res, err := sender.SendRule(ctx, ws, ruleID); err != nil || res.Sent != 2 || res.Skipped != 1 {
		t.Fatalf("collected: %+v %v", res, err)
	}
	// It covers ad audiences too: Ben joins as consented to personalised
	// ads with no grant on record, and the person who withdrew stays out.
	if add, granted, _, err := adconv.AudienceConsent(ctx, e.pool, ws, de, adconv.Collected); err != nil || !add || !granted {
		t.Fatalf("collected audience: add %v granted %v %v", add, granted, err)
	}
	if add, _, _, err := adconv.AudienceConsent(ctx, e.pool, ws, ok, adconv.Collected); err != nil || add {
		t.Fatalf("collected audience, withdrawn: add %v %v", add, err)
	}
	if add, _, _, err := adconv.AudienceConsent(ctx, e.pool, ws, de, adconv.OptOut); err != nil || add {
		t.Fatalf("opt_out audience in Germany: add %v %v", add, err)
	}

	// Switching a rule off skips what waits; deleting removes it.
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, message_id, occurred_at)
		VALUES ($1, outflare_first_brand($1), $2, 'track', 'order_completed', 'msg-y', now())`, ws, ok); err != nil {
		t.Fatal(err)
	}
	adconv.Match(ctx, e.pool, ws)
	off := owner.must(200, "PATCH", base+"/ads/conversion-rules/"+ruleID, map[string]any{"enabled": false})
	if off["enabled"] != false || off["queued"].(float64) != 0 {
		t.Fatalf("switched off: %v", off)
	}
	owner.must(400, "PATCH", base+"/ads/conversion-rules/"+ruleID, map[string]any{"target_id": "1"})
	owner.must(204, "DELETE", base+"/ads/conversion-rules/"+ruleID, nil)
	owner.must(404, "DELETE", base+"/ads/conversion-rules/"+ruleID, nil)
	if a := named("ad_conversion_rule.deleted"); len(a) != 1 {
		t.Fatalf("delete event: %v", a)
	}
}
