package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A site key's script is told to load the brand's ChatGPT Ads pixel only
// once the switch is on and the brand's own connection names a pixel,
// and gets a visitor's hashed email and id only when the brand's product
// signed them in and their ad data may be sent.
func TestSiteKeyLoadsTheChatGPTPixel(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	g := "/v1/brands/" + geo
	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key, id := k["key"].(string), k["id"].(string)
	if k["chatgpt_pixel"] != false || k["chatgpt_pixel_id"] != "" {
		t.Fatalf("a new key: %v", k)
	}
	page := func(anon, msg string) map[string]any {
		t.Helper()
		code, out := sendSite(t, e.h, "https://northpeak.example", key, map[string]any{"type": "page", "anonymousId": anon, "messageId": msg,
			"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("page: %d %v", code, out)
		}
		return out
	}
	sha := func(v string) string {
		sum := sha256.Sum256([]byte(v))
		return hex.EncodeToString(sum[:])
	}

	// No ChatGPT Ads connection yet: the switch says what to do first.
	if out := owner.must(400, "PATCH", g+"/site-keys/"+id, map[string]any{"chatgpt_pixel": true}); out["error"].(map[string]any)["code"] != "invalid_request" {
		t.Fatalf("without a connection: %v", out)
	}
	if out := page("anon-ana-0001", "p1"); out["pixels"] != nil {
		t.Fatalf("pixels with the switch off: %v", out)
	}

	// The workspace connects its own ChatGPT Ads account, with a pixel.
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
		_, err := e.integrations.Insert(ctx, tx, ws, "chatgpt_ads", "api_key", "ChatGPT Ads",
			connectors.Creds{"api_key": "sk-test-not-a-real-key", "pixel_id": "px_test_1"}, connectors.Account{}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if got := owner.must(200, "GET", g+"/site-keys", nil)["data"].([]any)[0].(map[string]any); got["chatgpt_pixel"] != false || got["chatgpt_pixel_id"] != "px_test_1" {
		t.Fatalf("with a connection: %v", got)
	}
	events := track.Capture(t)
	if on := owner.must(200, "PATCH", g+"/site-keys/"+id, map[string]any{"chatgpt_pixel": true}); on["chatgpt_pixel"] != true || on["capture_clicks"] != false {
		t.Fatalf("turned on: %v", on)
	}
	if ev := named(events(), "site_key.updated"); len(ev) != 1 {
		t.Fatalf("site_key.updated: %+v", ev)
	}

	// An anonymous visitor: the pixel, and nobody's data.
	px := func(out map[string]any) map[string]any {
		t.Helper()
		p, _ := out["pixels"].(map[string]any)
		c, _ := p["chatgpt"].(map[string]any)
		if c["id"] != "px_test_1" {
			t.Fatalf("pixels: %v", out)
		}
		return c
	}
	if c := px(page("anon-ana-0001", "p2")); c["user"] != nil {
		t.Fatalf("an anonymous visitor got user data: %v", c)
	}

	// The brand's server says who the browser is. Their country is
	// unknown, so nothing is sent until they allow ad data.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u1", "anonymousId": "anon-ana-0001",
		"traits": map[string]any{"email": " Ana@Acme.example "}})
	var ana string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u1'`, ws).Scan(&ana); err != nil {
		t.Fatal(err)
	}
	if c := px(page("anon-ana-0001", "p3")); c["user"] != nil {
		t.Fatalf("user data without consent: %v", c)
	}
	owner.must(201, "POST", base+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "granted"})
	user, _ := px(page("anon-ana-0001", "p4"))["user"].(map[string]any)
	if user["email_sha256"] != sha("ana@acme.example") || user["external_id_sha256"] != sha(ana) || len(user) != 2 {
		t.Fatalf("user: %v", user)
	}
	// Another browser is still nobody.
	if c := px(page("anon-ben-0002", "p5")); c["user"] != nil {
		t.Fatalf("another browser got user data: %v", c)
	}
	// Withdrawing ad data stops it.
	owner.must(201, "POST", base+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "withdrawn"})
	if c := px(page("anon-ana-0001", "p6")); c["user"] != nil {
		t.Fatalf("user data after withdrawing: %v", c)
	}

	owner.must(200, "PATCH", g+"/site-keys/"+id, map[string]any{"chatgpt_pixel": false})
	if out := page("anon-ana-0001", "p7"); out["pixels"] != nil {
		t.Fatalf("pixels after turning it off: %v", out)
	}
}

// A site key's script is told to load Meta's or TikTok's pixel only once
// its switch is on and the brand sends that platform a conversion: the
// pixel is that conversion's, and the script learns only the conversions
// a page can send exactly as the server does.
func TestSiteKeyLoadsTheMetaAndTikTokPixels(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	brand := exec(`SELECT outflare_first_brand($1)::text`, ws)
	g := "/v1/brands/" + brand
	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key, id := k["key"].(string), k["id"].(string)
	if k["meta_pixel"] != false || k["meta_pixel_id"] != "" || k["tiktok_pixel"] != false || k["tiktok_pixel_id"] != "" {
		t.Fatalf("a new key: %v", k)
	}
	n := 0
	page := func() map[string]any {
		t.Helper()
		n++
		code, out := sendSite(t, e.h, "https://northpeak.example", key, map[string]any{"type": "page", "anonymousId": "anon-ana-0001", "messageId": fmt.Sprintf("p%d", n),
			"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("page: %d %v", code, out)
		}
		p, _ := out["pixels"].(map[string]any)
		return p
	}

	// No conversion goes to Meta yet: the switch says what to do first.
	for _, sw := range []string{"meta_pixel", "tiktok_pixel"} {
		if out := owner.must(400, "PATCH", g+"/site-keys/"+id, map[string]any{sw: true}); out["error"].(map[string]any)["code"] != "invalid_request" {
			t.Fatalf("%s without a conversion: %v", sw, out)
		}
	}

	// The brand sends Meta four conversions: a purchase with its value, a
	// sign-up from the feed (the server's alone), one with a fixed value,
	// and one to another pixel.
	integ := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'meta_ads', 'Meta Ads', '\x00') RETURNING id::text`, ws)
	acct := exec(`INSERT INTO ad_accounts (workspace_id, brand_id, integration_id, connector, external_id, name, currency) VALUES ($1, $2, $3, 'meta_ads', '123', 'Northpeak', 'EUR') RETURNING id::text`, ws, brand, integ)
	rule := func(event, target, kind, rest string) {
		t.Helper()
		exec(`INSERT INTO ad_conversion_rules (workspace_id, ad_account_id, event_name, target_id, target_name, event_type`+rest[:strings.Index(rest, "|")]+`)
			VALUES ($1, $2, $3, $4, 'Northpeak pixel', $5`+rest[strings.Index(rest, "|")+1:]+`) RETURNING id::text`, ws, acct, event, target, kind)
	}
	rule("order_completed", "1234567890", "Purchase", ", value_property, value_unit|, 'total', 'minor'")
	rule("signed_up", "1234567890", "CompleteRegistration", ", feed_role|, 'signup'")
	rule("demo_booked", "1234567890", "", ", fixed_value_micros, currency|, 50000000, 'USD'")
	rule("trial_started", "9999999999", "StartTrial", "|")

	if got := owner.must(200, "GET", g+"/site-keys", nil)["data"].([]any)[0].(map[string]any); got["meta_pixel_id"] != "1234567890" || got["tiktok_pixel_id"] != "" {
		t.Fatalf("with conversions: %v", got)
	}
	if p := page(); p != nil {
		t.Fatalf("pixels with the switch off: %v", p)
	}
	events := track.Capture(t)
	if on := owner.must(200, "PATCH", g+"/site-keys/"+id, map[string]any{"meta_pixel": true}); on["meta_pixel"] != true || on["tiktok_pixel"] != false {
		t.Fatalf("turned on: %v", on)
	}
	if ev := named(events(), "site_key.updated"); len(ev) != 1 {
		t.Fatalf("site_key.updated: %+v", ev)
	}
	owner.must(400, "PATCH", g+"/site-keys/"+id, map[string]any{"tiktok_pixel": true})

	p := page()
	meta, _ := p["meta"].(map[string]any)
	if meta["id"] != "1234567890" || meta["user"] != nil || p["tiktok"] != nil || p["chatgpt"] != nil {
		t.Fatalf("pixels: %v", p)
	}
	want := map[string]any{
		"order_completed": map[string]any{"type": "Purchase", "value_property": "total", "value_unit": "minor", "account_currency": "EUR"},
		"demo_booked":     map[string]any{"type": "Lead", "value": 50.0, "currency": "USD", "account_currency": "EUR"},
	}
	if !reflect.DeepEqual(meta["events"], want) {
		t.Fatalf("events: %v", meta["events"])
	}

	// A signed-in visitor who allows ad data: Meta's names for the hashes.
	owner.must(200, "POST", "/v1/workspaces/"+ws+"/customer-events", map[string]any{"type": "identify", "userId": "u1", "anonymousId": "anon-ana-0001",
		"traits": map[string]any{"email": "ana@acme.example"}})
	ana := exec(`SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u1'`, ws)
	owner.must(201, "POST", "/v1/workspaces/"+ws+"/records/"+ana+"/consents", map[string]any{"channel": "ads", "purpose": "ad_user_data", "status": "granted"})
	user, _ := page()["meta"].(map[string]any)["user"].(map[string]any)
	if user["em"] != connectors.HashID("ana@acme.example") || user["external_id"] != connectors.HashID(ana) || len(user) != 2 {
		t.Fatalf("user: %v", user)
	}

	owner.must(200, "PATCH", g+"/site-keys/"+id, map[string]any{"meta_pixel": false})
	if p := page(); p != nil {
		t.Fatalf("pixels after turning it off: %v", p)
	}
}
