package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/appsignin"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Signing in to the Outflare desktop app end to end: the app starts a
// sign-in with a PKCE challenge, the person approves it in the browser,
// and the app completes it with the code and its verifier. A wrong
// verifier, a reused code and an expired sign-in all fail, and the code
// works once whatever happens.
func TestAppSignIn(t *testing.T) {
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	ownerID := owner.must(200, "GET", "/v1/me", nil)["user"].(map[string]any)["id"].(string)
	app := &client{t: t, h: e.h}
	events := track.Capture(t)

	const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	start := func() (state string) {
		t.Helper()
		got := app.must(201, "POST", "/v1/auth/app/start", map[string]any{
			"code_challenge": appsignin.Challenge(verifier), "name": "Ana's MacBook", "platform": "macos", "app_version": "0.1.0"})
		state = got["state"].(string)
		if got["expires_in"] != 600.0 || got["url"] != "http://localhost:8080/signin/app?state="+url.QueryEscape(state) {
			t.Fatalf("start: %v", got)
		}
		return state
	}
	// complete plays the app's window: a plain navigation that ends in a
	// redirect, into the app or back to the sign-in page with the error.
	complete := func(code, verifier string) (location string, cookie *http.Cookie) {
		t.Helper()
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/app/complete?code="+url.QueryEscape(code)+"&code_verifier="+url.QueryEscape(verifier), nil))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("complete: %d %s", rec.Code, rec.Body)
		}
		for _, ck := range rec.Result().Cookies() {
			if ck.Name == "outflare_session" {
				cookie = ck
			}
		}
		return rec.Header().Get("Location"), cookie
	}

	// Bad starts.
	app.must(400, "POST", "/v1/auth/app/start", map[string]any{"code_challenge": "nope", "name": "x", "platform": "macos", "app_version": "0.1.0"})
	app.must(400, "POST", "/v1/auth/app/start", map[string]any{"code_challenge": appsignin.Challenge(verifier), "name": "x", "platform": "beos", "app_version": "0.1.0"})

	// Only a signed-in person sees or approves a sign-in.
	state := start()
	app.must(401, "GET", "/v1/auth/app/signins/"+state, nil)
	app.must(401, "POST", "/v1/auth/app/signins/"+state+"/approve", nil)
	owner.must(404, "GET", "/v1/auth/app/signins/unknown-state", nil)
	owner.must(404, "POST", "/v1/auth/app/signins/unknown-state/approve", nil)
	info := owner.must(200, "GET", "/v1/auth/app/signins/"+state, nil)
	if info["name"] != "Ana's MacBook" || info["platform"] != "macos" || info["approved"] != false {
		t.Fatalf("lookup: %v", info)
	}
	approved := owner.must(201, "POST", "/v1/auth/app/signins/"+state+"/approve", nil)
	code := approved["code"].(string)
	if !strings.HasPrefix(code, "ofac_") || approved["url"] != "outflare://signed-in?state="+url.QueryEscape(state)+"&code="+url.QueryEscape(code) {
		t.Fatalf("approve: %v", approved)
	}
	owner.must(410, "POST", "/v1/auth/app/signins/"+state+"/approve", nil) // approved once
	if info := owner.must(200, "GET", "/v1/auth/app/signins/"+state, nil); info["approved"] != true {
		t.Fatalf("after approval: %v", info)
	}

	// A code that reached another app is useless without the verifier,
	// and it is burnt by the attempt.
	if loc, ck := complete(code, "someone-else's-verifier"); loc != "http://localhost:8080/signin/app?error=app_signin_mismatch" || ck != nil {
		t.Fatalf("wrong verifier: %s %v", loc, ck)
	}
	if loc, _ := complete(code, verifier); loc != "http://localhost:8080/signin/app?error=not_found" {
		t.Fatalf("burnt code: %s", loc)
	}
	if loc, _ := complete("ofac_never_issued", verifier); loc != "http://localhost:8080/signin/app?error=not_found" {
		t.Fatalf("unknown code: %s", loc)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/auth/app/complete?code=ofac_x", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing verifier: %d", rec.Code)
	}

	// The happy path: the session is the owner's, and marked as the app's.
	state = start()
	code = owner.must(201, "POST", "/v1/auth/app/signins/"+state+"/approve", nil)["code"].(string)
	loc, ck := complete(code, verifier)
	if loc != "http://localhost:8080/" || ck == nil || !ck.HttpOnly {
		t.Fatalf("complete: %s %v", loc, ck)
	}
	desktop := &client{t: t, h: e.h, cookie: ck}
	if me := desktop.must(200, "GET", "/v1/me", nil); me["user"].(map[string]any)["id"] != ownerID {
		t.Fatalf("me: %v", me)
	}
	var clients []string
	rows, err := e.pool.Query(t.Context(), `SELECT coalesce(client, 'browser') FROM sessions WHERE user_id = $1 ORDER BY created_at`, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	if strings.Join(clients, ",") != "browser,app" {
		t.Fatalf("session clients: %v", clients)
	}
	if loc, _ := complete(code, verifier); loc != "http://localhost:8080/signin/app?error=not_found" {
		t.Fatalf("code used twice: %s", loc)
	}
	owner.must(404, "GET", "/v1/auth/app/signins/"+state, nil) // gone once complete

	// Expired: before approval, and an approved code the app was slow with.
	state = start()
	expire := func() {
		if _, err := e.pool.Exec(t.Context(), `UPDATE app_signins SET expires_at = now() - interval '1 minute' WHERE state = $1`, state); err != nil {
			t.Fatal(err)
		}
	}
	expire()
	owner.must(410, "GET", "/v1/auth/app/signins/"+state, nil)
	owner.must(410, "POST", "/v1/auth/app/signins/"+state+"/approve", nil)
	state = start()
	code = owner.must(201, "POST", "/v1/auth/app/signins/"+state+"/approve", nil)["code"].(string)
	expire()
	if loc, ck := complete(code, verifier); loc != "http://localhost:8080/signin/app?error=app_signin_expired" || ck != nil {
		t.Fatalf("expired code: %s %v", loc, ck)
	}
	// Starting again clears rows that expired over an hour ago.
	if _, err := e.pool.Exec(t.Context(), `UPDATE app_signins SET expires_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	start()
	var n int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM app_signins`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("rows after sweep: %d %v", n, err)
	}

	// Every step is an event; the sign-in itself says it came from the app.
	counts := map[string]int{}
	var failures []string
	for _, ev := range events() {
		counts[ev.Name]++
		switch ev.Name {
		case "app_signin.failed":
			failures = append(failures, ev.ErrorCode)
		case "app_signin.approved":
			if ev.UserID != ownerID || ev.Props["platform"] != "macos" {
				t.Errorf("approved event: %+v", ev)
			}
		case "user.signed_in":
			if ev.Props["via"] == "app" && ev.UserID != ownerID {
				t.Errorf("signed_in event: %+v", ev)
			}
		}
	}
	if counts["app_signin.started"] != 5 || counts["app_signin.approved"] != 3 {
		t.Errorf("event counts: %v", counts)
	}
	if got := strings.Join(failures, ","); got != "app_signin_mismatch,not_found,not_found,not_found,app_signin_expired" {
		t.Errorf("failures: %s", got)
	}
	var viaApp int
	for _, ev := range events() {
		if ev.Name == "user.signed_in" && ev.Props["via"] == "app" {
			viaApp++
		}
	}
	if viaApp != 1 {
		t.Errorf("signed in via app: %d", viaApp)
	}
}
