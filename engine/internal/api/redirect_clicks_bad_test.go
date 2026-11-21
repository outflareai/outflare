package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A click whose values the database can't take must never stop the others
// being written. Anyone can send one: a NUL byte or broken UTF-8 in a
// parameter or the user agent (an ad network's Latin-1 keyword does it
// with no bad intent). Such values are made storable, and a click the
// database still refuses is dropped alone.
func TestRedirectClickBadValues(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	c := owner.must(201, "POST", base+"/tracking-campaigns", map[string]any{
		"name": "Winter push", "channel": "paid_ads", "source": "google", "landing_url": "https://northpeak.example/winter", "redirect_links": true,
	})
	link := owner.must(201, "POST", base+"/tracking-campaigns/"+c["code"].(string)+"/links", map[string]any{"content": "search-a"})
	code := regexp.MustCompile(`/l/([a-z0-9]{8})$`).FindStringSubmatch(link["redirect_url"].(string))[1]
	follow := func(query, agent, ip string) {
		t.Helper()
		req := httptest.NewRequest("GET", "/l/"+code+query, nil)
		req.Header.Set("User-Agent", agent)
		req.RemoteAddr = ip + ":40000"
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, req)
		if rec.Code != http.StatusFound {
			t.Fatalf("redirect %q = %d", query, rec.Code)
		}
	}
	const person = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15"
	const android = "Mozilla/5.0 (Linux; Android 14; SM-\xff\xfe9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Mobile Safari/537.36"
	count := func() (n int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM tracking_link_clicks WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	follow("?sub1=a%00b&sub2=caf%E9", person, "192.0.2.10") // a NUL byte, and Latin-1 where UTF-8 is expected
	follow("", android, "192.0.2.11")                       // a device model that isn't UTF-8
	follow("?sub1=plain", person, "192.0.2.12")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if n := count(); n != 3 {
		t.Fatalf("clicks written = %d, want all 3", n)
	}
	var one, two string
	if err := e.pool.QueryRow(ctx, `SELECT fields->>'1', fields->>'2' FROM tracking_link_clicks WHERE workspace_id = $1 AND fields->>'2' IS NOT NULL`, ws).Scan(&one, &two); err != nil {
		t.Fatal(err)
	}
	if one != "ab" || two != "caf�" {
		t.Fatalf("fields = %q, %q", one, two)
	}

	// A click the database refuses whatever is done to its text (here a
	// destination that isn't an id) is dropped alone and counted; the
	// clicks before and after it in the batch are written.
	events := track.Capture(t)
	follow("?sub1=before", person, "192.0.2.13")
	e.srv.BufferBadClick(ws, link["id"].(string), "cccccccccccccccc")
	follow("?sub1=after", person, "192.0.2.14")
	follow("?sub1=last", person, "192.0.2.15")
	if err := e.srv.FlushClicks(ctx); err != nil {
		t.Fatalf("flush with a click the database refuses: %v", err)
	}
	if n := count(); n != 6 {
		t.Fatalf("clicks written = %d, want 6 (the refused one left out)", n)
	}
	dropped := 0
	for _, ev := range events() {
		if ev.Name == "tracking_link.click_dropped" {
			dropped++
			if ev.WorkspaceID != ws || ev.ErrorCode != "invalid_request" {
				t.Fatalf("dropped event: %+v", ev)
			}
		}
	}
	if dropped != 1 {
		t.Fatalf("tracking_link.click_dropped events = %d", dropped)
	}
	// Nothing is left waiting to fail again.
	follow("?sub1=next", person, "192.0.2.16")
	if err := e.srv.FlushClicks(ctx); err != nil || count() != 7 {
		t.Fatalf("the flush after: %v, %d clicks", err, count())
	}
}
