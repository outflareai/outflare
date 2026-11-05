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

// A refund of a payment whose conversion went to Google lowers that
// conversion's value there (a restatement), and a refund of what is left
// takes it away (a retraction), found by the order id it was sent with.
func TestAdConversionAdjustments(t *testing.T) {
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
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "payment.succeeded",
		"target_id": "42", "value_property": "amount_minor", "value_unit": "minor"})["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE ad_conversion_rules SET created_at = now() - interval '1 hour' WHERE id = $1`, rule); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u1", "traits": map[string]any{"email": "ana@acme.example"}})
	var ana string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u1'`, ws).Scan(&ana); err != nil {
		t.Fatal(err)
	}
	owner.must(201, "POST", base+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "granted"})
	tx := "/v1/brands/" + brand + "/transactions"
	owner.must(201, "POST", tx, map[string]any{"kind": "payment", "amount_minor": 5000, "currency": "USD", "source_id": "in_1", "user_id": "u1"})

	googleSuffix.Lock()
	googleSuffix.uploads = nil
	googleSuffix.Unlock()
	uploads := func() []string {
		googleSuffix.Lock()
		defer googleSuffix.Unlock()
		return append([]string(nil), googleSuffix.uploads...)
	}
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("match: %d %v", n, err)
	}
	if res, err := sender.SendRule(ctx, ws, rule); err != nil || res.Sent != 1 {
		var reason string
		e.pool.QueryRow(ctx, `SELECT reason FROM ad_conversion_sends WHERE rule_id = $1`, rule).Scan(&reason)
		t.Fatalf("send: %+v %v %s", res, err, reason)
	}
	var orderID string
	if err := e.pool.QueryRow(ctx, `SELECT event_id FROM ad_conversion_sends WHERE rule_id = $1`, rule).Scan(&orderID); err != nil {
		t.Fatal(err)
	}

	// A partial refund: the conversion's value becomes what is left.
	owner.must(201, "POST", tx, map[string]any{"kind": "refund", "amount_minor": 2000, "currency": "USD", "source_id": "re_1", "refund_of": "in_1", "user_id": "u1"})
	if n, err := adconv.MatchAdjustments(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("match adjustments: %d %v", n, err)
	}
	if n, _ := adconv.MatchAdjustments(ctx, e.pool, ws); n != 0 {
		t.Fatalf("a refund is queued once: %d", n)
	}
	if res, err := sender.AdjustRule(ctx, ws, rule); err != nil || res.Sent != 1 {
		t.Fatalf("adjust: %+v %v", res, err)
	}
	up := uploads()
	last := up[len(up)-1]
	for _, want := range []string{":uploadConversionAdjustments", `"adjustmentType":"RESTATEMENT"`, `"adjustedValue":30`, `"orderId":"` + orderID + `"`,
		`"conversionAction":"customers/123/conversionActions/42"`, `"partialFailure":true`} {
		if !strings.Contains(last, want) {
			t.Fatalf("adjustment lacks %s: %s", want, last)
		}
	}

	// The rest refunded: the conversion is taken away.
	owner.must(201, "POST", tx, map[string]any{"kind": "refund", "amount_minor": 3000, "currency": "USD", "source_id": "re_2", "refund_of": "in_1", "user_id": "u1"})
	if n, err := adconv.MatchAdjustments(ctx, e.pool, ws); err != nil || n != 1 {
		t.Fatalf("second refund: %d %v", n, err)
	}
	if res, err := sender.AdjustRule(ctx, ws, rule); err != nil || res.Sent != 1 {
		t.Fatalf("retract: %+v %v", res, err)
	}
	up = uploads()
	if last = up[len(up)-1]; !strings.Contains(last, `"adjustmentType":"RETRACTION"`) || strings.Contains(last, "restatementValue") {
		t.Fatalf("retraction: %s", last)
	}
	var sent int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM ad_conversion_adjustments WHERE brand_id = $1 AND status = 'sent'`, brand).Scan(&sent); err != nil || sent != 2 {
		t.Fatalf("adjustments sent: %d %v", sent, err)
	}
}
