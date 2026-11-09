package api_test

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// chatCall is a request from the chat widget: no account, the visitor's
// token in X-Chat-Token.
func chatCall(t *testing.T, h http.Handler, method, path, token string, body any) (int, map[string]any) {
	t.Helper()
	var r *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	} else {
		r = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, r)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Chat-Token", token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// A visitor chats from the business's website: the conversation lands in
// the support inbox, the team's reply shows in the chat, and nobody else's
// chat is ever visible.
func TestSupportChat(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, _ := setupOwner(t, e.h)
	ws := owner.must(201, "POST", "/v1/workspaces", map[string]any{"name": "Help desk", "test": true})["id"].(string)
	base := "/v1/workspaces/" + ws
	wb := base + "/support/chat-widgets"

	// A widget needs the site it goes on.
	owner.must(400, "POST", wb, map[string]any{})
	owner.must(400, "POST", wb, map[string]any{"allowed_origins": []string{"com"}})
	owner.must(400, "POST", wb, map[string]any{"allowed_origins": []string{"northpeak.example"}, "settings": map[string]any{"color": "red", "title": "Hi"}})
	cw := owner.must(201, "POST", wb, map[string]any{"allowed_origins": []string{"northpeak.example"}})
	key := cw["key"].(string)
	if !strings.HasPrefix(key, "ofw_") || !strings.Contains(cw["embed_code"].(string), "/chat/"+key+"/widget.js") || cw["identity_verification"] != false {
		t.Fatalf("widget: %v", cw)
	}

	// The site loads the script and the frame; only the widget's sites may
	// show the frame.
	if rec := helpGet(t, e.h, "GET", "/chat/"+key+"/widget.js", "", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "/chat/"+key+"/frame") {
		t.Fatalf("loader: %d", rec.Code)
	}
	rec := helpGet(t, e.h, "GET", "/chat/"+key+"/frame", "", "")
	if csp := rec.Header().Get("Content-Security-Policy"); rec.Code != 200 || !strings.Contains(csp, "frame-ancestors https://northpeak.example https://*.northpeak.example") {
		t.Fatalf("frame: %d %s", rec.Code, csp)
	}
	if rec := helpGet(t, e.h, "GET", "/chat/ofw_aaaaaaaaaaaaaaaaaaaaaaaa/widget.js", "", ""); rec.Code != 404 {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	cb := "/chat/" + key

	// A visitor starts: a token, nothing said yet.
	code, s := chatCall(t, e.h, "POST", cb+"/session", "", map[string]any{})
	ana := s["token"].(string)
	if code != 200 || !strings.HasPrefix(ana, "ofv_") || len(s["messages"].([]any)) != 0 || s["status"] != nil {
		t.Fatalf("session: %d %v", code, s)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/messages", "", map[string]any{"body": "Hi"}); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/messages", ana, map[string]any{"body": " "}); code != 400 {
		t.Fatalf("empty: %d", code)
	}
	code, m := chatCall(t, e.h, "POST", cb+"/messages", ana, map[string]any{"body": "Where do I find my invoices?"})
	if code != 201 || m["started"] != true {
		t.Fatalf("send: %d %v", code, m)
	}
	chatCall(t, e.h, "POST", cb+"/messages", ana, map[string]any{"body": "It's for March."})

	// It's a chat in the support inbox, waiting on the team.
	l := owner.must(200, "GET", base+"/support/conversations", nil)
	if l["total"] != 1.0 {
		t.Fatalf("inbox: %v", l)
	}
	c := l["data"].([]any)[0].(map[string]any)
	if c["channel"] != "chat" || c["via"] != "chat" || c["status"] != "open" || c["messages"] != 2.0 || c["subject"] != "Where do I find my invoices?" ||
		c["visitor_seen_at"] == nil || c["identity_verified"] != false {
		t.Fatalf("conversation: %v", c)
	}
	conv := base + "/support/conversations/" + c["id"].(string)

	// The team answers; the chat shows it, and the conversation waits on
	// the visitor. A note never reaches the chat.
	owner.must(201, "POST", conv+"/notes", map[string]any{"body": "Billing question."})
	owner.must(201, "POST", conv+"/replies", map[string]any{"body": "Under Settings, Billing."})
	_, p := chatCall(t, e.h, "GET", cb+"/messages", ana, nil)
	msgs := p["messages"].([]any)
	if len(msgs) != 3 || msgs[2].(map[string]any)["from"] != "team" || msgs[2].(map[string]any)["body"] != "Under Settings, Billing." || p["status"] != "pending" {
		t.Fatalf("poll: %v", p)
	}
	after := msgs[2].(map[string]any)["created_at"].(string)
	if code, p := chatCall(t, e.h, "GET", cb+"/messages?after="+url.QueryEscape(after), ana, nil); code != 200 || len(p["messages"].([]any)) != 0 {
		t.Fatalf("after: %d %v", code, p)
	}
	if owner.must(200, "GET", conv, nil)["conversation"].(map[string]any)["status"] != "pending" {
		t.Fatal("not pending")
	}

	// Another visitor sees only their own chat, and a widget that needs an
	// email asks for it first.
	owner.must(200, "PATCH", wb+"/"+cw["id"].(string), map[string]any{"settings": map[string]any{"color": "#0f766e", "title": "Northpeak support",
		"greeting": "We reply within the hour.", "require_email": true, "show_articles": true}})
	_, s = chatCall(t, e.h, "POST", cb+"/session", "", map[string]any{})
	li := s["token"].(string)
	if len(s["messages"].([]any)) != 0 {
		t.Fatalf("someone else's chat: %v", s)
	}
	if code, out := chatCall(t, e.h, "POST", cb+"/messages", li, map[string]any{"body": "Hello"}); code != 400 || out["error"].(map[string]any)["code"] != "email_required" {
		t.Fatalf("email required: %d %v", code, out)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/messages", li, map[string]any{"body": "Hello", "email": "not an email"}); code != 400 {
		t.Fatalf("bad email: %d", code)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/messages", li, map[string]any{"body": "Do you have an API?", "email": "li@globex.example", "name": "Li Wei"}); code != 201 {
		t.Fatalf("with email: %d", code)
	}
	if code, _ := chatCall(t, e.h, "POST", cb+"/messages", li, map[string]any{"body": "Thanks"}); code != 201 {
		t.Fatalf("email known: %d", code)
	}
	l = owner.must(200, "GET", base+"/support/conversations?q=api", nil)
	if c := l["data"].([]any)[0].(map[string]any); c["contact"].(map[string]any)["email"] != "li@globex.example" || c["record_id"] == nil {
		t.Fatalf("contact: %v", c)
	}

	// A signed-in user, vouched for by the site, is trusted; a wrong
	// signature is just a visitor.
	secret := owner.must(200, "POST", wb+"/"+cw["id"].(string)+"/identity-secret", nil)["secret"].(string)
	sign := func(email string) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(email))
		return hex.EncodeToString(m.Sum(nil))
	}
	_, s = chatCall(t, e.h, "POST", cb+"/session", "", map[string]any{"identity": map[string]any{"email": "jo@stark.example", "signature": "00"}})
	if s["verified"] != false {
		t.Fatalf("bad signature: %v", s)
	}
	jo := s["token"].(string)
	if code, _ := chatCall(t, e.h, "POST", cb+"/messages", jo, map[string]any{"body": "SSO please",
		"identity": map[string]any{"email": "jo@stark.example", "name": "Jo Lee", "signature": sign("jo@stark.example")}}); code != 201 {
		t.Fatalf("signed: %d", code)
	}
	l = owner.must(200, "GET", base+"/support/conversations?q=sso", nil)
	if c := l["data"].([]any)[0].(map[string]any); c["identity_verified"] != true || c["contact"].(map[string]any)["email"] != "jo@stark.example" {
		t.Fatalf("verified: %v", c)
	}

	// Help centre answers from the widget, once a help centre is live.
	owner.must(201, "POST", base+"/knowledge/items", map[string]any{"title": "Find your invoices", "visibility": "public", "body": "Open Settings, then Billing, to download invoices."})
	if _, a := chatCall(t, e.h, "GET", cb+"/articles?q=invoices", "", nil); len(a["data"].([]any)) != 0 {
		t.Fatalf("no help centre: %v", a)
	}
	owner.must(201, "POST", base+"/help-centres", map[string]any{"enabled": true})
	if _, a := chatCall(t, e.h, "GET", cb+"/articles?q=invoices", "", nil); len(a["data"].([]any)) != 1 || a["data"].([]any)[0].(map[string]any)["url"] == nil {
		t.Fatalf("articles: %v", a)
	}

	// Turned off, the chat is gone from the site.
	owner.must(200, "PATCH", wb+"/"+cw["id"].(string), map[string]any{"enabled": false})
	if code, _ := chatCall(t, e.h, "GET", cb+"/messages", ana, nil); code != 404 {
		t.Fatalf("off: %d", code)
	}
	if l := owner.must(200, "GET", wb, nil)["data"].([]any); len(l) != 1 || l[0].(map[string]any)["identity_verification"] != true {
		t.Fatalf("list: %v", l)
	}
	owner.must(204, "DELETE", wb+"/"+cw["id"].(string), nil)
	owner.must(404, "DELETE", wb+"/"+cw["id"].(string), nil)
	for _, name := range []string{"support.chat_widget_created", "support.chat_widget_updated", "support.chat_secret_rotated", "support.chat_widget_deleted", "support.conversation_started"} {
		if len(named(got(), name)) == 0 {
			t.Errorf("no %s event", name)
		}
	}
}
