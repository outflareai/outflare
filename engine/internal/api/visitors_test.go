package api_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestVisitorsFromEmailClicks(t *testing.T) {
	e := freshEnv(t)
	got := track.Capture(t)
	clock := &fakeClock{t: time.Now()}
	owner, base, srv := sendingSetup(t, e, clock)

	// The site has the snippet; acme.example is the workspace's site.
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "Site", "origins": []string{"https://acme.example"}})["key"].(string)
	// Northpeak is a company the CRM already has.
	np := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Northpeak",
		"identities": []map[string]any{{"namespace": "domain", "value": "northpeak.example"}}})["id"].(string)

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email,first_name\nana@northpeak.example,Ana\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Links", "daily_new_limit": 10, "send_days": []int{1, 2, 3, 4, 5, 6, 7}, "send_start": "00:00", "send_end": "23:59", "track_clicks": true,
		"steps": []map[string]any{{"subject": "Hi", "body": "Pricing: https://acme.example/pricing and a guide: https://docs.other.example/guide {{unsubscribe_url}}"}},
	})
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if len(srv.Messages()) != 1 {
		t.Fatalf("sent %d", len(srv.Messages()))
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE tracked_links SET created_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	var pricing, guide string
	if err := e.pool.QueryRow(t.Context(), `SELECT (SELECT id::text FROM tracked_links WHERE url LIKE '%acme.example/pricing'),
		(SELECT id::text FROM tracked_links WHERE url LIKE '%other.example/guide')`).Scan(&pricing, &guide); err != nil {
		t.Fatal(err)
	}
	click := func(id, ua string) string {
		req := httptest.NewRequest("GET", "/c/"+id, nil)
		req.Header.Set("User-Agent", ua)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("click: %d", rec.Code)
		}
		return rec.Header().Get("Location")
	}
	person := "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0) AppleWebKit/605.1.15 Safari/605.1.15"

	// A mail scanner gets the plain link; so does a link to someone else's site.
	if loc := click(pricing, "Mozilla/5.0 (compatible; Barracuda link scanner)"); loc != "https://acme.example/pricing" {
		t.Fatalf("scanner: %s", loc)
	}
	if loc := click(guide, person); loc != "https://docs.other.example/guide" {
		t.Fatalf("other site: %s", loc)
	}
	loc, err := url.Parse(click(pricing, person))
	if err != nil || loc.Host != "acme.example" || loc.Path != "/pricing" {
		t.Fatalf("own site: %v", loc)
	}
	token := loc.Query().Get("ofvisit")
	if token == "" {
		t.Fatalf("no token: %s", loc)
	}

	page := func(anon, path, tok string) {
		t.Helper()
		props := map[string]any{}
		if tok != "" {
			props["ofvisit"] = tok
		}
		code, out := sendSite(t, e.h, "https://acme.example", key, map[string]any{"type": "page", "anonymousId": anon,
			"properties": props, "context": map[string]any{"page": map[string]any{"path": path, "url": "https://acme.example" + path}}})
		if code != 200 || out["accepted"] != 1.0 {
			t.Fatalf("page: %d %v", code, out)
		}
	}
	// A forged token, and a stranger's page view, name nobody.
	page("b-stranger", "/pricing", "1.AAAAAAAAAAAAAAAAAAAAAA")
	page("b-ana", "/pricing", token)
	page("b-ana", "/pricing/teams", "")
	page("b-ana", "/blog/why", "")
	// The same token from another browser is ignored.
	page("b-forward", "/pricing", token)

	// A forwarded email: a colleague clicks it first and joins Ana, then
	// signs up in the product as themselves. They take their browser back,
	// and Ana's record keeps only her own.
	fwd, err := url.Parse(click(pricing, person))
	if err != nil {
		t.Fatal(err)
	}
	page("b-colleague", "/pricing", fwd.Query().Get("ofvisit"))
	var anaRec, colRec string
	if err := e.pool.QueryRow(t.Context(), `SELECT record_id::text FROM record_identities WHERE namespace = 'anonymous_id' AND value = 'b-colleague' AND valid_to IS NULL`).
		Scan(&anaRec); err != nil {
		t.Fatalf("colleague not joined from the click: %v", err)
	}
	owner.must(200, "POST", base+"/customer-events", map[string]any{"type": "identify", "userId": "u-col", "anonymousId": "b-colleague",
		"traits": map[string]any{"email": "col@northpeak.example"}})
	if err := e.pool.QueryRow(t.Context(), `SELECT record_id::text FROM record_identities WHERE namespace = 'anonymous_id' AND value = 'b-colleague' AND valid_to IS NULL`).
		Scan(&colRec); err != nil || colRec == anaRec {
		t.Fatalf("colleague's browser stayed with Ana: %v", err)
	}
	var mixed int
	if err := e.pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM record_identities WHERE record_id = $1::uuid AND valid_to IS NULL AND (namespace = 'user_id' OR value = 'col@northpeak.example'))
		+ (SELECT count(*) FROM customer_events WHERE record_id = $1::uuid AND anonymous_id = 'b-colleague')
		+ (SELECT count(*) FROM site_visitors WHERE record_id = $1::uuid AND anonymous_id = 'b-colleague')`, anaRec).Scan(&mixed); err != nil || mixed != 0 {
		t.Fatalf("colleague left on Ana's record: %d %v", mixed, err)
	}
	var moved int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM customer_events WHERE record_id = $1::uuid AND anonymous_id = 'b-colleague' AND source = 'browser'`, colRec).
		Scan(&moved); err != nil || moved != 1 {
		t.Fatalf("colleague's visit not moved: %d %v", moved, err)
	}

	var stored int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM customer_events WHERE properties ? 'ofvisit'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("token stored in events: %d %v", stored, err)
	}

	people := owner.must(200, "GET", base+"/visitors?view=people", nil)
	list := people["data"].([]any)
	if len(list) != 2 {
		t.Fatalf("people: %v", people)
	}
	ana := list[0].(map[string]any)
	if ana["email"] != "ana@northpeak.example" {
		ana = list[1].(map[string]any)
	}
	if ana["email"] != "ana@northpeak.example" || ana["pageviews"] != 3.0 || ana["key_pageviews"] != 2.0 || ana["sessions"] != 1.0 ||
		ana["last_path"] != "/blog/why" || ana["company"].(map[string]any)["id"] != np || ana["identified_by"].([]any)[0] != "email_click" {
		t.Fatalf("ana: %v", ana)
	}
	tot := people["totals"].(map[string]any)
	if tot["browsers"] != 4.0 || tot["known"] != 2.0 || tot["people"] != 2.0 || tot["companies"] != 1.0 {
		t.Fatalf("totals: %v", tot)
	}
	companies := owner.must(200, "GET", base+"/visitors", nil)["data"].([]any)
	if len(companies) != 1 || companies[0].(map[string]any)["record_id"] != np || companies[0].(map[string]any)["domain"] != "northpeak.example" ||
		companies[0].(map[string]any)["people"] != 2.0 {
		t.Fatalf("companies: %v", companies)
	}

	// Ana's earlier visits are on her record's timeline.
	var onRecord int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM customer_events WHERE record_id = $1::uuid AND source = 'browser'`, ana["record_id"]).Scan(&onRecord); err != nil || onRecord != 3 {
		t.Fatalf("events on record: %d %v", onRecord, err)
	}

	// Two key pages, a return visit: hot at a lower bar, alerted once.
	owner.must(400, "PUT", base+"/visitors/settings", map[string]any{"key_pages": []string{"pricing"}})
	owner.must(400, "PUT", base+"/visitors/settings", map[string]any{"hot_score": 0})
	set := owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"hot_score": 40, "key_pages": []string{"/pricing*", "/Demo/"}})
	if set["hot_score"] != 40.0 || len(set["key_pages"].([]any)) != 2 || set["key_pages"].([]any)[1] != "/demo/" {
		t.Fatalf("settings: %v", set)
	}
	if _, err := e.pool.Exec(t.Context(), `UPDATE site_visitors SET last_seen_at = now() - interval '2 hours'`); err != nil {
		t.Fatal(err)
	}
	page("b-ana", "/pricing", "")
	page("b-ana", "/pricing", "")
	var alerts int
	if err := e.pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action = 'visitor.became_hot'`).Scan(&alerts); err != nil || alerts != 2 {
		t.Fatalf("alerts (person and company): %d %v", alerts, err)
	}
	// Each is an alert too, with the name read from the record.
	shown := owner.must(200, "GET", base+"/alerts?kind=visitor.hot", nil)["data"].([]any)
	if len(shown) != 2 || !strings.HasSuffix(shown[0].(map[string]any)["title"].(string), "is hot on your website") {
		t.Fatalf("visitor alerts: %v", shown)
	}
	hot := owner.must(200, "GET", base+"/visitors?view=people&hot=true", nil)
	if len(hot["data"].([]any)) != 1 || hot["data"].([]any)[0].(map[string]any)["hot"] != true {
		t.Fatalf("hot: %v", hot)
	}
	seen := false
	for _, ev := range got() {
		if ev.Name == "visitor.became_hot" {
			seen = true
			if ev.Props["kind"] == nil || ev.Props["score"] == nil {
				t.Fatalf("event props: %v", ev.Props)
			}
		}
	}
	if !seen {
		t.Fatal("no visitor.became_hot event")
	}

	// The team's own domain is left out.
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"exclude_domains": []string{"northpeak.example"}})
	if l := owner.must(200, "GET", base+"/visitors?view=people", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("excluded: %v", l)
	}
	// With email clicks off, a click gets no token.
	owner.must(200, "PUT", base+"/visitors/settings", map[string]any{"email_clicks": false})
	if loc := click(pricing, person); strings.Contains(loc, "ofvisit=") {
		t.Fatalf("off: %s", loc)
	}
	owner.must(400, "GET", base+"/visitors?view=everyone", nil)
}
