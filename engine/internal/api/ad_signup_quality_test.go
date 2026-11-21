package api_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// A rule that sends qualified sign-ups with a predicted value decides from
// the brand's own history of who paid after signing up.
func TestAdSignupQuality(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	adconv.ResetQualityCache()
	t.Cleanup(adconv.ResetQualityCache)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}

	person := func(email string) string {
		t.Helper()
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Person",
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
	}
	event := func(rec, name, country, props, at string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, context, properties, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', $3, jsonb_build_object('location', jsonb_build_object('country', $4::text)), $5::jsonb, now() - $6::interval)`,
			ws, rec, name, country, props, at); err != nil {
			t.Fatal(err)
		}
	}
	// History, 40 days back: 60 from the US of whom 12 paid 50.00, and 60
	// from Brazil of whom none paid.
	for i := 0; i < 60; i++ {
		us := person(fmt.Sprintf("us.person%d@gmail.com", i))
		event(us, "signed_up", "US", `{}`, "40 days")
		if i < 12 {
			event(us, "payment.succeeded", "US", `{"amount_minor": 5000, "currency": "USD"}`, "39 days")
		}
		event(person(fmt.Sprintf("br.person%d@gmail.com", i)), "signed_up", "BR", `{}`, "40 days")
	}

	q := owner.must(200, "GET", "/v1/brands/"+brand+"/ads/signup-quality", nil)
	if q["enough"] != true || q["signups"] != float64(120) || q["paid"] != float64(12) || q["value_per_payer_micros"] != float64(50_000_000) || len(q["segments"].([]any)) != 2 {
		t.Fatalf("quality: %v", q)
	}
	if thin := owner.must(200, "GET", "/v1/brands/"+brand+"/ads/signup-quality?event=trial_started", nil); thin["enough"] != false {
		t.Fatalf("no history: %v", thin)
	}

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	made := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "signed_up",
		"target_id": "777", "event_type": "Lead", "currency": "USD", "predicted_value": true, "qualified_only": true})
	rule := made["id"].(string)
	if made["predicted_value"] != true || made["qualified_only"] != true {
		t.Fatalf("rule: %v", made)
	}

	us, br, junk := person("new.us@gmail.com"), person("new.br@gmail.com"), person("new.junk@mailinator.com")
	event(us, "signed_up", "US", `{}`, "0")
	event(br, "signed_up", "BR", `{}`, "0")
	event(junk, "signed_up", "US", `{}`, "0")
	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 3 {
		t.Fatalf("match: %d %v", n, err)
	}
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if res, err := sender.SendRule(ctx, ws, rule); err != nil || res.Sent != 1 || res.Skipped != 2 {
		t.Fatalf("send: %+v %v", res, err)
	}
	by := map[string]map[string]any{}
	for _, s := range owner.must(200, "GET", base+"/ads/conversion-sends?rule_id="+rule, nil)["data"].([]any) {
		s := s.(map[string]any)
		by[s["record_id"].(string)] = s
	}
	if by[us]["status"] != "sent" || by[us]["value_micros"] != float64(8_529_412) || by[us]["currency"] != "USD" {
		t.Errorf("US sign-up: %v", by[us])
	}
	for _, id := range []string{br, junk} {
		if by[id]["status"] != "skipped" || !strings.Contains(by[id]["reason"].(string), "not a qualified sign-up") {
			t.Errorf("held sign-up: %v", by[id])
		}
	}

	// Switched off again, the rule goes back to its own value.
	off := owner.must(200, "PATCH", base+"/ads/conversion-rules/"+rule, map[string]any{"predicted_value": false, "qualified_only": false})
	if off["predicted_value"] != false || off["qualified_only"] != false {
		t.Fatalf("rule off: %v", off)
	}
}
