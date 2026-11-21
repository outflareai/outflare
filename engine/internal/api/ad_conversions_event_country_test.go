package api_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

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

// A conversion a server sent without a browser takes the one the site
// script stored on the person's latest earlier event, never a later one,
// an old one or another person's.
func TestPersonBrowser(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	person := func(name, email string) string {
		t.Helper()
		return owner.must(201, "POST", "/v1/workspaces/"+ws+"/records", map[string]any{"type": "person", "name": name,
			"identities": []any{map[string]any{"namespace": "email", "value": email}}})["id"].(string)
	}
	ana, ben := person("Ana", "ana@acme.example"), person("Ben", "ben@acme.example")
	now := time.Now().UTC()
	for _, ev := range []struct {
		rec, ua string
		ago     time.Duration
	}{
		{ana, "Old/1.0", 100 * 24 * time.Hour},
		{ana, "Earlier/1.0", 48 * time.Hour},
		{ana, "Latest/2.0", 2 * time.Hour},
		{ana, "", time.Hour},
		{ana, "Later/3.0", -time.Hour},
	} {
		c := `{}`
		if ev.ua != "" {
			c = `{"userAgent": "` + ev.ua + `"}`
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO customer_events (workspace_id, brand_id, record_id, kind, name, context, occurred_at)
			VALUES ($1, outflare_first_brand($1), $2, 'page', 'page.viewed', $3::jsonb, $4)`, ws, ev.rec, c, now.Add(-ev.ago)); err != nil {
			t.Fatal(err)
		}
	}
	if ua, err := adconv.PersonBrowser(ctx, e.pool, ws, &ana, nil, now); err != nil || ua != "Latest/2.0" {
		t.Errorf("Ana's browser = %q, %v", ua, err)
	}
	if ua, err := adconv.PersonBrowser(ctx, e.pool, ws, &ana, nil, now.Add(-95*24*time.Hour)); err != nil || ua != "Old/1.0" {
		t.Errorf("Ana's browser 95 days ago = %q, %v", ua, err)
	}
	if ua, err := adconv.PersonBrowser(ctx, e.pool, ws, &ben, nil, now); err != nil || ua != "" {
		t.Errorf("Ben's browser = %q, %v", ua, err)
	}
	if ua, err := adconv.PersonBrowser(ctx, e.pool, ws, nil, nil, now); err != nil || ua != "" {
		t.Errorf("nobody's browser = %q, %v", ua, err)
	}
}
