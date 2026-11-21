package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/connectors"
)

// A rule made by hand with feed_role first_payment sends a person's first
// payment once: a later renewal adds nothing, and the same payment sent
// again (the same transaction, or its Order Completed copy) is still one
// conversion, with one order id. Its repeat_payment twin takes the rest.
func TestAdConversionFirstPaymentsOnly(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	var integ string
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		i, err := e.integrations.Insert(ctx, tx, ws, "google_ads", connectors.AuthOAuth2, "Google Ads", connectors.Creds{"access_token": "gt"}, connectors.Account{}, "")
		integ = i.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ads.SyncOnce(ctx, ws, integ); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts?connector=google_ads", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	// Only payments can be first or repeat, and only these roles by hand.
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "signed_up", "target_id": "42", "feed_role": "first_payment"})
	owner.must(400, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "payment.succeeded", "target_id": "42", "feed_role": "signup"})

	first := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "payment.succeeded",
		"target_id": "42", "value_property": "amount_minor", "value_unit": "minor", "feed_role": "first_payment"})
	if first["feed_role"] != "first_payment" || first["customer_type"] != "new" {
		t.Fatalf("first payment rule: %v", first)
	}
	firstID := first["id"].(string)
	// A narrowed rule's event stays payment.succeeded; switching back and
	// forth by PATCH sets the customer type with it.
	owner.must(400, "PATCH", base+"/ads/conversion-rules/"+firstID, map[string]any{"event_name": "signed_up"})
	if back := owner.must(200, "PATCH", base+"/ads/conversion-rules/"+firstID, map[string]any{"feed_role": "custom"}); back["feed_role"] != "custom" || back["customer_type"] != "" {
		t.Fatalf("back to every payment: %v", back)
	}
	if again := owner.must(200, "PATCH", base+"/ads/conversion-rules/"+firstID, map[string]any{"feed_role": "first_payment"}); again["customer_type"] != "new" {
		t.Fatalf("first payments again: %v", again)
	}
	// Its repeat twin, on a second conversion action (the fake account has one).
	var repeatID string
	if err := e.pool.QueryRow(ctx, `INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id, target_name, event_type, value_property, value_unit, enabled, feed_role, customer_type)
		SELECT workspace_id, ad_account_id, event_name, '43', 'Repeat', '', value_property, value_unit, true, 'repeat_payment', 'returning' FROM ad_conversion_rules WHERE id = $1 RETURNING id::text`, firstID).Scan(&repeatID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET created_at = now() - interval '1 hour' WHERE id = ANY($1)`, []string{firstID, repeatID}); err != nil {
		t.Fatal(err)
	}

	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u1", "traits": map[string]any{"email": "ana@acme.example"}})
	var ana string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u1'`, ws).Scan(&ana); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "granted"})
	tx := "/v1/brands/" + brand + "/transactions"
	pay := map[string]any{"kind": "payment", "amount_minor": 5000, "currency": "USD", "source_id": "in_1", "user_id": "u1", "occurred_at": "2026-11-18T10:00:00Z"}
	owner.must(201, "POST", tx, pay)

	googleSuffix.Lock()
	googleSuffix.uploads = nil
	googleSuffix.Unlock()
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sends := func(rule string) []string {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT event_id FROM ad_conversion_sends WHERE rule_id = $1 ORDER BY occurred_at`, rule)
		if err != nil {
			t.Fatal(err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if _, err := adconv.Match(ctx, e.pool, ws); err != nil {
		t.Fatal(err)
	}
	if res, err := sender.SendRule(ctx, ws, firstID); err != nil || res.Sent != 1 {
		t.Fatalf("send first: %+v %v", res, err)
	}
	ids := sends(firstID)
	if len(ids) != 1 || len(sends(repeatID)) != 0 {
		t.Fatalf("first payment: first %v repeat %v", ids, sends(repeatID))
	}
	googleSuffix.Lock()
	up := strings.Join(googleSuffix.uploads, "\n")
	googleSuffix.Unlock()
	for _, want := range []string{`"customerType":"NEW"`, `"transactionId":"` + ids[0] + `"`, `"conversionValue":50`} {
		if !strings.Contains(up, want) {
			t.Fatalf("the first payment lacks %s:\n%s", want, up)
		}
	}

	// The same payment again: the same transaction, then its order event.
	owner.must(200, "POST", tx, pay)
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "track", "userId": "u1", "event": "Order Completed", "messageId": "order-in_1",
		"timestamp": "2026-11-18T10:00:00Z", "properties": map[string]any{"order_id": "in_1", "revenue": 50, "currency": "USD"}})
	// A renewal a month on.
	owner.must(201, "POST", tx, map[string]any{"kind": "payment", "amount_minor": 5000, "currency": "USD", "source_id": "in_2", "user_id": "u1"})
	if _, err := adconv.Match(ctx, e.pool, ws); err != nil {
		t.Fatal(err)
	}
	if got := sends(firstID); len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("the first payment must stay one conversion: %v", got)
	}
	if got := sends(repeatID); len(got) != 1 || got[0] == ids[0] {
		t.Fatalf("the renewal is the repeat rule's only payment: %v", got)
	}
	var payments int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND name = 'payment.succeeded'`, ws).Scan(&payments); err != nil || payments != 2 {
		t.Fatalf("payment events: %d %v", payments, err)
	}
}
