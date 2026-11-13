package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// The IP lookup worker (plans/ad-tracker.md, B7c): due addresses are
// looked up on the brand's own IPQualityScore connection, and a high score
// or a data-centre network becomes a fraud reason on the click and its
// conversions. The address goes once looked up. The monthly cap skips the
// rest; a provider that is down is tried again, then given up. A
// converting click is looked up when it converts, and its postback to the
// traffic source waits for the lookup.
func TestIPLookupRuns(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	host := ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")

	var mu sync.Mutex
	var sent []url.Values
	network := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent = append(sent, r.URL.Query())
		mu.Unlock()
	}))
	defer network.Close()
	defer api.SetSourcePostbackClient(network.Client())()
	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Acme Push", "click_id": map[string]any{"param": "clickid", "token": "{clickid}"},
		"postback": map[string]any{"url": network.URL + "/pb?cid={source_click_id}&tx={transaction_id}"}})
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true,
		"traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	get := func(path, addr string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.RemoteAddr = addr
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	// click is a click a minute ago from addr, so its conversion isn't
	// too fast.
	click := func(addr, netID string) string {
		t.Helper()
		loc, _ := url.Parse(get("/l/"+code+"?clickid="+netID, addr+":51000").Header().Get("Location"))
		key := loc.Query().Get("ofclk")
		if err := e.srv.FlushClicks(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE tracking_link_clicks SET clicked_at = clicked_at - interval '1 minute' WHERE click_key = $1`, key); err != nil {
			t.Fatal(err)
		}
		return key
	}
	run := func() int {
		t.Helper()
		n, err := e.srv.LookUpDueAddresses(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	reasons := func(key string) []string {
		t.Helper()
		var r []string
		if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracking_link_clicks WHERE click_key = $1`, key).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	kept := func(key string) bool {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_click_addresses WHERE click_key = $1`, key).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n > 0
	}

	iq := owner.must(201, "POST", "/v1/workspaces/"+ws+"/integrations", map[string]any{"connector": "ipqualityscore", "credentials": map[string]any{"api_key": "test-good"}})
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"enabled": true, "integration_id": iq["id"], "converting": false, "sample_percent": 100, "monthly_cap": 2})

	// A data-centre proxy scoring 90 is flagged twice; a home network
	// isn't. Both addresses go once looked up.
	bad, good := click("198.51.100.7", "n-1"), click("203.0.113.5", "n-2")
	if n := run(); n != 2 {
		t.Fatalf("claimed %d, want 2", n)
	}
	if r := reasons(bad); !slices.Contains(r, "ip_reputation") || !slices.Contains(r, "datacenter_network") {
		t.Fatalf("flagged click's reasons: %v", r)
	}
	if r := reasons(good); len(r) != 0 {
		t.Fatalf("clean click's reasons: %v", r)
	}
	if kept(bad) || kept(good) {
		t.Fatal("an address stayed after its lookup")
	}
	var score int
	var flagged []string
	if err := e.pool.QueryRow(ctx, `SELECT score, flagged FROM tracker_ip_lookups WHERE click_key = $1 AND brand_id = $2`, bad, brand).Scan(&score, &flagged); err != nil ||
		score != 90 || len(flagged) != 2 {
		t.Fatalf("lookup result: %d %v %v", score, flagged, err)
	}

	// The cap of 2 is spent: the next is skipped, and its address goes.
	capped := click("198.51.100.8", "n-3")
	run()
	if kept(capped) || len(reasons(capped)) != 0 {
		t.Fatalf("a capped click: kept %v, reasons %v", kept(capped), reasons(capped))
	}

	// A provider that is down: tried again later, then given up.
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"monthly_cap": 100})
	down := click("192.0.2.9", "n-4")
	run()
	var attempts int
	var next time.Time
	if err := e.pool.QueryRow(ctx, `SELECT attempts, next_at FROM tracker_click_addresses WHERE click_key = $1`, down).Scan(&attempts, &next); err != nil ||
		attempts != 1 || !next.After(time.Now()) {
		t.Fatalf("after a failed try: %d %v %v", attempts, next, err)
	}
	for range 2 {
		if _, err := e.pool.Exec(ctx, `UPDATE tracker_click_addresses SET next_at = now() WHERE click_key = $1`, down); err != nil {
			t.Fatal(err)
		}
		run()
	}
	if kept(down) {
		t.Fatal("an address stayed after its last try")
	}
	failed := false
	for _, ev := range events() {
		if ev.Name == "tracker.ip_lookup_failed" && ev.ErrorCode == "connector_unavailable" && ev.WorkspaceID == ws {
			failed = true
		}
	}
	if !failed {
		t.Fatal("no tracker.ip_lookup_failed event")
	}

	// Converting clicks: kept, looked up when they convert, and the
	// postback waits for the lookup. A flagged one is then held back; a
	// clean one is sent.
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"converting": true, "sample_percent": 0, "flagged": false})
	conv1, conv2 := click("198.51.100.9", "n-5"), click("203.0.113.6", "n-6")
	if n := run(); n != 0 {
		t.Fatalf("looked up %d converting-only clicks before they converted", n)
	}
	for i, k := range []string{conv1, conv2} {
		if rec := get(token+"?cid="+k+"&payout=10&txid=T-"+string(rune('1'+i)), "192.0.2.200:1"); rec.Code != 200 {
			t.Fatalf("postback: %d %s", rec.Code, rec.Body)
		}
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_source_postbacks WHERE brand_id = $1`, brand).Scan(&queued); err != nil || queued != 0 {
		t.Fatalf("postbacks queued before their lookups: %d %v", queued, err)
	}
	if n := run(); n != 2 {
		t.Fatalf("claimed %d converted clicks, want 2", n)
	}
	var convReasons []string
	if err := e.pool.QueryRow(ctx, `SELECT fraud_reasons FROM tracker_conversions WHERE click_key = $1`, conv1).Scan(&convReasons); err != nil ||
		!slices.Contains(convReasons, "ip_reputation") {
		t.Fatalf("the flagged click's conversion: %v %v", convReasons, err)
	}
	e.runCRMImports(t)
	for range 250 {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(sent) != 1 || sent[0].Get("cid") != "n-6" {
		t.Fatalf("sent: %v", sent)
	}
	mu.Unlock()

	// Turning lookups off sends what waited for a lookup.
	conv3 := click("203.0.113.7", "n-7")
	if rec := get(token+"?cid="+conv3+"&payout=10&txid=T-3", "192.0.2.200:1"); rec.Code != 200 {
		t.Fatalf("postback: %d", rec.Code)
	}
	owner.must(200, "PUT", base+"/ip-lookups", map[string]any{"enabled": false})
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracker_source_postbacks WHERE brand_id = $1`, brand).Scan(&queued); err != nil || queued != 2 {
		t.Fatalf("postbacks after turning lookups off: %d %v", queued, err)
	}
}
