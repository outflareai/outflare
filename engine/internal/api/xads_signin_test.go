package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// X Ads signs in with OAuth 1.0a: the workspace's own app keys get a request
// token, the state rides in a cookie (X matches callback URLs exactly), and
// the callback trades the verifier for the account's access token.
func TestXAdsSignIn(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	var callbacks []string
	x := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/request_token":
			if h := r.Header.Get("Authorization"); strings.Contains(h, "oauth_callback=") {
				i := strings.Index(h, `oauth_callback="`) + len(`oauth_callback="`)
				cb, _ := url.QueryUnescape(h[i : i+strings.Index(h[i:], `"`)])
				callbacks = append(callbacks, cb)
			}
			w.Write([]byte("oauth_token=rt1&oauth_token_secret=rs1&oauth_callback_confirmed=true"))
		case "/oauth/access_token":
			if !strings.Contains(r.Header.Get("Authorization"), `oauth_verifier="v1"`) {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte("oauth_token=at1&oauth_token_secret=as1&user_id=42&screen_name=northpeak"))
		case "/12/accounts":
			w.Write([]byte(`{"data":[{"id":"18ce5","name":"Northpeak","currency":"USD","timezone":"UTC"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(x.Close)
	e.integrations.Client.BaseURLs["x_ads"] = x.URL
	e.integrations.Client.BaseURLs["x_ads.auth"] = x.URL

	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "x_ads",
		"credentials": map[string]any{"consumer_key": "x-app-key-not-real", "consumer_secret": "x-app-secret-not-real"}})
	id := conn["id"].(string)

	// Starting the sign-in hands back X's consent page and sets the state cookie.
	req := httptest.NewRequest("GET", base+"/integrations/"+id+"/sign-in?format=json&return=ads", nil)
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "/oauth/authorize?oauth_token=rt1") {
		t.Fatalf("sign-in: %d %s", rec.Code, rec.Body)
	}
	if len(callbacks) != 1 || strings.Contains(callbacks[0], "?") || !strings.HasSuffix(callbacks[0], "/v1/integrations/oauth/callback") {
		t.Fatalf("callback must be the plain URL: %v", callbacks)
	}
	var state *http.Cookie
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "outflare_oauth1_state" {
			state = ck
		}
	}
	if state == nil || state.Value == "" || !state.HttpOnly {
		t.Fatalf("state cookie: %+v", state)
	}

	back := func(query string, cookies ...*http.Cookie) *url.URL {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/integrations/oauth/callback?"+query, nil)
		req.AddCookie(owner.cookie)
		for _, ck := range cookies {
			req.AddCookie(ck)
		}
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("callback: %d %s", rec.Code, rec.Body)
		}
		u, _ := url.Parse(rec.Header().Get("Location"))
		return u
	}
	// Without the cookie the callback can't be tied to a sign-in.
	if u := back("oauth_token=rt1&oauth_verifier=v1"); u.Query().Get("connect_error") == "" {
		t.Fatalf("no cookie: %s", u)
	}
	// A token from another sign-in is refused.
	if u := back("oauth_token=other&oauth_verifier=v1", state); !strings.Contains(u.Query().Get("connect_error"), "does not match") {
		t.Fatalf("wrong token: %s", u)
	}
	if u := back("oauth_token=rt1&oauth_verifier=v1", state); u.Query().Get("signed_in") != "1" || u.Query().Get("integration") != id || !strings.HasSuffix(u.Path, "/ads") {
		t.Fatalf("signed in: %s", u)
	}
	if len(named(got(), "integration.signed_in")) != 1 {
		t.Fatal("integration.signed_in not recorded")
	}
	c := owner.must(200, "GET", base+"/integrations/"+id, nil)
	if a, _ := c["account"].(map[string]any); a["name"] != "@northpeak" || c["status"] != "connected" {
		t.Fatalf("connection after sign-in: %v", c)
	}
}
