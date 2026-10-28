package api_test

import (
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTrackingDomainAndClicks(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{t: time.Now()}
	owner, base, srv := sendingSetup(t, e, clock)

	// A tracking domain is pending until its CNAME points here.
	td := owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "https://Links.Acme-com.example/"})
	if td["domain"] != "links.acme-com.example" || td["status"] != "pending" || td["target"] != "localhost" || !strings.Contains(td["error"].(string), "CNAME") {
		t.Fatalf("pending: %v", td)
	}
	owner.must(409, "POST", base+"/tracking-domains", map[string]string{"domain": "links.acme-com.example"})
	owner.must(400, "POST", base+"/tracking-domains", map[string]string{"domain": "not a domain"})
	e.dns.setCNAME("links.acme-com.example", "localhost")
	if td = owner.must(200, "POST", base+"/tracking-domains/"+td["id"].(string)+"/check", nil); td["status"] != "verified" {
		t.Fatalf("verified: %v", td)
	}

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email,first_name\nli@one-io.example,Li\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Links", "daily_new_limit": 10, "send_days": []int{1, 2, 3, 4, 5, 6, 7}, "send_start": "00:00", "send_end": "23:59", "track_clicks": true, "unsubscribe_header": false,
		"steps": []map[string]any{{"subject": "Hi", "body": "See https://acme-com.example/pricing. Or opt out: {{unsubscribe_url}}"}},
	})
	if seq["track_clicks"] != true || seq["unsubscribe_header"] != false {
		t.Fatalf("settings: %v", seq)
	}
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("sent %d", len(msgs))
	}
	raw := string(msgs[0].Data)
	if strings.Contains(raw, "List-Unsubscribe") {
		t.Fatal("unsubscribe header should be off")
	}
	b, _ := io.ReadAll(quotedprintable.NewReader(parse(t, msgs[0]).Body))
	body := string(b)
	i := strings.Index(body, "https://links.acme-com.example/c/")
	if i < 0 || strings.Contains(body, "acme-com.example/pricing") || !strings.Contains(body, "https://links.acme-com.example/u/") {
		t.Fatalf("body: %s", body)
	}
	link := strings.TrimSuffix(strings.Fields(body[i:])[0], ".")
	path := strings.TrimPrefix(link, "https://links.acme-com.example")

	// A scanner straight after sending doesn't count; a person does.
	click := func(ua string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	if rec := click("Mozilla/5.0 (compatible; Barracuda link scanner)"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "https://acme-com.example/pricing" {
		t.Fatalf("redirect: %d %v", rec.Code, rec.Header())
	}
	got := owner.must(200, "GET", base+"/sequences/"+seqID, nil)
	if got["stats"].(map[string]any)["clicked"] != 0.0 {
		t.Fatalf("bot counted: %v", got["stats"])
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE tracked_links SET created_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	click("Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15")
	got = owner.must(200, "GET", base+"/sequences/"+seqID, nil)
	if got["stats"].(map[string]any)["clicked"] != 1.0 {
		t.Fatalf("person not counted: %v", got["stats"])
	}
	if rec := click("x"); rec.Code != http.StatusFound {
		t.Fatal("repeat click")
	}
	tot := owner.must(200, "GET", base+"/reports?to="+clock.Now().Add(time.Hour).Format(time.RFC3339), nil)["totals"].(map[string]any)
	if tot["clicks"] != 2.0 || tot["clicked_leads"] != 1.0 || tot["click_rate"] != 100.0 {
		t.Fatalf("report clicks: %v", tot)
	}
	req := httptest.NewRequest("GET", "/c/00000000-0000-0000-0000-000000000000", nil)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown link: %d", rec.Code)
	}
	owner.must(204, "DELETE", base+"/tracking-domains/"+td["id"].(string), nil)
}

// On a tracking domain the server answers only email links: the app and
// its sign-in never show under a customer's own domain.
func TestTrackingDomainServesOnlyLinks(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{t: time.Now()}
	owner, base, _ := sendingSetup(t, e, clock)
	owner.must(201, "POST", base+"/tracking-domains", map[string]string{"domain": "links.acme.example"})

	get := func(method, host, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Host = host
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	for _, path := range []string{"/", "/v1/meta", "/v1/auth/login", "/oauth/authorize", "/mcp", "/connect"} {
		if rec := get("GET", "Links.Acme.Example:443", path); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "only serves email links") {
			t.Fatalf("%s on a tracking domain: %d %s", path, rec.Code, rec.Body)
		}
	}
	if rec := get("POST", "links.acme.example", "/v1/auth/login"); rec.Code != http.StatusNotFound {
		t.Fatalf("sign-in on a tracking domain: %d", rec.Code)
	}
	// Its links still work (here: an unknown link, answered by the link route).
	if rec := get("GET", "links.acme.example", "/c/00000000-0000-0000-0000-000000000000"); rec.Code != http.StatusNotFound || strings.Contains(rec.Body.String(), "only serves email links") {
		t.Fatalf("click link on a tracking domain: %d %s", rec.Code, rec.Body)
	}
	if rec := get("GET", "links.acme.example", "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("health on a tracking domain: %d", rec.Code)
	}
	// Any other host still gets the app.
	if rec := get("GET", "other.example", "/v1/meta"); rec.Code != http.StatusOK {
		t.Fatalf("meta on another host: %d", rec.Code)
	}
}
