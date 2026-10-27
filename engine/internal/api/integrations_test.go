package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeZeroBounce accepts the key "good-key" and calls addresses at
// ok.example deliverable and everything else invalid.
type fakeZeroBounce struct{}

func (fakeZeroBounce) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("api_key") != "good-key" {
		json.NewEncoder(w).Encode(map[string]any{"Credits": "-1", "error": "Invalid API key"})
		return
	}
	switch r.URL.Path {
	case "/v2/getcredits":
		json.NewEncoder(w).Encode(map[string]any{"Credits": "2500"})
	case "/v2/validate":
		status := "invalid"
		if strings.HasSuffix(q.Get("email"), "@ok.example") {
			status = "valid"
		}
		json.NewEncoder(w).Encode(map[string]any{"status": status, "sub_status": "", "free_email": false})
	default:
		http.NotFound(w, r)
	}
}

func TestConnectWithAPIKey(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: e.h}

	cat := anon.must(200, "GET", "/v1/connectors", nil)["data"].([]any)
	var zb map[string]any
	for _, c := range cat {
		if c.(map[string]any)["id"] == "zerobounce" {
			zb = c.(map[string]any)
		}
	}
	if zb == nil || zb["status"] != "available" || zb["auth"] != "api_key" {
		t.Fatalf("zerobounce in catalog: %v", zb)
	}
	if u, _ := zb["logo_url"].(string); !strings.HasSuffix(u, "/v1/connectors/zerobounce/logo.svg?v=2") || zb["logo_dark_url"] != u+"&theme=dark" {
		t.Fatalf("logo urls: %v %v", zb["logo_url"], zb["logo_dark_url"])
	}
	for path, want := range map[string]int{"/v1/connectors/hubspot/logo.svg?theme=dark": 200, "/v1/connectors/zerobounce/logo.svg": 200, "/v1/connectors/nope/logo.svg": 404} {
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != want || (want == 200 && (rec.Header().Get("Content-Type") != "image/svg+xml" || !strings.HasPrefix(rec.Body.String(), "<svg"))) {
			t.Fatalf("%s: %d %s %.80s", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body.String())
		}
	}

	// Logos revalidate: the same ETag answers 304, so a new logo shows at once.
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/connectors/zerobounce/logo.svg?v=2", nil))
	etag := rec.Header().Get("ETag")
	if etag == "" || rec.Header().Get("Cache-Control") != "public, no-cache" {
		t.Fatalf("logo caching: %q %q", etag, rec.Header().Get("Cache-Control"))
	}
	req := httptest.NewRequest("GET", "/v1/connectors/zerobounce/logo.svg?v=2", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Fatalf("logo revalidation: %d", rec.Code)
	}

	if code, out := owner.do("POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "wrong"}}); code != 422 {
		t.Fatalf("wrong key: %d %v", code, out)
	}
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "nope", "credentials": map[string]string{}})
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "emailbison", "credentials": map[string]string{"api_key": "x"}}) // planned

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "good-key"}})
	if i["status"] != "connected" || i["account"].(map[string]any)["credits"] != 2500.0 {
		t.Fatalf("integration: %v", i)
	}
	if b, _ := json.Marshal(i); strings.Contains(string(b), "good-key") {
		t.Fatal("credentials returned")
	}
	id := i["id"].(string)
	owner.must(200, "POST", base+"/integrations/"+id+"/test", nil)

	set := owner.must(200, "PUT", base+"/verification-settings", map[string]any{"integration_id": id})
	if set["provider"] != "zerobounce" {
		t.Fatalf("settings: %v", set)
	}
	if _, err := e.verify.Verify(context.Background(), ws, []string{"ann@ok.example", "bob@bad.example"}, false); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", base+"/verifications?status=undeliverable", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["email"] != "bob@bad.example" || list[0].(map[string]any)["provider"] != "zerobounce" {
		t.Fatalf("undeliverable: %v", list)
	}
	// Paid checks are booked, one credit an address, and count toward the
	// connection's usage limits: past a limit, the free checks take over.
	if u := owner.must(200, "GET", base+"/integrations/"+id+"/usage", nil); u["used"].(map[string]any)["today"].(float64) != 2 || u["limits"].(map[string]any)["unit"] != "credit" {
		t.Fatalf("verification usage: %v", u)
	}
	owner.must(200, "PUT", base+"/integrations/"+id+"/usage/limits", map[string]any{"daily_limit": 3})
	if _, err := e.verify.Verify(context.Background(), ws, []string{"dan@ok.example", "eve@ok.example"}, false); err != nil {
		t.Fatal(err)
	}
	if u := owner.must(200, "GET", base+"/integrations/"+id+"/usage", nil); u["used"].(map[string]any)["today"].(float64) != 3 {
		t.Fatalf("usage at the limit: %v", u)
	}
	byProvider := map[string]string{}
	for _, v := range owner.must(200, "GET", base+"/verifications", nil)["data"].([]any) {
		byProvider[v.(map[string]any)["email"].(string)] = v.(map[string]any)["provider"].(string)
	}
	if byProvider["dan@ok.example"] != "zerobounce" || byProvider["eve@ok.example"] != "builtin" {
		t.Fatalf("checks past the limit: %v", byProvider)
	}
	owner.must(200, "DELETE", base+"/integrations/"+id+"/usage/limits", nil)
	owner.raw("POST", base+"/leads/import", "text/csv", "email\nAnn@ok.example\nbob@bad.example\ncat@new.example\n")
	for filter, want := range map[string]string{"deliverable": "Ann@ok.example", "undeliverable": "bob@bad.example", "unverified": "cat@new.example"} {
		got := owner.must(200, "GET", base+"/leads?verification="+filter, nil)["data"].([]any)
		if len(got) != 1 || !strings.EqualFold(got[0].(map[string]any)["email"].(string), want) {
			t.Fatalf("leads?verification=%s: %v", filter, got)
		}
	}

	owner.must(204, "DELETE", base+"/integrations/"+id, nil)
	if set := owner.must(200, "GET", base+"/verification-settings", nil); set["provider"] != "builtin" {
		t.Fatalf("after disconnect: %v", set)
	}
}

func TestConnectRequestLink(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	anon := &client{t: t, h: e.h}

	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Agent", "scopes": []string{"integrations.read", "integrations.manage"}})["token"].(string)
	agent := &client{t: t, h: e.h, bearer: tok}
	agent.must(400, "POST", base+"/connect-requests", map[string]any{"connector": "builtin"})
	req := agent.must(201, "POST", base+"/connect-requests", map[string]any{"connector": "zerobounce", "reason": "To check your 4,000 leads before the first send."})
	link, _ := req["url"].(string)
	_, secret, ok := strings.Cut(link, "/connect#")
	if !ok || !strings.HasPrefix(secret, "ofc_") || req["status"] != "pending" {
		t.Fatalf("request: %v", req)
	}
	id := req["id"].(string)
	if got := agent.must(200, "GET", base+"/connect-requests/"+id, nil); got["url"] != nil {
		t.Fatal("link returned after creation")
	}

	// The link opens only for a signed-in member who may connect services.
	anon.must(401, "POST", "/v1/connect/lookup", map[string]string{"token": secret})
	owner.must(404, "POST", "/v1/connect/lookup", map[string]string{"token": "ofc_nope"})
	join := func(ws, email, role string) *client {
		inv := owner.must(201, "POST", "/v1/workspaces/"+ws+"/invites", map[string]string{"email": email, "role": role})
		c := &client{t: t, h: e.h}
		c.must(200, "POST", "/v1/invites/accept", map[string]string{"token": inv["token"].(string), "name": "Member", "password": "a long enough password"})
		return c
	}
	elsewhere := owner.must(201, "POST", "/v1/workspaces", map[string]string{"name": "Northpeak"})["id"].(string)
	stranger := join(elsewhere, "sam@northpeak.example", "admin")
	if code, out := stranger.do("POST", "/v1/connect/lookup", map[string]string{"token": secret}); code != 403 || out["error"].(map[string]any)["code"] != "not_a_member" || strings.Contains(fmt.Sprint(out), "Acme") {
		t.Fatalf("stranger: %d %v", code, out)
	}
	stranger.must(403, "POST", "/v1/connect/complete", map[string]any{"token": secret, "credentials": map[string]string{"api_key": "good-key"}})
	stranger.must(403, "POST", "/v1/connect/oauth", map[string]string{"token": secret})
	viewer := join(ws, "ola@acme.example", "viewer")
	viewer.must(403, "POST", "/v1/connect/lookup", map[string]string{"token": secret})
	(&client{t: t, h: e.h, bearer: tok}).must(403, "POST", "/v1/connect/lookup", map[string]string{"token": secret})

	info := owner.must(200, "POST", "/v1/connect/lookup", map[string]string{"token": secret})
	// The page names who asked: here, the person whose token the agent uses.
	if info["requested_by"] != "agent" || info["requested_by_email"] != "jack@example.com" || info["workspace_name"] != "Acme Outbound" || info["connector"].(map[string]any)["id"] != "zerobounce" {
		t.Fatalf("lookup: %v", info)
	}
	if code, _ := owner.do("POST", "/v1/connect/complete", map[string]any{"token": secret, "credentials": map[string]string{"api_key": "wrong"}}); code != 422 {
		t.Fatalf("wrong key: %d", code)
	}
	owner.must(201, "POST", "/v1/connect/complete", map[string]any{"token": secret, "credentials": map[string]string{"api_key": "good-key"}})
	// It is still the person whose agent asked for it.
	ev := events()
	if c := named(ev, "integration.connected"); len(c) != 1 || c[0].UserID == "" || c[0].UserID != named(ev, "user.signed_up")[0].UserID {
		t.Fatalf("integration.connected: %+v", c)
	}
	owner.must(410, "POST", "/v1/connect/complete", map[string]any{"token": secret, "credentials": map[string]string{"api_key": "good-key"}})
	owner.must(410, "POST", "/v1/connect/lookup", map[string]string{"token": secret})

	got := agent.must(200, "GET", base+"/connect-requests/"+id, nil)
	if got["status"] != "connected" || got["integration_id"] == nil {
		t.Fatalf("after connecting: %v", got)
	}
	if set := owner.must(200, "GET", base+"/verification-settings", nil); set["provider"] != "zerobounce" {
		t.Fatalf("verification connector should take over: %v", set)
	}

	// Cancelled links stop working.
	other := agent.must(201, "POST", base+"/connect-requests", map[string]any{"connector": "neverbounce"})
	_, secret2, _ := strings.Cut(other["url"].(string), "#")
	agent.must(204, "DELETE", base+"/connect-requests/"+other["id"].(string), nil)
	owner.must(410, "POST", "/v1/connect/lookup", map[string]string{"token": secret2})

	// A token without integrations.manage cannot ask.
	ro := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"integrations.read"}})["token"].(string)
	(&client{t: t, h: e.h, bearer: ro}).must(403, "POST", base+"/connect-requests", map[string]any{"connector": "zerobounce"})
}
