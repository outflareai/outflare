package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
)

// siteConsent asks for or sends to /v1/site/consent the way the script
// does, from a page's origin.
func siteConsent(t *testing.T, h http.Handler, method, query, origin string, body map[string]any, headers ...string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		req = httptest.NewRequest(method, "/v1/site/consent"+query, strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	} else {
		req = httptest.NewRequest(method, "/v1/site/consent"+query, nil)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("no CORS header: %v", rec.Header())
	}
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestConsentBannerSettings(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"northpeak.example"}})
	key, id := k["key"].(string), k["id"].(string)
	path := base + "/site-keys/" + id + "/consent-banner"

	// Never saved: the defaults, every law and framework on, and no banner
	// on the site.
	b := owner.must(200, "GET", path, nil)
	cfg := b["config"].(map[string]any)
	fw := cfg["frameworks"].(map[string]any)
	if b["saved"] != false || cfg["enabled"] != false || cfg["regions"].(map[string]any)["eea"] != "opt_in" || cfg["regions"].(map[string]any)["us"] != "opt_out" ||
		fw["gpc"] != true || fw["google_consent_mode"] != true || fw["meta"] != true || fw["tiktok"] != true || fw["microsoft"] != true {
		t.Fatalf("defaults: %v", b)
	}
	if _, out := siteConsent(t, e.h, "GET", "?key="+key+"&lang=de-AT", "https://northpeak.example", nil); out["enabled"] != false ||
		out["texts"].(map[string]any)["accept"] != "Alle akzeptieren" || out["lang"] != "de" {
		t.Fatalf("site before saving: %v", out)
	}

	// The company's look is what the brand preset starts from.
	owner.must(400, "PATCH", base+"/context", map[string]any{"colors": map[string]any{"primary": "blue"}})
	owner.must(400, "PATCH", base+"/context", map[string]any{"logo_url": "http://northpeak.example/logo.svg"})
	ctxOut := owner.must(200, "PATCH", base+"/context", map[string]any{"logo_url": "https://northpeak.example/logo.svg",
		"colors": map[string]any{"primary": "#1D4ED8", "background": "", "text": "#111"}})
	if ctxOut["colors"].(map[string]any)["primary"] != "#1d4ed8" || ctxOut["logo_url"] != "https://northpeak.example/logo.svg" {
		t.Fatalf("context look: %v", ctxOut)
	}

	owner.must(400, "PUT", path, map[string]any{"layout": "popup"})
	owner.must(400, "PUT", path, map[string]any{"regions": map[string]any{"eea": "maybe"}})
	owner.must(400, "PUT", path, map[string]any{"unknown": true})
	owner.must(400, "PUT", path, map[string]any{"texts": map[string]any{"EN": map[string]any{"title": "Hi"}}})
	owner.must(400, "PUT", path, map[string]any{"policy_url": "javascript:alert(1)"})
	// IAB TCF isn't built.
	owner.must(400, "PUT", path, map[string]any{"frameworks": map[string]any{"tcf": map[string]any{"enabled": true}}})

	b = owner.must(200, "PUT", path, map[string]any{"enabled": true, "theme": "brand", "layout": "box", "position": "bottom_right",
		"categories": map[string]any{"preferences": false}, "policy_url": "/cookies",
		"frameworks": map[string]any{"meta": false},
		"texts":      map[string]any{"en": map[string]any{"title": "Cookies at Northpeak"}}})
	cfg = b["config"].(map[string]any)
	fw = cfg["frameworks"].(map[string]any)
	if b["saved"] != true || b["version"] != 1.0 || b["consent_version"] != 1.0 || cfg["enabled"] != true || fw["meta"] != false || fw["tiktok"] != true ||
		cfg["categories"].(map[string]any)["analytics"] != true || cfg["categories"].(map[string]any)["preferences"] != false ||
		b["look"].(map[string]any)["logo_url"] != "https://northpeak.example/logo.svg" {
		t.Fatalf("saved: %v", b)
	}
	// Asking about a new category asks everyone again.
	b = owner.must(200, "PUT", path, map[string]any{"categories": map[string]any{"preferences": true}})
	if b["version"] != 2.0 || b["consent_version"] != 2.0 || b["config"].(map[string]any)["texts"].(map[string]any)["en"].(map[string]any)["title"] != "Cookies at Northpeak" {
		t.Fatalf("new category: %v", b)
	}
	if b = owner.must(200, "POST", path+"/reask", nil); b["consent_version"] != 3.0 || b["version"] != 2.0 {
		t.Fatalf("reask: %v", b)
	}

	// Custom markup runs on the site, so it takes workspace.manage.
	manager := agentToken(t, owner, base, "Banner", "customers.manage", "customers.read")
	manager.must(403, "PUT", path, map[string]any{"custom_html": `<button data-of-consent="accept">OK</button>`})
	manager.must(200, "PUT", path, map[string]any{"layout": "bar"})
	owner.must(200, "PUT", path, map[string]any{"custom_css": ".of-cb{border-radius:0}"})
	reader := agentToken(t, owner, base, "Reader", "customers.read")
	reader.must(200, "GET", path, nil)
	reader.must(403, "PUT", path, map[string]any{"layout": "bar"})

	// What the site gets: words in the visitor's language with the
	// workspace's own on top, and the company's look.
	_, out := siteConsent(t, e.h, "GET", "?key="+key+"&lang=en-GB", "https://www.northpeak.example", nil)
	texts := out["texts"].(map[string]any)
	if out["enabled"] != true || out["consent_version"] != 3.0 || out["layout"] != "bar" || out["theme"] != "brand" ||
		out["colors"].(map[string]any)["primary"] != "#1d4ed8" || out["logo_url"] != "https://northpeak.example/logo.svg" ||
		texts["title"] != "Cookies at Northpeak" || texts["reject"] != "Reject all" || out["country"] != "" || out["custom_css"] != ".of-cb{border-radius:0}" {
		t.Fatalf("site config: %v", out)
	}
	if rec, _ := siteConsent(t, e.h, "GET", "?key=ofk_test_not_a_real_key", "", nil); rec.Code != 401 {
		t.Fatalf("unknown key: %d", rec.Code)
	}
	// The country counts only from the site's own proxy.
	if _, out := siteConsent(t, e.h, "GET", "?key="+key, "", nil, "X-Outflare-Country", "DE"); out["country"] != "" {
		t.Fatalf("country without the proxy secret: %v", out["country"])
	}
	secret := owner.must(200, "POST", base+"/site-keys/"+id+"/proxy-secret", nil)["secret"].(string)
	if _, out := siteConsent(t, e.h, "GET", "?key="+key, "", nil, "X-Outflare-Proxy", secret, "X-Outflare-Country", "de"); out["country"] != "DE" {
		t.Fatalf("country from the proxy: %v", out["country"])
	}
}

func TestConsentChoicesJoinThePerson(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"northpeak.example"}})
	key := k["key"].(string)
	owner.must(200, "PUT", base+"/site-keys/"+k["id"].(string)+"/consent-banner", map[string]any{"enabled": true, "categories": map[string]any{"preferences": false}})

	choice := func(origin string, body map[string]any) (*httptest.ResponseRecorder, map[string]any) {
		body["key"] = key
		return siteConsent(t, e.h, "POST", "", origin, body)
	}
	if rec, _ := choice("https://elsewhere.example", map[string]any{"anonymousId": "visitor-1", "analytics": true, "action": "accept_all"}); rec.Code != 403 {
		t.Fatalf("other origin: %d", rec.Code)
	}
	if rec, _ := choice("https://northpeak.example", map[string]any{"anonymousId": "visitor-1", "action": "accept_all"}); rec.Code != 400 {
		t.Fatalf("no categories: %d", rec.Code)
	}
	if rec, _ := choice("https://northpeak.example", map[string]any{"anonymousId": "visitor-1", "analytics": true, "action": "sure"}); rec.Code != 400 {
		t.Fatalf("bad action: %d", rec.Code)
	}
	// Reject all on the visitor's first page, in Europe.
	rec, out := choice("https://www.northpeak.example", map[string]any{"anonymousId": "visitor-1", "analytics": false, "marketing": false, "preferences": false,
		"action": "reject_all", "region": "eea", "version": 1, "consent_version": 1, "lang": "de", "path": "/pricing?plan=pro"})
	// Preferences isn't asked about on this banner: 2 categories and 3 ad purposes.
	if rec.Code != 200 || out["recorded"] != 5.0 {
		t.Fatalf("reject: %d %v", rec.Code, out)
	}
	var cookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "of_consent" {
			cookie = c
		}
		if c.Name == "of_aid" {
			t.Fatalf("an id cookie without analytics consent: %v", c)
		}
	}
	if cookie == nil || cookie.Domain != "northpeak.example" || !strings.HasPrefix(cookie.Value, "1.002.") {
		t.Fatalf("of_consent: %v", cookie)
	}
	var evidence map[string]any
	if err := e.pool.QueryRow(ctx, `SELECT evidence FROM consents WHERE workspace_id = $1 AND anonymous_id = 'visitor-1' AND channel = 'web' AND purpose = 'analytics'`, ws).
		Scan(&evidence); err != nil {
		t.Fatal(err)
	}
	if evidence["action"] != "reject_all" || evidence["region"] != "eea" || evidence["path"] != "/pricing" || evidence["banner_version"] != 1.0 || evidence["lang"] != "de" {
		t.Fatalf("evidence: %v", evidence)
	}

	// Ad send-back reads the anonymous visitor's choice before anyone
	// knows who they are.
	anon := "visitor-1"
	if send, _, _, err := adconv.Consent(ctx, e.pool, ws, nil, &anon, adconv.OptOut); err != nil || send {
		t.Fatalf("send-back after reject: %v %v", send, err)
	}
	// They change their mind: analytics and marketing.
	rec, _ = choice("https://northpeak.example", map[string]any{"anonymousId": "visitor-1", "analytics": true, "marketing": true, "action": "custom", "region": "eea"})
	aid := false
	for _, c := range rec.Result().Cookies() {
		aid = aid || (c.Name == "of_aid" && c.Value == "visitor-1")
	}
	if rec.Code != 200 || !aid {
		t.Fatalf("accept: %d %v", rec.Code, rec.Result().Cookies())
	}
	if send, granted, _, err := adconv.Consent(ctx, e.pool, ws, nil, &anon, adconv.OptIn); err != nil || !send || !granted {
		t.Fatalf("send-back after accept: %v %v %v", send, granted, err)
	}

	// Signing up joins every choice to their record.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u_ana", "anonymousId": "visitor-1",
		"traits": map[string]any{"email": "ana@northpeak.example"}})
	var rec2 string
	if err := e.pool.QueryRow(ctx, `SELECT record_id::text FROM record_identities WHERE workspace_id = $1 AND namespace = 'user_id' AND value = 'u_ana'`, ws).Scan(&rec2); err != nil {
		t.Fatal(err)
	}
	list := owner.must(200, "GET", base+"/records/"+rec2+"/consents", nil)["data"].([]any)
	got := map[string]string{}
	for _, x := range list {
		c := x.(map[string]any)
		got[c["channel"].(string)+"/"+c["purpose"].(string)] = c["status"].(string)
		if c["anonymous_id"] != "visitor-1" || c["source"] != "consent_banner" {
			t.Fatalf("consent: %v", c)
		}
	}
	if len(got) != 5 || got["web/analytics"] != "granted" || got["ads/ad_user_data"] != "granted" || got["web/marketing"] != "granted" {
		t.Fatalf("joined consents: %v", got)
	}
	// Once known, a new choice goes straight onto the record.
	choice("https://northpeak.example", map[string]any{"anonymousId": "visitor-1", "marketing": false, "action": "opt_out", "region": "us"})
	var status string
	if err := e.pool.QueryRow(ctx, `SELECT status FROM consents WHERE record_id = $1 AND channel = 'ads' AND purpose = 'ad_user_data'
		ORDER BY occurred_at DESC, created_at DESC LIMIT 1`, rec2).Scan(&status); err != nil || status != "withdrawn" {
		t.Fatalf("after opt-out: %s %v", status, err)
	}
	if send, _, _, _ := adconv.Consent(ctx, e.pool, ws, &rec2, &anon, adconv.OptOut); send {
		t.Fatal("send-back after the person opted out")
	}

	// Without a saved banner, a site's own banner can still record choices
	// through outflare.consent, for every category.
	k2 := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Docs", "origins": []string{"docs.northpeak.example"}})
	rec, out = siteConsent(t, e.h, "POST", "", "https://docs.northpeak.example", map[string]any{"key": k2["key"], "anonymousId": "visitor-2",
		"analytics": true, "marketing": true, "preferences": true, "action": "api"})
	if rec.Code != 200 || out["recorded"] != 6.0 {
		t.Fatalf("own banner: %d %v", rec.Code, out)
	}
}
