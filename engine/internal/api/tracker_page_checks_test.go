package api_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakePages answers for landers' pages by host and path, without a
// network: a status for each, or a failure before any page answers.
type fakePages struct {
	mu     sync.Mutex
	status map[string]int   // host + path to the status it answers
	fail   map[string]error // host to the error reaching it gives
	asked  []*http.Request
}

func (f *fakePages) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, r)
	if err := f.fail[r.URL.Host]; err != nil {
		return nil, err
	}
	if err := f.fail["*"]; err != nil {
		return nil, err
	}
	code := f.status[r.URL.Host+r.URL.Path]
	if code == 0 {
		code = 200
	}
	return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader("<html></html>")), Header: http.Header{}, Request: r}, nil
}

func (f *fakePages) set(key string, code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status[key] = code
}

func (f *fakePages) down(host string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, host)
		return
	}
	f.fail[host] = err
}

// A lander in use is asked for its page about hourly, an offer every six
// hours unless its check is switched off; two failed checks in a row make
// it broken and tell its brand once; a page that turns automated requests
// away isn't broken; and a pass where nothing could be reached is thrown
// away (plans/ad-tracker.md, B8).
func TestTrackerPageChecks(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand
	ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	pages := &fakePages{status: map[string]int{"pages.northpeak.example/gone": 404, "guarded.northpeak.example/": 403}, fail: map[string]error{}}
	t.Cleanup(api.SetLanderCheckClient(&http.Client{Transport: pages}))

	lander := func(name, url string) string {
		t.Helper()
		return owner.must(201, "POST", base+"/landers", map[string]any{"name": name, "url": url})["id"].(string)
	}
	best := lander("Best of 2026", "https://pages.northpeak.example/best?c={click_id}&src=review")
	old := lander("Old review", "https://pages.northpeak.example/gone")
	guarded := lander("Guarded", "https://guarded.northpeak.example/")
	unused := lander("Unused", "https://pages.northpeak.example/unused")
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{"name": "Review push", "channel": "paid_ads", "source": "acme", "redirect_links": true})
	link := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"url": "https://northpeak.example/own"})
	offer := func(name, url string, extra map[string]any) string {
		t.Helper()
		body := map[string]any{"name": name, "url": url}
		for k, v := range extra {
			body[k] = v
		}
		return owner.must(201, "POST", base+"/offers", body)["id"].(string)
	}
	offerA := offer("Offer A", "https://offers.northpeak.example/a?sub={click_id}", nil)
	offerOff := offer("Offer off", "https://offers.northpeak.example/off", map[string]any{"check_enabled": false})
	offer("Offer unused", "https://offers.northpeak.example/unused", nil)
	owner.must(200, "PUT", base+"/tracking-campaigns/"+c["code"].(string)+"/links/"+link["id"].(string)+"/destinations", map[string]any{"destinations": []any{
		map[string]any{"lander_id": best}, map[string]any{"lander_id": old}, map[string]any{"lander_id": guarded},
		map[string]any{"offer_id": offerA}, map[string]any{"offer_id": offerOff},
	}})

	check := func() int {
		t.Helper()
		n, err := e.srv.CheckTrackerPages(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// due makes a lander's or an offer's last check d ago.
	due := func(d time.Duration, ids ...string) {
		t.Helper()
		for _, table := range []string{"tracker_landers", "tracker_offers"} {
			if _, err := e.pool.Exec(ctx, `UPDATE `+table+` SET checked_at = $2 WHERE id = ANY($1::uuid[])`, ids, time.Now().Add(-d)); err != nil {
				t.Fatal(err)
			}
		}
	}
	offerView := func(id string) map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/offers/"+id, nil)
	}
	view := func(id string) map[string]any {
		t.Helper()
		return owner.must(200, "GET", base+"/landers/"+id, nil)
	}
	heard := func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, a := range owner.must(200, "GET", base+"/alerts?kind=tracker.link_broken", nil)["data"].([]any) {
			out = append(out, a.(map[string]any))
		}
		return out
	}

	// The first pass asks the three landers and the one offer a link
	// sends to and may check, once each, with their tokens left empty, as
	// OutflareBot.
	if n := check(); n != 4 {
		t.Fatalf("first pass asked %d", n)
	}
	for _, r := range pages.asked {
		if strings.ContainsAny(r.URL.String(), "{}") || !strings.Contains(r.Header.Get("User-Agent"), "OutflareBot") || r.Method != "GET" {
			t.Fatalf("asked %s %s as %q", r.Method, r.URL, r.Header.Get("User-Agent"))
		}
		if r.URL.Path == "/unused" || r.URL.Path == "/off" {
			t.Fatalf("%s was asked: no link sends to it, or its check is off", r.URL.Path)
		}
		if r.URL.Path == "/a" && r.URL.RawQuery != "sub=" {
			t.Fatalf("the offer's own parameters: %s", r.URL.RawQuery)
		}
		if r.URL.Path == "/best" && r.URL.RawQuery != "c=&src=review" {
			t.Fatalf("the lander's own parameters: %s", r.URL.RawQuery)
		}
	}
	if v := view(best); v["check_status"] != "ok" || v["check_code"] != "" || v["checked_at"] == nil || v["broken_since"] != nil {
		t.Fatalf("a page that answers: %v", v)
	}
	if v := view(old); v["check_status"] != "failing" || v["check_code"] != "http_404" || v["broken_since"] == nil {
		t.Fatalf("one failed check: %v", v)
	}
	// A page that turns bots away can't be checked; it isn't broken.
	if v := view(guarded); v["check_status"] != "refused" || v["check_code"] != "http_403" || v["broken_since"] != nil {
		t.Fatalf("a page that refuses: %v", v)
	}
	if v := view(unused); v["check_status"] != "" || v["checked_at"] != nil {
		t.Fatalf("an unused lander: %v", v)
	}
	if v := offerView(offerA); v["check_status"] != "ok" || v["check_enabled"] != true || v["checked_at"] == nil {
		t.Fatalf("an offer that answers: %v", v)
	}
	if v := offerView(offerOff); v["check_status"] != "" || v["check_enabled"] != false || v["checked_at"] != nil {
		t.Fatalf("an offer whose check is off: %v", v)
	}
	if n := len(heard()); n != 0 {
		t.Fatalf("%d alerts after one failed check", n)
	}
	// Nothing is due at once; the failing one is ten minutes later, and
	// its second failure makes it broken and tells the brand, once.
	if n := check(); n != 0 {
		t.Fatalf("asked again at once: %d", n)
	}
	due(11*time.Minute, old, best)
	if n := check(); n != 1 {
		t.Fatalf("after ten minutes asked %d, want only the failing one", n)
	}
	// Once broken it is asked at its usual pace, not every ten minutes.
	due(11*time.Minute, old)
	if n := check(); n != 0 {
		t.Fatalf("a broken lander was asked again after ten minutes: %d", n)
	}
	list := heard()
	if len(list) != 1 || list[0]["title"] != "Old review isn't answering" || list[0]["path"] != "tracker-landers" ||
		!strings.Contains(list[0]["text"].(string), "the page isn't there any more (404)") {
		t.Fatalf("after two failed checks: %v", list)
	}
	// It is for the person who added the lander, so only they are emailed.
	var me string
	if err := e.pool.QueryRow(ctx, `SELECT u.id::text FROM users u JOIN memberships m ON m.user_id = u.id WHERE m.workspace_id = $1 AND m.role = 'owner'`, ws).Scan(&me); err != nil {
		t.Fatal(err)
	}
	if list[0]["owner_id"] != me {
		t.Fatalf("the alert isn't for the person who added the lander: %v (want %s)", list[0]["owner_id"], me)
	}
	if v := view(old); v["check_status"] != "broken" || v["broken_since"] == nil {
		t.Fatalf("a broken lander: %v", v)
	}
	// It answers again: the alert says so. It fails again later, with a
	// server error: a new alert.
	pages.set("pages.northpeak.example/gone", 200)
	due(61*time.Minute, old)
	check()
	if v := view(old); v["check_status"] != "ok" || v["broken_since"] != nil {
		t.Fatalf("after it came back: %v", v)
	}
	if list = heard(); len(list) != 1 || list[0]["title"] != "Old review is answering again" {
		t.Fatalf("the alert after it came back: %v", list)
	}
	pages.set("pages.northpeak.example/gone", 503)
	due(61*time.Minute, old)
	check()
	due(11*time.Minute, old)
	check()
	if list = heard(); len(list) != 2 || !strings.Contains(list[0]["text"].(string), "its server answers with an error (503)") {
		t.Fatalf("after it broke again: %v", list)
	}

	// Nothing can be reached at all: that is this server's network, so
	// the pass is thrown away, nothing changes and nobody is told.
	pages.down("*", &net.DNSError{Err: "no such host", Name: "pages.northpeak.example", IsNotFound: true})
	due(61*time.Minute, best, old, guarded)
	if n := check(); n != 3 {
		t.Fatalf("the pass with no network asked %d", n)
	}
	if v := view(best); v["check_status"] != "ok" {
		t.Fatalf("a thrown-away pass changed a lander: %v", v)
	}
	pages.down("*", nil)
	// One domain that can't be found, while the others answer, is the
	// lander's own.
	pages.set("pages.northpeak.example/gone", 200)
	pages.down("guarded.northpeak.example", &net.DNSError{Err: "no such host", Name: "guarded.northpeak.example", IsNotFound: true})
	due(61*time.Minute, best, old, guarded)
	check()
	due(11*time.Minute, guarded)
	check()
	if list = heard(); len(list) != 3 || list[0]["title"] != "Guarded isn't answering" || !strings.Contains(list[0]["text"].(string), "its domain can't be found") {
		t.Fatalf("a lander whose domain is gone: %v", list)
	}

	// A new URL is a page not asked yet.
	owner.must(200, "PATCH", base+"/landers/"+guarded, map[string]any{"url": "https://pages.northpeak.example/new"})
	if v := view(guarded); v["check_status"] != "" || v["checked_at"] != nil || v["broken_since"] != nil {
		t.Fatalf("after its URL changed: %v", v)
	}
	if n := check(); n != 1 || view(guarded)["check_status"] != "ok" {
		t.Fatalf("the new URL wasn't asked: %d", n)
	}

	// Offers are asked every six hours, not hourly; one that is gone is
	// broken after two checks and tells the brand, as an offer.
	pages.set("offers.northpeak.example/a", 410)
	due(5*time.Hour, offerA)
	if n := check(); n != 0 {
		t.Fatalf("an offer was asked again after five hours: %d", n)
	}
	due(7*time.Hour, offerA)
	check()
	if v := offerView(offerA); v["check_status"] != "failing" || v["check_code"] != "http_410" {
		t.Fatalf("an offer after one failed check: %v", v)
	}
	due(11*time.Minute, offerA)
	check()
	if list = heard(); len(list) != 4 || list[0]["title"] != "Offer A isn't answering" || list[0]["path"] != "tracker-offers" ||
		!strings.Contains(list[0]["text"].(string), "Clicks sent to this offer are wasted: the page isn't there any more (410). Check the offer with its network") {
		t.Fatalf("a broken offer: %v", list)
	}
	// Switching its check off forgets what was found and stops the asking.
	owner.must(200, "PATCH", base+"/offers/"+offerA, map[string]any{"check_enabled": false})
	if v := offerView(offerA); v["check_status"] != "" || v["check_enabled"] != false || v["broken_since"] != nil {
		t.Fatalf("after its check was switched off: %v", v)
	}
	due(7*time.Hour, offerA, offerOff)
	if n := check(); n != 0 {
		t.Fatalf("an offer whose check is off was asked: %d", n)
	}
	// On again: asked on the next pass.
	owner.must(200, "PATCH", base+"/offers/"+offerOff, map[string]any{"check_enabled": true})
	if n := check(); n != 1 || offerView(offerOff)["check_status"] != "ok" {
		t.Fatalf("an offer whose check was switched on wasn't asked: %d", n)
	}

	ev := got()
	failed := named(ev, "tracker.page_checks_failed")
	if len(failed) != 1 || failed[0].ErrorCode != "unavailable" || failed[0].Props["checks"] != 3 {
		t.Fatalf("tracker.page_checks_failed: %+v", failed)
	}
	raised := 0
	for _, x := range named(ev, "alert.raised") {
		if x.Props["kind"] == "tracker.link_broken" {
			raised++
		}
		if strings.Contains(fmt.Sprint(x.Props), "northpeak") {
			t.Fatalf("the event names the page: %v", x.Props)
		}
	}
	if raised != 4 {
		t.Fatalf("alert.raised for tracker.link_broken: %d", raised)
	}
}
