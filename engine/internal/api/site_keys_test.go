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

// sendSite posts browser events the way the snippet does: JSON as
// text/plain, from a page's origin, with no credentials.
func sendSite(t *testing.T, h http.Handler, origin, key string, batch ...map[string]any) (int, map[string]any) {
	t.Helper()
	rec := sendSiteAs(t, h, origin, key, desktopUA, false, batch...)
	var out map[string]any
	json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

const (
	desktopUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36"
	iphoneUA  = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_6 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.6 Mobile/15E148 Safari/604.1"
)

// sendSiteAs sends from a given browser, saying whether the visitor
// consented to cookies.
func sendSiteAs(t *testing.T, h http.Handler, origin, key, ua string, consent bool, batch ...map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"key": key, "batch": batch, "consent": consent})
	req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("no CORS header: %v", rec.Header())
	}
	return rec
}

func TestSiteKeyCapturesVisitsRawAndOnlyWrites(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(400, "POST", base+"/site-keys", map[string]any{"name": "Acme", "origins": []string{"https://acme.example/pricing"}})
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example", "https://*.acme.example"}})
	key := k["key"].(string)
	// The site leaves out its own experiment parameters.
	owner.must(400, "PATCH", base+"/site-keys/"+k["id"].(string), map[string]any{"excluded_params": []string{"no spaces"}})
	if k := owner.must(200, "PATCH", base+"/site-keys/"+k["id"].(string), map[string]any{"excluded_params": []string{"exp_*", "exp_*", "variant"}}); len(k["excluded_params"].([]any)) != 2 {
		t.Fatalf("excluded_params: %v", k)
	}
	if !strings.HasPrefix(key, "ofk_") || !strings.Contains(k["snippet"].(string), `data-key="`+key+`"`) {
		t.Fatalf("key: %v", k)
	}

	// A landing page with campaign tags, a click id and a referral code.
	landing := map[string]any{"type": "page", "anonymousId": "a-1", "messageId": "m-1",
		"userId": "u_someone_else", "traits": map[string]any{"email": "ann@acme.example", "email_verified": true},
		"timestamp":  "2020-01-01T00:00:00Z",
		"properties": map[string]any{"click_ids": map[string]any{"gclid": "g-1", "evil": "x"}, "ref": "PARTNER1", "ofc": "c-1"},
		"context": map[string]any{
			"page": map[string]any{"url": "https://www.acme.example/welcome?utm_source=google&gclid=g-1&token=secret-1&plan=pro&exp_hero=b&Variant=2&u=ann%40acme.example#x", "path": "/welcome",
				"search": "?utm_source=google&gclid=g-1&email=ann%40acme.example&plan=pro&code=abc&exp_hero=b", "referrer": "https://search.example/results?q=ann%40acme.example"},
			"campaign": map[string]any{"source": "google", "medium": "cpc", "name": "launch"},
			"ip":       "198.51.100.7",
		}}
	code, out := sendSite(t, e.h, "https://www.acme.example", key, landing,
		map[string]any{"type": "identify", "anonymousId": "a-1", "traits": map[string]any{"email": "ann@acme.example"}},
		map[string]any{"type": "track", "event": "Signup Started", "anonymousId": "a-1"},
		map[string]any{"type": "page"})
	if code != 200 || out["accepted"] != 2.0 || out["rejected"] != 2.0 || len(out) != 2 {
		t.Fatalf("send: %d %v", code, out)
	}
	// Outflare's own event names are not a browser's to send.
	if code, out := sendSite(t, e.h, "https://acme.example", key, map[string]any{"type": "track", "event": "payment.succeeded", "anonymousId": "a-1",
		"properties": map[string]any{"amount_minor": 100000, "currency": "usd", "provider": "stripe"}}); code != 200 || out["rejected"] != 1.0 {
		t.Fatalf("engine name from a browser: %d %v", code, out)
	}
	var props, ctxObj map[string]any
	var userID *string
	var recID *string
	var source string
	var at string
	if err := e.pool.QueryRow(ctx, `SELECT properties, context, user_id, record_id::text, source, occurred_at::date::text FROM customer_events
		WHERE workspace_id = $1 AND anonymous_id = 'a-1' AND kind = 'page'`, ws).Scan(&props, &ctxObj, &userID, &recID, &source, &at); err != nil {
		t.Fatal(err)
	}
	ids := props["click_ids"].(map[string]any)
	if ids["gclid"] != "g-1" || ids["evil"] != nil || props["ref"] != "PARTNER1" || props["ofc"] != "c-1" {
		t.Fatalf("properties: %v", props)
	}
	page := ctxObj["page"].(map[string]any)
	if page["url"] != "https://www.acme.example/welcome?gclid=g-1&plan=pro&utm_source=google" || page["search"] != "?gclid=g-1&plan=pro&utm_source=google" ||
		page["referrer"] != "https://search.example/results" || page["host"] != "www.acme.example" {
		t.Fatalf("addresses kept a token, an email address or an excluded parameter: %v", page)
	}
	if ctxObj["campaign"].(map[string]any)["source"] != "google" || ctxObj["ip"] != nil {
		t.Fatalf("context: %v", ctxObj)
	}
	if userID != nil || recID != nil || source != "browser" || at == "2020-01-01" {
		t.Fatalf("browser said who or when: %v %v %s %s", userID, recID, source, at)
	}
	var people int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM records WHERE workspace_id = $1`, ws).Scan(&people)
	if people != 0 {
		t.Fatalf("%d records made from the browser", people)
	}

	// The product's own server names the visitor: their visits join them.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u_ann", "anonymousId": "a-1",
		"traits": map[string]any{"email": "ann@acme.example"}})
	var linked int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND anonymous_id = 'a-1' AND record_id IS NOT NULL`, ws).Scan(&linked)
	if linked != 3 {
		t.Fatalf("%d events linked", linked)
	}
	// The server can't pass its events off as the browser's.
	owner.must(400, "POST", base+"/customer-events", map[string]any{"source": "browser", "type": "page", "anonymousId": "a-1"})

	// Stray origins, unknown keys and other credentials are refused.
	if code, _ := sendSite(t, e.h, "https://elsewhere.example", key, map[string]any{"type": "page", "anonymousId": "a-2"}); code != 403 {
		t.Fatalf("other origin: %d", code)
	}
	if code, _ := sendSite(t, e.h, "https://www.acme.example", "ofk_test_not_a_real_key", map[string]any{"type": "page", "anonymousId": "a-2"}); code != 401 {
		t.Fatalf("unknown key: %d", code)
	}
	// The key opens nothing else.
	bot := &client{t: t, h: e.h, bearer: key}
	bot.must(401, "GET", base+"/site-keys", nil)
	bot.must(401, "GET", base+"/customer-events", nil)
	bot.must(401, "POST", base+"/customer-events", map[string]any{"type": "page", "anonymousId": "a-3"})

	// Rotation keeps the old value working for a while; revoking stops both.
	r := owner.must(200, "POST", base+"/site-keys/"+k["id"].(string)+"/rotate", nil)
	if r["key"] == key || r["previous_expires_at"] == nil {
		t.Fatalf("rotate: %v", r)
	}
	for _, kv := range []string{key, r["key"].(string)} {
		if code, _ := sendSite(t, e.h, "https://shop.acme.example", kv, map[string]any{"type": "page", "anonymousId": "a-4"}); code != 200 {
			t.Fatalf("after rotation: %d", code)
		}
	}
	list := owner.must(200, "GET", base+"/site-keys", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["events_today"] != 7.0 {
		t.Fatalf("list: %v", list)
	}
	owner.must(200, "PATCH", base+"/site-keys/"+k["id"].(string), map[string]any{"origins": []string{}})
	if code, _ := sendSite(t, e.h, "", r["key"].(string), map[string]any{"type": "page", "anonymousId": "a-5"}); code != 200 {
		t.Fatalf("any origin: %d", code)
	}
	owner.must(204, "DELETE", base+"/site-keys/"+k["id"].(string), nil)
	for _, kv := range []string{key, r["key"].(string)} {
		if code, _ := sendSite(t, e.h, "https://acme.example", kv, map[string]any{"type": "page", "anonymousId": "a-6"}); code != 401 {
			t.Fatalf("after revoking: %d", code)
		}
	}
	var audits int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action LIKE 'site_key.%'`, ws).Scan(&audits)
	if audits != 5 {
		t.Fatalf("site_key audits: %d", audits)
	}
}

func TestSiteScriptAndPreflight(t *testing.T) {
	e := freshEnv(t)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, httptest.NewRequest("GET", "/site.js", nil))
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/javascript") || !strings.Contains(rec.Body.String(), "/v1/site/events") {
		t.Fatalf("site.js: %d %v", rec.Code, rec.Header())
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest("OPTIONS", "/v1/site/events", nil)
	req.Header.Set("Origin", "https://acme.example")
	req.Header.Set("Access-Control-Request-Method", "POST")
	e.h.ServeHTTP(rec, req)
	if rec.Code != 204 || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("preflight: %d %v", rec.Code, rec.Header())
	}
}

func TestSiteSessionsAndTouches(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak", "origins": []string{"https://*.northpeak.example", "https://northpeak.example"}})
	key := k["key"].(string)
	if strings.Contains(k["snippet"].(string), "data-domains") {
		t.Fatalf("one domain listed: %v", k["snippet"])
	}
	two := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak app", "origins": []string{"https://www.northpeak.example", "https://go.northpeak-app.example"}})
	if !strings.Contains(two["snippet"].(string), `data-domains="northpeak.example,northpeak-app.example"`) {
		t.Fatalf("domains not listed: %v", two["snippet"])
	}
	site := "https://www.northpeak.example"
	page := func(anon, url, referrer string) map[string]any {
		ctxObj := map[string]any{"page": map[string]any{"url": url, "path": strings.SplitN(strings.TrimPrefix(url, site), "?", 2)[0], "referrer": referrer},
			"timezone": "Australia/Sydney"}
		props := map[string]any{}
		if i := strings.Index(url, "?"); i >= 0 {
			for _, kv := range strings.Split(url[i+1:], "&") {
				k, v, _ := strings.Cut(kv, "=")
				switch {
				case strings.HasPrefix(k, "utm_"):
					camp, _ := ctxObj["campaign"].(map[string]any)
					if camp == nil {
						camp = map[string]any{}
						ctxObj["campaign"] = camp
					}
					name := strings.TrimPrefix(k, "utm_")
					if name == "campaign" {
						name = "name"
					}
					camp[name] = v
				case k == "gclid":
					props["click_ids"] = map[string]any{"gclid": v}
				case k == "ref":
					props["ref"] = v
				}
			}
		}
		return map[string]any{"type": "page", "anonymousId": anon, "properties": props, "context": ctxObj}
	}
	send := func(ua string, consent bool, ev map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		rec := sendSiteAs(t, e.h, site, key, ua, consent, ev)
		if rec.Code != 200 {
			t.Fatalf("send: %d %s", rec.Code, rec.Body)
		}
		return rec
	}

	// An ad click, a second page in the same visit, then a partner link.
	send(iphoneUA, false, page("a-1", site+"/pricing?utm_source=google&utm_medium=cpc&utm_campaign=spring-launch&gclid=g-1", "https://search.example/results"))
	send(iphoneUA, false, page("a-1", site+"/signup", site+"/pricing"))
	send(iphoneUA, false, page("a-1", site+"/?ref=PARTNER1", "https://blog.example/review"))
	type session struct {
		pageviews                                 int
		source, ref, country, device, browser, os string
	}
	sessions := func(anon string) []session {
		t.Helper()
		rows, err := e.pool.Query(ctx, `SELECT pageviews, utm_source, ref, country, device, browser, os FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = $2 ORDER BY started_at, ctid`, ws, anon)
		if err != nil {
			t.Fatal(err)
		}
		var out []session
		for rows.Next() {
			var s session
			if err := rows.Scan(&s.pageviews, &s.source, &s.ref, &s.country, &s.device, &s.browser, &s.os); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	got := sessions("a-1")
	if len(got) != 2 || got[0].pageviews != 2 || got[0].source != "google" || got[1].ref != "PARTNER1" ||
		got[0].country != "AU" || got[0].device != "mobile" || got[0].browser != "safari" || got[0].os != "ios" {
		t.Fatalf("sessions: %+v", got)
	}
	var tagged int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND anonymous_id = 'a-1' AND context ? 'sessionId'`, ws).Scan(&tagged)
	if tagged != 3 {
		t.Fatalf("%d events carry their session", tagged)
	}

	// Signing up gives the person their visits and touches.
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u_ana", "anonymousId": "a-1"})
	attrs := func() map[string]any {
		t.Helper()
		var a map[string]any
		if err := e.pool.QueryRow(ctx, `SELECT r.attributes FROM records r JOIN record_identities i ON i.record_id = r.id
			WHERE r.workspace_id = $1 AND i.namespace = 'user_id' AND i.value = 'u_ana'`, ws).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := attrs()
	if a["first_touch_source"] != "google" || a["first_touch_medium"] != "cpc" || a["first_touch_campaign"] != "spring-launch" ||
		a["first_touch_landing_page"] != "/pricing" || a["first_touch_referrer"] != "search.example" ||
		a["last_touch_source"] != "blog.example" || a["last_touch_medium"] != "referral" || a["referral_code"] != "PARTNER1" || a["first_touch_at"] == nil {
		t.Fatalf("touches: %v", a)
	}

	// A later visit from another site moves the last touch; a visit from
	// the site's own pages, after the first went idle, doesn't.
	if _, err := e.pool.Exec(ctx, `UPDATE site_sessions SET last_seen_at = last_seen_at - interval '1 hour', started_at = started_at - interval '1 hour' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	send(desktopUA, false, page("a-1", site+"/blog", "https://news.example/story"))
	if a := attrs(); a["last_touch_source"] != "news.example" || a["referral_code"] != "PARTNER1" || a["first_touch_source"] != "google" {
		t.Fatalf("after a referral: %v", a)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE site_sessions SET last_seen_at = last_seen_at - interval '1 hour', started_at = started_at - interval '1 hour' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	send(desktopUA, false, page("a-1", site+"/docs", "https://shop.northpeak.example/cart"))
	if a := attrs(); a["last_touch_source"] != "news.example" {
		t.Fatalf("own site counted as a referral: %v", a)
	}
	var linked int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM site_sessions WHERE workspace_id = $1 AND record_id IS NOT NULL`, ws).Scan(&linked)
	if linked != 4 {
		t.Fatalf("%d sessions on the record", linked)
	}

	// Bots are counted, not stored.
	rec := sendSiteAs(t, e.h, site, key, "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)", false, page("bot-1", site+"/", ""))
	if rec.Code != 200 {
		t.Fatalf("bot: %d", rec.Code)
	}
	var stored, bots int
	_ = e.pool.QueryRow(ctx, `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND anonymous_id = 'bot-1'`, ws).Scan(&stored)
	_ = e.pool.QueryRow(ctx, `SELECT coalesce(sum(bots), 0) FROM site_key_days WHERE workspace_id = $1`, ws).Scan(&bots)
	if stored != 0 || bots != 1 {
		t.Fatalf("bot stored %d, counted %d", stored, bots)
	}

	// With consent the server keeps the visitor's id on the site's domain.
	if c := send(desktopUA, true, page("a-2abcdefgh", site+"/", "")).Result().Cookies(); len(c) != 1 || c[0].Name != "of_aid" ||
		c[0].Value != "a-2abcdefgh" || c[0].Domain != "northpeak.example" || c[0].MaxAge < 3600*24*300 {
		t.Fatalf("cookie: %v", c)
	}
	if c := send(desktopUA, false, page("a-3abcdefgh", site+"/", "")).Result().Cookies(); len(c) != 0 {
		t.Fatalf("cookie without consent: %v", c)
	}
}

func TestSiteProxySecret(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Northpeak"})
	id := k["id"].(string)
	if k["proxy_secret_at"] != nil {
		t.Fatalf("new key has a proxy secret: %v", k)
	}
	out := owner.must(200, "POST", base+"/site-keys/"+id+"/proxy-secret", nil)
	secret, _ := out["secret"].(string)
	if !strings.HasPrefix(secret, "ofp_") || out["site_key"].(map[string]any)["proxy_secret_at"] == nil {
		t.Fatalf("proxy secret: %v", out)
	}
	list := owner.must(200, "GET", base+"/site-keys", nil)["data"].([]any)
	if strings.Contains(fmt.Sprint(list), secret) {
		t.Fatal("the secret is shown again")
	}
	if len(named(got(), "site_key.proxy_secret_created")) != 1 {
		t.Fatal("no event for the new proxy secret")
	}

	// Events forwarded with the secret are taken as usual.
	b, _ := json.Marshal(map[string]any{"key": k["key"], "batch": []map[string]any{{"type": "page", "anonymousId": "visitor-proxied-1"}}})
	req := httptest.NewRequest("POST", "/v1/site/events", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("User-Agent", desktopUA)
	req.Header.Set("X-Outflare-Proxy", secret)
	req.Header.Set("X-Outflare-Client-IP", "203.0.113.9")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"accepted":1`) {
		t.Fatalf("proxied event: %d %s", rec.Code, rec.Body)
	}

	owner.must(204, "DELETE", base+"/site-keys/"+id+"/proxy-secret", nil)
	list = owner.must(200, "GET", base+"/site-keys", nil)["data"].([]any)
	if list[0].(map[string]any)["proxy_secret_at"] != nil {
		t.Fatalf("proxy secret not removed: %v", list[0])
	}
	if len(named(got(), "site_key.proxy_secret_removed")) != 1 {
		t.Fatal("no event for the removed proxy secret")
	}
}

func TestSiteKeyBareDomainCoversSubdomains(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	// Not a site's domain, a path, http away from localhost, a port on a
	// whole domain.
	for _, o := range []string{"com", "*.co.uk", "localhost", "acme.example/pricing", "http://acme.example", "http://*.acme.example", "https://*.acme.example:8443"} {
		owner.must(400, "POST", base+"/site-keys", map[string]any{"name": "Acme", "origins": []string{o}})
	}
	k := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Acme", "origins": []string{" Acme.Example./ ", "*.acme.example", "http://localhost:3000", "https://shop.northpeak.example:8443"}})
	if got := k["origins"].([]any); len(got) != 3 || got[0] != "https://*.acme.example" || got[1] != "http://localhost:3000" || got[2] != "https://shop.northpeak.example:8443" {
		t.Fatalf("origins: %v", got)
	}
	key := k["key"].(string)
	for _, origin := range []string{"https://acme.example", "https://docs.acme.example", "https://APP.Acme.Example", "https://acme.example.", "http://localhost:3000", "https://shop.northpeak.example:8443"} {
		if code, _ := sendSite(t, e.h, origin, key, map[string]any{"type": "page", "anonymousId": "a-1"}); code != 200 {
			t.Fatalf("%s: %d", origin, code)
		}
	}
	// A whole domain with a port, saved before those were refused, still
	// takes that port only.
	if _, err := e.pool.Exec(context.Background(), `UPDATE site_keys SET origins = '{https://*.old.example:8443}' WHERE key = $1`, key); err != nil {
		t.Fatal(err)
	}
	for origin, want := range map[string]int{"https://shop.old.example:8443": 200, "https://old.example:8443": 200, "https://shop.old.example": 403, "https://shop.old.example:9443": 403} {
		if code, _ := sendSite(t, e.h, origin, key, map[string]any{"type": "page", "anonymousId": "a-1"}); code != want {
			t.Fatalf("%s: %d, want %d", origin, code, want)
		}
	}
	if _, err := e.pool.Exec(context.Background(), `UPDATE site_keys SET origins = '{https://*.acme.example,http://localhost:3000,https://shop.northpeak.example:8443}' WHERE key = $1`, key); err != nil {
		t.Fatal(err)
	}
	for _, origin := range []string{"http://acme.example", "https://evilacme.example", "https://acme.example.evil.example", "https://acme.example:8443", "http://localhost:4000", "https://shop.northpeak.example", "null"} {
		if code, _ := sendSite(t, e.h, origin, key, map[string]any{"type": "page", "anonymousId": "a-1"}); code != 403 {
			t.Fatalf("%s: %d", origin, code)
		}
	}
}
