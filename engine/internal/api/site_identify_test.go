package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A browser joins a person only with a signature its site's server made.
func TestSiteIdentify(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	rp := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Summitly", "website": "https://summitly.example"})["id"].(string)
	g := "/v1/brands/" + geo
	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})
	key, id := k["key"].(string), k["id"].(string)
	if k["identify_secret_at"] != nil {
		t.Fatalf("a new key has an identify secret: %v", k)
	}

	send := func(origin string, body map[string]any) (int, map[string]any) {
		t.Helper()
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/v1/site/identify", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", desktopUA)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("no CORS header: %v", rec.Header())
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	sign := func(secret, user string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(user))
		return hex.EncodeToString(m.Sum(nil))
	}
	linked := func(anon string) (user string) {
		t.Helper()
		_ = e.pool.QueryRow(ctx, `SELECT coalesce((SELECT u.value FROM record_identities a JOIN record_identities u ON u.record_id = a.record_id AND u.namespace = 'user_id'
			WHERE a.workspace_id = $1 AND a.namespace = 'anonymous_id' AND a.value = $2 LIMIT 1), '')`, ws, anon).Scan(&user)
		return user
	}
	// The visitor browses first.
	if code, out := sendSite(t, e.h, "https://northpeak.example", key, map[string]any{"type": "page", "anonymousId": "anon-ana-0001", "messageId": "p1",
		"context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}}); code != 200 || out["accepted"] != 1.0 {
		t.Fatalf("page: %d %v", code, out)
	}

	// Off until the site has a secret, whatever the signature.
	body := map[string]any{"key": key, "anonymousId": "anon-ana-0001", "userId": "u-ana", "signature": sign("guess", "u-ana")}
	if code, out := send("https://northpeak.example", body); code != 403 {
		t.Fatalf("without a secret: %d %v", code, out)
	}

	events := track.Capture(t)
	made := owner.must(200, "POST", g+"/site-keys/"+id+"/identify-secret", nil)
	secret := made["secret"].(string)
	if !strings.HasPrefix(secret, "ofid_") || made["site_key"].(map[string]any)["identify_secret_at"] == nil {
		t.Fatalf("made: %v", made)
	}
	if ev := named(events(), "site_key.identify_secret_created"); len(ev) != 1 {
		t.Fatalf("site_key.identify_secret_created: %+v", ev)
	}
	// The secret is never stored as it is, and never listed.
	var stored []byte
	_ = e.pool.QueryRow(ctx, `SELECT identify_secret FROM site_keys WHERE id = $1`, id).Scan(&stored)
	if len(stored) == 0 || strings.Contains(string(stored), secret) {
		t.Fatalf("the secret is stored in the clear")
	}
	if list, _ := json.Marshal(owner.must(200, "GET", g+"/site-keys", nil)); strings.Contains(string(list), secret) {
		t.Fatalf("the list shows the secret")
	}
	owner.must(404, "POST", "/v1/brands/"+rp+"/site-keys/"+id+"/identify-secret", nil)

	// A wrong signature, another id's signature and a missing one are refused alike.
	for _, sig := range []string{sign("guess", "u-ana"), sign(secret, "u-ben"), "", "zz"} {
		body["signature"] = sig
		if code, out := send("https://northpeak.example", body); code != 403 && code != 400 {
			t.Fatalf("signature %q: %d %v", sig, code, out)
		}
	}
	if got := linked("anon-ana-0001"); got != "" {
		t.Fatalf("joined without a right signature: %q", got)
	}
	// Bad input and another site's page.
	body["signature"] = sign(secret, "u-ana")
	if code, _ := send("https://evil.example", body); code != 403 {
		t.Fatalf("another origin: %d", code)
	}
	if code, _ := send("https://northpeak.example", map[string]any{"key": key, "anonymousId": "x", "userId": "u-ana", "signature": body["signature"]}); code != 400 {
		t.Fatalf("a bad anonymousId: %d", code)
	}
	if code, _ := send("https://northpeak.example", map[string]any{"key": key, "anonymousId": "anon-ana-0001", "userId": "", "signature": body["signature"]}); code != 400 {
		t.Fatalf("no userId: %d", code)
	}

	// The right one joins the browser to the person, once however often it is sent.
	for i := 0; i < 2; i++ {
		if code, out := send("https://northpeak.example", body); code != 200 || out["ok"] != true {
			t.Fatalf("identify: %d %v", code, out)
		}
	}
	if got := linked("anon-ana-0001"); got != "u-ana" {
		t.Fatalf("the browser's person: %q", got)
	}
	var people int
	_ = e.pool.QueryRow(ctx, `SELECT count(DISTINCT record_id) FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u-ana'`, ws).Scan(&people)
	if people != 1 {
		t.Fatalf("people with the user id: %d", people)
	}
	// An uppercase signature is the same signature.
	body["anonymousId"], body["signature"] = "anon-ana-0002", strings.ToUpper(sign(secret, "u-ana"))
	if code, out := send("https://northpeak.example", body); code != 200 {
		t.Fatalf("uppercase signature: %d %v", code, out)
	}

	// A new secret stops the old signatures; removing it turns identify off.
	next := owner.must(200, "POST", g+"/site-keys/"+id+"/identify-secret", nil)["secret"].(string)
	body["signature"] = sign(secret, "u-ana")
	if code, _ := send("https://northpeak.example", body); code != 403 || next == secret {
		t.Fatalf("the old secret still signs: %d", code)
	}
	body["signature"] = sign(next, "u-ana")
	if code, _ := send("https://northpeak.example", body); code != 200 {
		t.Fatalf("the new secret: %d", code)
	}
	owner.must(204, "DELETE", g+"/site-keys/"+id+"/identify-secret", nil)
	if code, _ := send("https://northpeak.example", body); code != 403 {
		t.Fatalf("after removing the secret: %d", code)
	}
	if k := owner.must(200, "GET", g+"/site-keys", nil)["data"].([]any)[0].(map[string]any); k["identify_secret_at"] != nil {
		t.Fatalf("after removing: %v", k)
	}
}
