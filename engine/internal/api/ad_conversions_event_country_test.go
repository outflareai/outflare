package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// A conversion sent only from a server, for someone the site script never
// saw, has no website visit to give a country. The event's own country
// (context.location.country, else its context.ip) decides consent instead,
// so it isn't skipped as "country unknown".
func TestAdConversionsEventCountry(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	rule := owner.must(201, "POST", base+"/ads/conversion-rules", map[string]any{"account_id": acct, "event_name": "order_completed",
		"target_id": "777", "event_type": "Purchase"})["id"].(string)

	person := func(name, email string) string {
		t.Helper()
		return owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name,
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
	}
	us := person("Ana", "ana@acme.example")
	ip := person("Ben", "ben@acme.example")
	de := person("Cleo", "cleo@northpeak.example")
	none := person("Dev", "dev@northpeak.example")
	for msg, ev := range map[string]struct{ rec, context string }{
		"order-us":   {us, `{"location": {"country": "us"}}`},
		"order-ip":   {ip, `{"ip": "8.8.8.8"}`},
		"order-de":   {de, `{"location": {"country": "DE"}, "ip": "8.8.8.8"}`},
		"order-none": {none, `{}`},
	} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, context, message_id, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'track', 'order_completed', $3::jsonb, $4, now())`, ws, ev.rec, ev.context, msg); err != nil {
			t.Fatal(err)
		}
	}

	if n, err := adconv.Match(ctx, e.pool, ws); err != nil || n != 4 {
		t.Fatalf("match: %d %v", n, err)
	}
	sender := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if res, err := sender.SendRule(ctx, ws, rule); err != nil || res.Sent != 2 || res.Skipped != 2 {
		t.Fatalf("send: %+v %v", res, err)
	}
	by := map[string]map[string]any{}
	for _, s := range owner.must(200, "GET", base+"/ads/conversion-sends?rule_id="+rule, nil)["data"].([]any) {
		s := s.(map[string]any)
		by[s["record_id"].(string)] = s
	}
	if by[us]["status"] != "sent" || by[ip]["status"] != "sent" ||
		by[de]["status"] != "skipped" || !strings.Contains(by[de]["reason"].(string), "EEA") ||
		by[none]["status"] != "skipped" || !strings.Contains(by[none]["reason"].(string), "unknown") {
		t.Fatalf("sends: %v", by)
	}
}
