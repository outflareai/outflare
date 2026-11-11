package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Conversions back to traffic sources (plans/ad-tracker.md, T7): a tracker
// conversion on a campaign's click is sent to its traffic source's
// postback URL by the source's rules (share, decimals, minimum, types,
// statuses, re-sending changes), once per state, from a job.
func TestTrackerSourcePostbacks(t *testing.T) {
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

	// The network's postback endpoint: it takes approvals and refuses
	// declines.
	var mu sync.Mutex
	var got []url.Values
	network := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.URL.Query())
		mu.Unlock()
		if r.URL.Query().Get("st") == "declined" {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer network.Close()
	defer api.SetSourcePostbackClient(network.Client())()

	rules := map[string]any{"share": 50, "decimals": 2, "min_payout": "1", "types": []any{"sale"}, "statuses": []any{"approved", "declined"}, "resend": true}
	for name, bad := range map[string]map[string]any{"share": {"share": 2000}, "status": {"statuses": []any{"maybe"}}, "type": {"types": []any{"Big Sale"}}} {
		if st, _ := owner.do("POST", base+"/traffic-sources", map[string]any{"name": "x-" + name, "postback_rules": bad}); st != 400 {
			t.Errorf("%s: %d, want 400", name, st)
		}
	}
	ts := owner.must(201, "POST", base+"/traffic-sources", map[string]any{"name": "Acme Push", "click_id": map[string]any{"param": "clickid", "token": "{clickid}"},
		"postback":       map[string]any{"url": network.URL + "/pb?cid={source_click_id}&payout={payout}&tx={transaction_id}&st={status}&r={random}"},
		"postback_rules": rules})
	if pr := ts["postback_rules"].(map[string]any); pr["share"] != float64(50) || pr["min_payout"] != "1" || pr["resend"] != true ||
		len(pr["statuses"].([]any)) != 2 {
		t.Fatalf("rules: %v", pr)
	}
	// A change to one rule keeps the others.
	if pr := owner.must(200, "PATCH", base+"/traffic-sources/"+ts["id"].(string), map[string]any{"postback_rules": map[string]any{"decimals": 1}})["postback_rules"].(map[string]any); pr["decimals"] != float64(1) || pr["share"] != float64(50) {
		t.Fatalf("rules after a change: %v", pr)
	}
	owner.must(200, "PATCH", base+"/traffic-sources/"+ts["id"].(string), map[string]any{"postback_rules": map[string]any{"decimals": 2}})

	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Push", "channel": "paid_ads", "source": "acme", "redirect_links": true,
		"traffic_source": ts["id"]})
	l := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://offers.northpeak.example/o1"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(l["redirect_url"].(string))[1]
	src := owner.must(201, "POST", base+"/postback-sources", map[string]any{"name": "Acme Network", "duplicate_policy": "update"})
	token := strings.TrimPrefix(strings.SplitN(src["postback_url"].(string), "?", 2)[0], "https://"+host)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Host = host
		req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15")
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		return rec
	}
	click := func(netID string) string {
		t.Helper()
		loc, _ := url.Parse(get("/l/" + code + "?clickid=" + netID).Header().Get("Location"))
		return loc.Query().Get("ofclk")
	}
	convert := func(query string) {
		t.Helper()
		if rec := get(token + "?" + query); rec.Code != 200 {
			t.Fatalf("postback %s: %d %s", query, rec.Code, rec.Body)
		}
	}

	// A sale is sent with half its payout; a lead (not a type it sends) and
	// a sale under the minimum aren't.
	k1 := click("net-1")
	convert("cid=" + k1 + "&payout=10&type=sale&txid=S-1")
	convert("cid=" + click("net-2") + "&payout=10&type=lead&txid=L-1")
	convert("cid=" + click("net-3") + "&payout=1&type=sale&txid=S-2")
	e.runCRMImports(t)
	sendsPath := base + "/traffic-sources/" + ts["id"].(string) + "/postbacks"
	sends := func(want int) []any {
		t.Helper()
		for range 250 {
			list := owner.must(200, "GET", sendsPath, nil)["data"].([]any)
			done := 0
			for _, s := range list {
				if s.(map[string]any)["status"] != "queued" {
					done++
				}
			}
			if len(list) == want && done == want {
				return list
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("postbacks didn't finish: %v", owner.must(200, "GET", sendsPath, nil))
		return nil
	}
	first := sends(1)[0].(map[string]any)
	if first["status"] != "sent" || first["http_status"] != 200.0 || first["payout"] != "5" || !strings.Contains(first["url"].(string), "cid=net-1") {
		t.Fatalf("send: %v", first)
	}
	mu.Lock()
	q := got[0]
	mu.Unlock()
	if q.Get("cid") != "net-1" || q.Get("payout") != "5.00" || q.Get("tx") != "S-1" || q.Get("st") != "approved" || q.Get("r") == "" {
		t.Fatalf("postback query: %v", q)
	}

	// The network declines it later: the change is sent again (and the
	// network refuses it, so it fails once and isn't retried); the same
	// state again sends nothing.
	convert("cid=" + k1 + "&payout=10&type=sale&txid=S-1&status=declined")
	list := sends(2)
	last := list[0].(map[string]any)
	if last["status"] != "failed" || last["http_status"] != 404.0 || last["attempts"] != 1.0 {
		t.Fatalf("declined send: %v", last)
	}
	convert("cid=" + k1 + "&payout=10&type=sale&txid=S-1&status=declined")
	time.Sleep(200 * time.Millisecond)
	if n := len(owner.must(200, "GET", sendsPath, nil)["data"].([]any)); n != 2 {
		t.Fatalf("sends after a repeated state: %d, want 2", n)
	}
	if n := len(owner.must(200, "GET", sendsPath+"?status=sent", nil)["data"].([]any)); n != 1 {
		t.Fatalf("sent: %d", n)
	}
	owner.must(400, "GET", sendsPath+"?status=sometimes", nil)
	owner.must(404, "GET", base+"/traffic-sources/00000000-0000-0000-0000-000000000000/postbacks", nil)
	other := owner.must(201, "POST", "/v1/workspaces/"+ws+"/brands", map[string]any{"name": "Repocket"})
	owner.must(404, "GET", "/v1/brands/"+other["id"].(string)+"/traffic-sources/"+ts["id"].(string)+"/postbacks", nil)

	failed := false
	for _, ev := range events() {
		failed = failed || ev.Name == "traffic_source.postback_failed"
	}
	if !failed {
		t.Error("no traffic_source.postback_failed event")
	}
}
