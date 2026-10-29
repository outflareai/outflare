package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// A rule can send a conversion definition instead of a bare event: only
// the events that meet its conditions go, worth what it says, as the event
// type it names for the network.
func TestAdConversionsFromDefinitions(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	owner.must(201, "POST", base+"/conversion-definitions", map[string]any{"key": "qualified_call", "name": "Qualified call", "event": "call.completed",
		"filters": []any{map[string]any{"property": "duration_s", "op": "gt", "value": 60}}, "value_mode": "fixed", "fixed_value_minor": 5000, "currency": "USD",
		"network_events": map[string]string{"meta": "Schedule"}})

	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "target_id": "777"})
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "target_id": "777", "conversion": "no_such"})
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "target_id": "777", "conversion": "qualified_call", "event_name": "signed_up"})
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "target_id": "777", "conversion": "qualified_call"})
	ruleID := rule["id"].(string)
	// The definition's own name for Meta is taken over the first type.
	if rule["conversion"] != "qualified_call" || rule["conversion_name"] != "Qualified call" || rule["event_name"] != "call.completed" || rule["event_type"] != "Schedule" {
		t.Fatalf("rule: %v", rule)
	}

	ana := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana",
		"identities": []any{map[string]any{"namespace": "email", "value": "ana@acme.example"}}})["id"].(string)
	if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, record_id, click_ids, country, started_at, last_seen_at)
		VALUES ($1, 'anon-ana', $2, '{"fbclid": "fb-ana"}', 'US', now() - interval '1 hour', now() - interval '1 hour')`, ws, ana); err != nil {
		t.Fatal(err)
	}
	// A long call and a short one, received a few minutes ago so the
	// matcher has settled on them.
	for _, c := range []struct{ msg, props string }{{"call-long", `{"duration_s": 300}`}, {"call-short", `{"duration_s": 20}`}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, message_id, occurred_at, received_at)
			VALUES ($1, $2, 'track', 'call.completed', $3, $4, now() - interval '5 minutes', now() - interval '5 minutes')`, ws, ana, c.props, c.msg); err != nil {
			t.Fatal(err)
		}
	}
	// The rule was made just now, after those events arrived: let it have
	// been made earlier.
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET created_at = now() - interval '10 minutes' WHERE id = $1`, ruleID); err != nil {
		t.Fatal(err)
	}

	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("match: %d %v", n, err)
	}
	// Each event is checked once: the short call is never looked at again,
	// and the long one isn't queued twice.
	var until *string
	if err := e.pool.QueryRow(ctx, `SELECT matched_until::text FROM ad_conversion_rules WHERE id = $1`, ruleID).Scan(&until); err != nil || until == nil {
		t.Fatalf("matched_until = %v, %v", until, err)
	}
	if n, _ := adconv.Match(ctx, e.pool, ws); n != 0 {
		t.Fatalf("matched again: %d", n)
	}

	metaSent.Lock()
	metaSent.events = nil
	metaSent.Unlock()
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if res, err := sender.SendRule(ctx, ws, ruleID); err != nil || res.Sent != 1 || res.Skipped != 0 {
		t.Fatalf("send: %+v %v", res, err)
	}
	metaSent.Lock()
	got := metaSent.events
	metaSent.Unlock()
	if len(got) != 1 || got[0]["event_id"] != "call-long" || got[0]["event_name"] != "Schedule" || got[0]["custom_data"].(map[string]any)["value"] != 50.0 {
		t.Fatalf("meta got: %v", got)
	}

	// Archiving the definition stops the rule from queueing anything; one
	// already queued is skipped with the reason. (Time passes: the rule
	// checked up to four minutes ago, and a call arrives after that.)
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET matched_until = now() - interval '4 minutes' WHERE id = $1`, ruleID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, message_id, occurred_at, received_at)
		VALUES ($1, $2, 'track', 'call.completed', '{"duration_s": 900}', 'call-later', now() - interval '3 minutes', now() - interval '3 minutes')`, ws, ana); err != nil {
		t.Fatal(err)
	}
	if n, _ := adconv.Match(ctx, e.pool, ws); n != 1 {
		t.Fatalf("later call: %d", n)
	}
	owner.must(204, "DELETE", base+"/conversion-definitions/qualified_call", nil)
	if res, err := sender.SendRule(ctx, ws, ruleID); err != nil || res.Skipped != 1 || res.Sent != 0 {
		t.Fatalf("archived: %+v %v", res, err)
	}

	// Going back to a bare event keeps the event and drops the definition.
	r := owner.must(200, "PATCH", base+"/ads/conversion-rules/"+ruleID, map[string]any{"conversion": ""})
	if r["conversion"] != nil || r["event_name"] != "call.completed" {
		t.Fatalf("cleared: %v", r)
	}
}
