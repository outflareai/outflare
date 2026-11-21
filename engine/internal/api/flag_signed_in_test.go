package api_test

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// A "Signed in" rule also works for the website script: it sends the
// site's own hint with its flags request.
func TestFlagSignedInHint(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	geo := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Northpeak", "website": "https://northpeak.example"})["id"].(string)
	g := "/v1/brands/" + geo
	rule := func(v string) []map[string]any {
		return []map[string]any{{"attribute": "signed_in", "op": "is", "values": []string{v}}}
	}
	owner.must(201, "POST", g+"/flags", map[string]any{"key": "go-to-app", "status": "on", "rules": rule("true")})
	owner.must(201, "POST", g+"/flags", map[string]any{"key": "signup-banner", "status": "on", "rules": rule("false")})
	k := owner.must(201, "POST", g+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"northpeak.example"}})["key"].(string)

	ask := func(body map[string]any) map[string]any {
		t.Helper()
		body["key"], body["anonymousId"], body["url"] = k, "anon-ana-0001", "https://northpeak.example/"
		b, _ := json.Marshal(body)
		req := httptest.NewRequest("POST", "/v1/site/flags", strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("User-Agent", iphoneUA)
		req.Header.Set("Origin", "https://northpeak.example")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != 200 {
			t.Fatalf("site flags: %d %v", rec.Code, out)
		}
		return out["flags"].(map[string]any)
	}
	if f := ask(map[string]any{}); f["go-to-app"] != false || f["signup-banner"] != "on" {
		t.Fatalf("without the hint: %v", f)
	}
	if f := ask(map[string]any{"signedIn": false}); f["go-to-app"] != false || f["signup-banner"] != "on" {
		t.Fatalf("signed out: %v", f)
	}
	if f := ask(map[string]any{"signedIn": true}); f["go-to-app"] != "on" || f["signup-banner"] != false {
		t.Fatalf("signed in: %v", f)
	}
}
