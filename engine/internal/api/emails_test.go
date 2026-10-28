package api_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/emails"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeResend answers like Resend's API and keeps what was sent.
type fakeResend struct {
	mu       sync.Mutex
	sent     []map[string]any
	verified bool
	n        int
}

var resendSecret = make([]byte, 32)

func (f *fakeResend) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Header.Get("Authorization") != "Bearer re_test_not_a_real_key" {
		w.WriteHeader(401)
		io.WriteString(w, `{"name":"restricted_api_key","message":"API key is invalid"}`)
		return
	}
	domain := func() string {
		status := "not_started"
		if f.verified {
			status = "verified"
		}
		return `{"id":"d_1","name":"example.com","status":"` + status + `","records":[
			{"record":"DKIM","name":"resend._domainkey","type":"TXT","value":"p=KEY","status":"not_started"},
			{"record":"SPF","name":"send","type":"MX","value":"feedback-smtp.us-east-1.amazonses.com","priority":10,"status":"not_started"},
			{"record":"SPF","name":"send","type":"TXT","value":"v=spf1 include:amazonses.com ~all","status":"not_started"}]}`
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/domains":
		io.WriteString(w, `{"data":[]}`)
	case r.Method == "POST" && r.URL.Path == "/domains":
		io.WriteString(w, domain())
	case r.Method == "POST" && r.URL.Path == "/domains/d_1/verify":
		io.WriteString(w, `{}`)
	case r.Method == "GET" && r.URL.Path == "/domains/d_1":
		io.WriteString(w, domain())
	case r.Method == "POST" && r.URL.Path == "/emails":
		var m map[string]any
		json.NewDecoder(r.Body).Decode(&m)
		m["idempotency_key"] = r.Header.Get("Idempotency-Key")
		f.sent = append(f.sent, m)
		f.n++
		io.WriteString(w, `{"id":"re_msg_`+strconv.Itoa(f.n)+`"}`)
	case r.Method == "POST" && r.URL.Path == "/webhooks":
		io.WriteString(w, `{"id":"wh_1","signing_secret":"whsec_`+base64.StdEncoding.EncodeToString(resendSecret)+`"}`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"not found"}`)
	}
}

func (f *fakeResend) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sent[len(f.sent)-1]
}

// resendEvent posts a signed Resend webhook to the callback path.
func resendEvent(t *testing.T, h http.Handler, path, typ, messageID, to string, extra map[string]any) (int, map[string]any) {
	t.Helper()
	data := map[string]any{"email_id": messageID, "to": []string{to}}
	for k, v := range extra {
		data[k] = v
	}
	body, _ := json.Marshal(map[string]any{"type": typ, "created_at": time.Now().UTC().Format(time.RFC3339), "data": data})
	id, ts := "msg_"+typ+messageID, strconv.FormatInt(time.Now().Unix(), 10)
	m := hmac.New(sha256.New, resendSecret)
	m.Write([]byte(id + "." + ts + "." + string(body)))
	req := httptest.NewRequest("POST", path, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("svix-id", id)
	req.Header.Set("svix-timestamp", ts)
	req.Header.Set("svix-signature", "v1,"+base64.StdEncoding.EncodeToString(m.Sum(nil)))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func TestLifecycleEmail(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	fake := &fakeResend{}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	// The jobs the server queues, run by hand.
	svc := &emails.Service{Pool: e.pool, Store: e.integrations, Box: e.box, PublicURL: "https://outflare.test",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Resolver: e.dns}

	ov := owner.must(200, "GET", base+"/email-overview", nil)
	if ov["setup"].(map[string]any)["providers"].(float64) != 0 || len(ov["daily"].([]any)) != 30 {
		t.Fatalf("overview = %v", ov)
	}
	code, out := owner.do("POST", base+"/emails", map[string]any{"to": "a@example.org", "from": "x@example.com", "subject": "Hi", "text": "Hello"})
	if code != 409 || out["error"].(map[string]any)["code"] != "no_email_provider" {
		t.Fatalf("send with no provider = %d %v", code, out)
	}

	// Guides, with a prompt that keeps keys out of the chat.
	guides := owner.must(200, "GET", base+"/email-provider-guides?domain=example.com", nil)["data"].([]any)
	if len(guides) != 3 {
		t.Fatalf("guides = %d", len(guides))
	}
	ses := guides[0].(map[string]any)
	if ses["connector"] != "amazon_ses" || !strings.Contains(ses["agent_prompt"].(string), "Never ask me to paste") ||
		!strings.Contains(ses["agent_prompt"].(string), "ses:SendEmail") || !strings.Contains(ses["agent_prompt"].(string), "/providers/amazon_ses") {
		t.Fatalf("SES guide = %v", ses)
	}

	// Connecting Resend makes a provider, the default.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	providers := owner.must(200, "GET", base+"/email-providers", nil)["data"].([]any)
	if len(providers) != 1 {
		t.Fatalf("providers = %v", providers)
	}
	p := providers[0].(map[string]any)
	pid := p["id"].(string)
	if p["connector"] != "resend" || p["is_default"] != true || p["events_status"] != "not_set_up" {
		t.Fatalf("provider = %v", p)
	}
	if err := svc.SetUpEvents(ctx, pid); err != nil {
		t.Fatal(err)
	}
	if p := owner.must(200, "GET", base+"/email-providers/"+pid, nil); p["events_status"] != "active" {
		t.Fatalf("events = %v", p)
	}

	// A sending domain: registered by the job, verified once Resend says so.
	d := owner.must(201, "POST", base+"/email-provider-domains", map[string]any{"domain": "https://Example.com/pricing"})
	did := d["id"].(string)
	if d["status"] != "pending" || d["domain"] != "example.com" {
		t.Fatalf("domain = %v", d)
	}
	owner.must(409, "POST", base+"/email-provider-domains", map[string]any{"domain": "example.com"})
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	d = owner.must(200, "GET", base+"/email-provider-domains/"+did, nil)
	recs := d["records"].([]any)
	if d["status"] != "pending" || len(recs) != 4 || recs[0].(map[string]any)["name"] != "resend._domainkey.example.com" ||
		recs[0].(map[string]any)["status"] != "missing" || recs[3].(map[string]any)["purpose"] != "dmarc" {
		t.Fatalf("records = %v", d)
	}
	e.dns.setTXT("resend._domainkey.example.com", []string{"p=KEY"})
	fake.verified = true
	if err := svc.RunDomainCheck(ctx, did); err != nil {
		t.Fatal(err)
	}
	d = owner.must(200, "GET", base+"/email-provider-domains/"+did, nil)
	if d["status"] != "verified" || d["records"].([]any)[0].(map[string]any)["status"] != "verified" {
		t.Fatalf("verified domain = %v", d)
	}

	// A template, published before it sends.
	tpl := owner.must(201, "POST", base+"/email-templates", map[string]any{
		"name": "Password reset", "subject": "Reset your {{product|Acme}} password",
		"html":      `<p>Hi {{first_name|there}}, <a href="{{reset_url}}">reset it</a>.</p>`,
		"variables": []map[string]any{{"name": "reset_url", "required": true, "example": "https://acme.test/r/1"}},
	})
	if tpl["handle"] != "password-reset" || len(tpl["variables"].([]any)) != 3 || tpl["published_version"] != nil {
		t.Fatalf("template = %v", tpl)
	}
	owner.must(400, "POST", base+"/email-templates", map[string]any{"name": "Broken", "subject": "{{#if x}}open"})
	send := map[string]any{"to": "ana@example.org", "from": "Acme <billing@example.com>", "template": "password-reset",
		"data": map[string]any{"first_name": "Ana", "reset_url": "https://acme.test/r/9"}}
	code, out = owner.do("POST", base+"/emails", send)
	if code != 409 || out["error"].(map[string]any)["code"] != "template_not_published" {
		t.Fatalf("unpublished = %d %v", code, out)
	}
	if v := owner.must(200, "POST", base+"/email-templates/password-reset/publish", map[string]any{"note": "first"}); v["published_version"].(float64) != 1 || v["unpublished_changes"] != false {
		t.Fatalf("published = %v", v)
	}
	if vs := owner.must(200, "GET", base+"/email-templates/password-reset/versions", nil)["data"].([]any); len(vs) != 1 || vs[0].(map[string]any)["note"] != "first" || vs[0].(map[string]any)["published_by"] != "Jack" {
		t.Fatalf("versions = %v", vs)
	}
	prev := owner.must(200, "POST", base+"/email-templates/preview", map[string]any{"subject": tpl["subject"], "html": tpl["html"], "variables": tpl["variables"]})
	if !strings.Contains(prev["html"].(string), "https://acme.test/r/1") || prev["subject"] != "Reset your Acme password" {
		t.Fatalf("preview = %v", prev)
	}
	code, out = owner.do("POST", base+"/emails", map[string]any{"to": "ana@example.org", "from": "billing@example.com", "template": "password-reset"})
	if code != 422 || out["error"].(map[string]any)["code"] != "template_variables_missing" {
		t.Fatalf("missing variables = %d %v", code, out)
	}
	code, out = owner.do("POST", base+"/emails", map[string]any{"to": "ana@example.org", "from": "billing@other.example", "subject": "Hi", "text": "x"})
	if code != 409 || out["error"].(map[string]any)["code"] != "email_domain_not_verified" {
		t.Fatalf("unverified domain = %d %v", code, out)
	}

	// Sending: queued, handed to Resend by the job, delivered by its event.
	em := owner.must(202, "POST", base+"/emails", send)
	eid := em["id"].(string)
	if em["status"] != "queued" || em["template_handle"] != "password-reset" || em["template_version"].(float64) != 1 {
		t.Fatalf("queued = %v", em)
	}
	if err := svc.Send(ctx, eid, false); err != nil {
		t.Fatal(err)
	}
	m := fake.last()
	if m["subject"] != "Reset your Acme password" || !strings.Contains(m["html"].(string), `Hi Ana, <a href="https://acme.test/r/9">`) ||
		m["idempotency_key"] != "outflare-"+eid || m["text"] == "" {
		t.Fatalf("sent = %v", m)
	}
	if v := owner.must(200, "GET", base+"/emails/"+eid, nil); v["status"] != "sent" || v["provider_message_id"] != "re_msg_1" {
		t.Fatalf("sent email = %v", v)
	}
	if err := svc.Send(ctx, eid, false); err != nil || len(fake.sent) != 1 {
		t.Fatalf("a second run sent again: %v %d", err, len(fake.sent))
	}

	cb := owner.must(200, "GET", base+"/email-providers/"+pid+"/callback", nil)["url"].(string)
	u, _ := url.Parse(cb)
	if code, out := resendEvent(t, e.h, u.Path, "email.delivered", "re_msg_1", "ana@example.org", nil); code != 200 || out["received"].(float64) != 1 {
		t.Fatalf("callback = %d %v", code, out)
	}
	if code, out := resendEvent(t, e.h, u.Path, "email.delivered", "re_msg_1", "ana@example.org", nil); code != 200 || out["received"].(float64) != 0 {
		t.Fatalf("repeated callback = %d %v", code, out)
	}
	if code, _ := resendEvent(t, e.h, u.Path+"x", "email.delivered", "re_msg_1", "ana@example.org", nil); code != 404 {
		t.Fatalf("wrong token = %d", code)
	}
	bad := httptest.NewRequest("POST", u.Path, strings.NewReader(`{"type":"email.delivered"}`))
	bad.Header.Set("svix-id", "x")
	bad.Header.Set("svix-timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	bad.Header.Set("svix-signature", "v1,AAAA")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, bad)
	if rec.Code != 400 {
		t.Fatalf("bad signature = %d", rec.Code)
	}
	v := owner.must(200, "GET", base+"/emails/"+eid, nil)
	types := []string{}
	for _, ev := range v["events"].([]any) {
		types = append(types, ev.(map[string]any)["type"].(string))
	}
	if v["status"] != "delivered" || v["delivered_at"] == nil || strings.Join(types, ",") != "queued,sent,delivered" {
		t.Fatalf("delivered = %v %v", v["status"], types)
	}

	// A hard bounce suppresses the address; the next email to it is not sent.
	b := owner.must(202, "POST", base+"/emails", map[string]any{"to": "gone@example.org", "from": "billing@example.com", "subject": "Receipt", "html": "<p>Thanks</p>"})
	if err := svc.Send(ctx, b["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	resendEvent(t, e.h, u.Path, "email.bounced", "re_msg_2", "gone@example.org", map[string]any{"bounce": map[string]any{"type": "Permanent", "message": "550 no such user"}})
	if v := owner.must(200, "GET", base+"/emails/"+b["id"].(string), nil); v["status"] != "bounced" || v["bounce_type"] != "hard" {
		t.Fatalf("bounced = %v", v)
	}
	again := owner.must(202, "POST", base+"/emails", map[string]any{"to": "gone@example.org", "from": "billing@example.com", "subject": "Receipt", "text": "Thanks"})
	if err := svc.Send(ctx, again["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if v := owner.must(200, "GET", base+"/emails/"+again["id"].(string), nil); v["status"] != "suppressed" || len(fake.sent) != 2 {
		t.Fatalf("to a bounced address = %v (%d sent)", v, len(fake.sent))
	}

	// Lifecycle email carries a one-click unsubscribe; transactional email
	// still reaches people who used it.
	l := owner.must(202, "POST", base+"/emails", map[string]any{"to": "ben@example.org", "from": "news@mail.example.com", "subject": "Tips", "html": "<p>Welcome</p>", "purpose": "lifecycle"})
	if err := svc.Send(ctx, l["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	m = fake.last()
	hdr := m["headers"].(map[string]any)
	unsub, _ := strings.CutPrefix(strings.Trim(hdr["List-Unsubscribe"].(string), "<>"), "https://outflare.test")
	if hdr["List-Unsubscribe-Post"] != "List-Unsubscribe=One-Click" || !strings.HasPrefix(unsub, "/u/e.") || !strings.Contains(m["html"].(string), "Unsubscribe</a>") {
		t.Fatalf("lifecycle headers = %v", m)
	}
	anon := &client{t: t, h: e.h}
	if code, _ := anon.raw("POST", unsub, "application/x-www-form-urlencoded", "List-Unsubscribe=One-Click"); code != 200 {
		t.Fatalf("unsubscribe = %d", code)
	}
	var reason string
	if err := e.pool.QueryRow(ctx, `SELECT reason FROM suppressions WHERE workspace_id = $1 AND value = 'ben@example.org'`, ws).Scan(&reason); err != nil || reason != "unsubscribed" {
		t.Fatalf("suppression = %q %v", reason, err)
	}
	tx := owner.must(202, "POST", base+"/emails", map[string]any{"to": "ben@example.org", "from": "billing@example.com", "subject": "Receipt", "text": "Thanks"})
	if err := svc.Send(ctx, tx["id"].(string), false); err != nil {
		t.Fatal(err)
	}
	if v := owner.must(200, "GET", base+"/emails/"+tx["id"].(string), nil); v["status"] != "sent" {
		t.Fatalf("transactional to an unsubscribed address = %v", v)
	}

	// Scheduled emails can be cancelled until they go.
	later := owner.must(202, "POST", base+"/emails", map[string]any{"to": "cy@example.org", "from": "billing@example.com", "subject": "Later", "text": "x", "send_at": time.Now().Add(time.Hour).Format(time.RFC3339)})
	if later["status"] != "scheduled" {
		t.Fatalf("scheduled = %v", later)
	}
	owner.must(200, "POST", base+"/emails/"+later["id"].(string)+"/cancel", nil)
	code, out = owner.do("POST", base+"/emails/"+later["id"].(string)+"/cancel", nil)
	if code != 409 || out["error"].(map[string]any)["code"] != "email_not_cancellable" {
		t.Fatalf("cancel twice = %d %v", code, out)
	}

	list := owner.must(200, "GET", base+"/emails?status=sent&limit=1", nil)
	if len(list["data"].([]any)) != 1 || list["next_cursor"] == nil {
		t.Fatalf("list = %v", list)
	}
	if n := len(owner.must(200, "GET", base+"/emails?to=gone@example.org", nil)["data"].([]any)); n != 2 {
		t.Fatalf("by recipient = %d", n)
	}
	ov = owner.must(200, "GET", base+"/email-overview", nil)
	setup := ov["setup"].(map[string]any)
	if setup["providers"].(float64) != 1 || setup["domains_verified"].(float64) != 1 || setup["sent"] != true || ov["totals_30d"].(map[string]any)["bounced"].(float64) != 1 {
		t.Fatalf("overview = %v", ov)
	}

	// Pausing the provider holds sends.
	owner.must(200, "PATCH", base+"/email-providers/"+pid, map[string]any{"status": "paused"})
	held := owner.must(202, "POST", base+"/emails", map[string]any{"to": "dee@example.org", "from": "billing@example.com", "subject": "Held", "text": "x"})
	if err := svc.Send(ctx, held["id"].(string), false); err == nil {
		t.Fatal("a paused provider sent")
	}
	if err := svc.Send(ctx, held["id"].(string), true); err != nil {
		t.Fatal(err)
	}
	if v := owner.must(200, "GET", base+"/emails/"+held["id"].(string), nil); v["status"] != "failed" {
		t.Fatalf("paused to the end = %v", v)
	}

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, n := range []string{"email.queued", "email.sent", "email.delivered", "email.bounced", "email.suppressed", "email.unsubscribed", "email.cancelled", "email_domain.added", "email_domain.verified", "email_template.created", "email_template.published", "email_provider.paused", "email.failed"} {
		if !names[n] {
			t.Errorf("no %s event", n)
		}
	}
}
