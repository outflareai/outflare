package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIdempotencyKeyReplaysTheFirstAnswer(t *testing.T) {
	h := freshServer(t)
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws
	secret := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "bot", "scopes": []string{"leads.read", "leads.manage"}})["token"].(string)

	post := func(key, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", base+"/leads", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+secret)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	id := func(rec *httptest.ResponseRecorder) string {
		var out struct{ ID string }
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out.ID
	}

	first := post("k1", `{"email":"ana@getacme-co.example","first_name":"Ana"}`)
	if first.Code != http.StatusCreated || first.Header().Get("Idempotent-Replayed") != "" {
		t.Fatalf("first: %d %s", first.Code, first.Body)
	}
	again := post("k1", `{"email":"ana@getacme-co.example","first_name":"Ana"}`)
	if again.Code != http.StatusCreated || again.Header().Get("Idempotent-Replayed") != "true" || id(again) != id(first) {
		t.Fatalf("replay: %d %q %s", again.Code, again.Header().Get("Idempotent-Replayed"), again.Body)
	}
	if again.Body.String() != first.Body.String() {
		t.Fatalf("replayed body differs:\n%s\n%s", first.Body, again.Body)
	}

	// The same key for a different request is refused, not replayed.
	other := post("k1", `{"email":"bo@getacme-co.example"}`)
	if other.Code != http.StatusUnprocessableEntity || !strings.Contains(other.Body.String(), "idempotency_key_reused") {
		t.Fatalf("reused key: %d %s", other.Code, other.Body)
	}

	// Errors are kept too: a retry of a refused request gets the same refusal.
	bad := post("k2", `{"email":"not an email"}`)
	badAgain := post("k2", `{"email":"not an email"}`)
	if bad.Code != http.StatusBadRequest || badAgain.Code != http.StatusBadRequest || badAgain.Header().Get("Idempotent-Replayed") != "true" {
		t.Fatalf("error replay: %d %d", bad.Code, badAgain.Code)
	}

	// Without a key, or with a new one, the request runs.
	if rec := post("", `{"email":"cy@getacme-co.example"}`); rec.Code != http.StatusCreated {
		t.Fatalf("no key: %d", rec.Code)
	}
	if rec := post("k3", `{"email":"ana@getacme-co.example","first_name":"Ana"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("new key: %d %s", rec.Code, rec.Body)
	} else if rec.Header().Get("Idempotent-Replayed") != "" {
		t.Fatal("a new key was replayed")
	}

	if rec := post(strings.Repeat("x", 256), `{"email":"dee@getacme-co.example"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("long key: %d", rec.Code)
	}

	// Keys are per caller: the owner's k1 is a different key from the bot's.
	code, _ := owner.do("POST", base+"/leads", map[string]any{"email": "ed@getacme-co.example"})
	if code != http.StatusCreated {
		t.Fatalf("owner: %d", code)
	}
	list := owner.must(200, "GET", base+"/leads", nil)
	if n := len(list["data"].([]any)); n != 3 {
		t.Fatalf("leads: %d, want 3 (ana, cy, ed)", n)
	}
}
