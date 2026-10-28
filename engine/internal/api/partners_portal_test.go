package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
)

// portalClient talks to a partner portal on its own host, keeping its
// session cookie.
type portalClient struct {
	t      *testing.T
	h      http.Handler
	base   string // such as https://northpeak.partners.test/
	cookie *http.Cookie
}

func (c *portalClient) do(method, path string, body any) (int, map[string]any, *httptest.ResponseRecorder) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, c.base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.cookie != nil {
		req.AddCookie(c.cookie)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.Name == "outflare_partner" {
			c.cookie = ck
		}
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec
}

func (c *portalClient) must(want int, method, path string, body any) map[string]any {
	c.t.Helper()
	code, out, _ := c.do(method, path, body)
	if code != want {
		c.t.Fatalf("%s %s: status %d, want %d: %v", method, path, code, want, out)
	}
	return out
}

var signInLink = regexp.MustCompile(`#signin=(ofpl_[A-Za-z0-9_-]+)`)

func TestPartnerPortal(t *testing.T) {
	e := freshEnvWith(t, func(o *api.Options) { o.PartnersDomain = "partners.test" })
	fake := &fakeResend{}
	rs := httptest.NewServer(fake)
	defer rs.Close()
	e.integrations.Client.BaseURLs["resend"] = rs.URL
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// The workspace's own provider, with a verified sending domain.
	owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "resend", "credentials": map[string]any{"api_key": "re_test_not_a_real_key"}})
	if _, err := e.pool.Exec(ctx, `INSERT INTO email_domains (workspace_id, provider_id, domain, status)
		SELECT workspace_id, id, 'northpeak.example', 'verified' FROM email_providers WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}

	prog := owner.must(201, "POST", base+"/partner-programs", map[string]any{"name": "Northpeak partners", "kind": "affiliate",
		"landing_url": "https://northpeak.example/", "terms_url": "https://northpeak.example/partner-terms"})
	id := prog["id"].(string)
	if prog["portal_enabled"] != false || !strings.HasSuffix(prog["portal_url"].(string), "/p/"+id+"/") {
		t.Fatalf("program = %v", prog)
	}
	anon := &portalClient{t: t, h: e.h, base: "https://northpeak.partners.test/"}
	anon.must(404, "GET", "api/program", nil)

	owner.must(400, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"settings": map[string]any{"color": "red"}})
	owner.must(400, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"subdomain": "www"})
	p := owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"enabled": true, "subdomain": "northpeak", "settings": map[string]any{
		"title": "Northpeak Partners", "color": "#0f766e", "welcome": "Earn 20% of every sale.", "email_from": "Northpeak Partners <partners@northpeak.example>",
		"signup_open": true, "signup_fields": []any{map[string]any{"key": "website", "label": "Your website", "type": "url", "required": true}},
	}})
	if p["portal_url"] != "https://northpeak.partners.test/" || p["portal"].(map[string]any)["color"] != "#0f766e" ||
		len(p["portal"].(map[string]any)["pages"].([]any)) != 6 {
		t.Fatalf("portal = %v", p)
	}

	// The portal's host serves only the portal and short links.
	_, _, page := anon.do("GET", "", nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), `var BASE = "/";`) || page.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("page = %d %s", page.Code, page.Body.String()[:200])
	}
	if code, _, _ := anon.do("GET", "v1/me", nil); code != 404 {
		t.Fatalf("app on the portal host = %d", code)
	}
	if info := anon.must(200, "GET", "api/program", nil); info["title"] != "Northpeak Partners" || info["signup_open"] != true {
		t.Fatalf("info = %v", info)
	}

	// Applying: the form's questions and the terms are checked.
	anon.must(400, "POST", "api/signup", map[string]any{"email": "eve@example.com", "name": "Eve", "accept_terms": true})
	anon.must(400, "POST", "api/signup", map[string]any{"email": "eve@example.com", "name": "Eve", "answers": map[string]any{"website": "https://eve.example"}})
	anon.must(201, "POST", "api/signup", map[string]any{"email": "eve@example.com", "name": "Eve", "accept_terms": true, "answers": map[string]any{"website": "https://eve.example"}})
	var text string
	if err := e.pool.QueryRow(ctx, `SELECT text_body FROM emails WHERE workspace_id = $1 AND 'eve@example.com' = ANY(to_addresses) ORDER BY created_at DESC LIMIT 1`, ws).Scan(&text); err != nil {
		t.Fatalf("sign-in email: %v", err)
	}
	m := signInLink.FindStringSubmatch(text)
	if m == nil || !strings.Contains(text, "https://northpeak.partners.test/#signin=") {
		t.Fatalf("email = %q", text)
	}
	partners := owner.must(200, "GET", base+"/partners?program="+id, nil)["data"].([]any)
	eve := partners[0].(map[string]any)
	if len(partners) != 1 || eve["status"] != "pending" || eve["source"] != "signup" {
		t.Fatalf("partners = %v", partners)
	}

	// The link signs in once.
	anon.must(200, "POST", "api/session", map[string]any{"token": m[1]})
	if anon.cookie == nil || anon.cookie.Path != "/" || !anon.cookie.HttpOnly {
		t.Fatalf("cookie = %v", anon.cookie)
	}
	other := &portalClient{t: t, h: e.h, base: anon.base}
	other.must(404, "POST", "api/session", map[string]any{"token": m[1]})
	me := anon.must(200, "GET", "api/me", nil)
	if me["status"] != "pending" || me["link"] != "https://northpeak.partners.test/r/"+eve["code"].(string) || me["read_only"] != false {
		t.Fatalf("me = %v", me)
	}
	other.must(401, "GET", "api/me", nil)
	if me := anon.must(200, "PATCH", "api/me", map[string]any{"payout_method": "PayPal: eve@example.com", "notify": map[string]any{"conversions": false}}); me["notify"].(map[string]any)["conversions"] != false {
		t.Fatalf("me after patch = %v", me)
	}
	if v := owner.must(200, "GET", base+"/partners/"+eve["id"].(string), nil); v["payout_method"] != "PayPal: eve@example.com" {
		t.Fatalf("payout method = %v", v["payout_method"])
	}
	// Signing in by email answers the same for anyone.
	anon.must(200, "POST", "api/sign-in", map[string]any{"email": "nobody@example.com"})
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM emails WHERE 'nobody@example.com' = ANY(to_addresses)`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("emails to a stranger = %d, %v", n, err)
	}

	// Approved, they earn; referred customers are masked.
	owner.must(200, "PATCH", base+"/partners/"+eve["id"].(string), map[string]any{"status": "active"})
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-9", "email": "fay@example.com", "ref": eve["code"], "amount_cents": 10000})
	refs := anon.must(200, "GET", "api/referrals", nil)["data"].([]any)
	if len(refs) != 1 || strings.Contains(jsonString(refs), "fay") || !strings.HasPrefix(refs[0].(map[string]any)["label"].(string), "Customer ") {
		t.Fatalf("referrals = %v", refs)
	}
	if rw := anon.must(200, "GET", "api/rewards", nil)["data"].([]any); len(rw) < 1 || rw[0].(map[string]any)["amount_cents"] != float64(2000) {
		t.Fatalf("rewards = %v", rw)
	}
	// Emails follow the partner's choices: conversions off, payouts on.
	e.runCRMImports(t)
	owner.must(200, "PATCH", base+"/partner-programs/"+id, map[string]any{"min_payout_cents": 0})
	owner.must(200, "POST", base+"/partner-rewards/"+anaRewardID(t, anon)+"/approve", map[string]any{})
	if po := owner.must(200, "POST", base+"/partner-payouts", map[string]any{"program": id})["data"].([]any); len(po) != 1 {
		t.Fatalf("payouts = %v", po)
	}
	waitEmail(t, e, ws, "eve@example.com", "A payout of 20.00 USD is on its way")
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM emails WHERE workspace_id = $1 AND subject LIKE 'You earned%'`, ws).Scan(&n); err != nil || n != 0 {
		t.Fatalf("conversion emails after opting out = %d, %v", n, err)
	}
	anon.must(200, "PATCH", "api/me", map[string]any{"notify": map[string]any{"conversions": true}})
	owner.must(201, "POST", base+"/partner-conversions", map[string]any{"type": "sale", "customer_id": "c-10", "ref": eve["code"], "amount_cents": 1000})
	waitEmail(t, e, ws, "eve@example.com", "You earned 2.00 USD from Northpeak Partners")
	owner.must(201, "POST", base+"/partner-programs/"+id+"/assets", map[string]any{"kind": "text", "title": "Email swipe", "body": "Try Northpeak."})
	owner.must(400, "POST", base+"/partner-programs/"+id+"/assets", map[string]any{"kind": "image", "title": "Banner", "url": "http://northpeak.example/b.png"})
	if a := anon.must(200, "GET", "api/assets", nil)["data"].([]any); len(a) != 1 {
		t.Fatalf("assets = %v", a)
	}
	// Short links work on the portal's host.
	if _, _, rec := anon.do("GET", "r/"+eve["code"].(string), nil); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "https://northpeak.example/?") {
		t.Fatalf("short link on the portal host = %d %s", rec.Code, rec.Header().Get("Location"))
	}

	// Seeing the portal as the partner: read-only, and recorded.
	imp := owner.must(201, "POST", base+"/partners/"+eve["id"].(string)+"/impersonate", map[string]any{})
	if !strings.HasPrefix(imp["url"].(string), "https://northpeak.partners.test/#signin=") {
		t.Fatalf("impersonation link = %v", imp["url"])
	}
	tok := signInLink.FindStringSubmatch(imp["url"].(string))
	team := &portalClient{t: t, h: e.h, base: anon.base}
	if s := team.must(200, "POST", "api/session", map[string]any{"token": tok[1]}); s["read_only"] != true {
		t.Fatalf("impersonation = %v", s)
	}
	if me := team.must(200, "GET", "api/me", nil); me["read_only"] != true || me["email"] != "eve@example.com" {
		t.Fatalf("impersonated me = %v", me)
	}
	team.must(403, "PATCH", "api/me", map[string]any{"payout_method": "x"})
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'partner.impersonated'`, ws).Scan(&n); err != nil || n != 1 {
		t.Fatalf("impersonation audit = %d, %v", n, err)
	}

	// Signing out ends the session; a closed portal answers 404, but the
	// team's preview still opens.
	anon.must(200, "POST", "api/sign-out", nil)
	anon.must(401, "GET", "api/me", nil)
	owner.must(200, "PUT", base+"/partner-programs/"+id+"/portal", map[string]any{"enabled": false})
	(&portalClient{t: t, h: e.h, base: "https://outflare.test/p/" + id + "/"}).must(404, "GET", "api/program", nil)
	team.base = "http://localhost:8080/p/" + id + "/"
	imp = owner.must(201, "POST", base+"/partners/"+eve["id"].(string)+"/impersonate", map[string]any{})
	tok = signInLink.FindStringSubmatch(imp["url"].(string))
	team.cookie = nil
	team.must(200, "POST", "api/session", map[string]any{"token": tok[1]})
	if team.cookie.Path != "/p/"+id+"/" {
		t.Fatalf("cookie path = %q", team.cookie.Path)
	}
	team.must(200, "GET", "api/program", nil)
}

func anaRewardID(t *testing.T, c *portalClient) string {
	t.Helper()
	return c.must(200, "GET", "api/rewards", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
}

// waitEmail waits for a queued email to someone with a subject.
func waitEmail(t *testing.T, e *env, ws, to, subject string) {
	t.Helper()
	for range 250 {
		var n int
		if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM emails WHERE workspace_id = $1 AND $2 = ANY(to_addresses) AND subject = $3`,
			ws, to, subject).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no email to %s: %q", to, subject)
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
