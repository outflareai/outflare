package api_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/rdap"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A verified tracking domain's registration is read from its public
// record, shown on the domain, and its brand is told 30 days before it
// runs out, 7 days before, and when it has (plans/ad-tracker.md, B8).
func TestTrackingDomainExpiry(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	base := "/v1/brands/" + brand

	// A registry that says when northpeak.example runs out, has no date
	// for nodate.example, and can be down.
	var mu sync.Mutex
	left := 200 * 24 * time.Hour
	down := false
	hits := map[string]int{}
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		hits[r.URL.Path]++
		switch {
		case down:
			w.WriteHeader(http.StatusBadGateway)
		case r.URL.Path == "/domain/northpeak.example":
			fmt.Fprintf(w, `{"events":[{"eventAction":"registration","eventDate":"2019-02-01T00:00:00Z"},{"eventAction":"expiration","eventDate":%q}]}`,
				time.Now().Add(left).UTC().Format(time.RFC3339))
		case r.URL.Path == "/domain/nodate.example":
			w.Write([]byte(`{"events":[{"eventAction":"registration","eventDate":"2019-02-01T00:00:00Z"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(registry.Close)
	set := func(d time.Duration, isDown bool) {
		mu.Lock()
		left, down = d, isDown
		mu.Unlock()
	}
	check := func() int {
		t.Helper()
		n, err := e.srv.CheckTrackingDomainExpiry(ctx, 0)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	// due makes a domain's last read d ago.
	due := func(host string, d time.Duration) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE tracking_domains SET registration_checked_at = $2 WHERE domain = $1`, host, time.Now().Add(-d)); err != nil {
			t.Fatal(err)
		}
	}
	domain := func(host string) map[string]any {
		t.Helper()
		for _, d := range owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any) {
			if d.(map[string]any)["domain"] == host {
				return d.(map[string]any)
			}
		}
		t.Fatalf("no tracking domain %s", host)
		return nil
	}
	heard := func() []map[string]any {
		t.Helper()
		var out []map[string]any
		for _, a := range owner.must(200, "GET", base+"/alerts?kind=tracking_domain.expiring", nil)["data"].([]any) {
			out = append(out, a.(map[string]any))
		}
		return out
	}

	ownTrackingDomain(t, e, ws, brand, "go.northpeak.example")
	if _, err := e.pool.Exec(ctx, `INSERT INTO tracking_domains (workspace_id, brand_id, domain, status) VALUES ($1, $2, 'pending.northpeak.example', 'pending')`, ws, brand); err != nil {
		t.Fatal(err)
	}
	// Without a registration reader nothing is read.
	if n := check(); n != 0 || domain("go.northpeak.example")["registration_expires_at"] != nil {
		t.Fatalf("read %d with no reader", n)
	}
	e.crawl.RDAP = &rdap.Client{BaseURL: registry.URL}

	// Far from running out: the date shows, nobody is told, and it isn't
	// read again for a week. A domain that isn't verified is never read.
	if n := check(); n != 1 {
		t.Fatalf("first pass read %d", n)
	}
	at, _ := time.Parse(time.RFC3339, domain("go.northpeak.example")["registration_expires_at"].(string))
	if d := time.Until(at); d < 199*24*time.Hour || d > 201*24*time.Hour {
		t.Fatalf("expires %v", at)
	}
	if n := len(heard()); n != 0 {
		t.Fatalf("%d alerts for a domain 200 days out", n)
	}
	due("go.northpeak.example", 6*24*time.Hour)
	if n := check(); n != 0 {
		t.Fatalf("read again after 6 days: %d", n)
	}
	if domain("pending.northpeak.example")["registration_expires_at"] != nil || hits["/domain/northpeak.example"] != 1 {
		t.Fatalf("hits: %v", hits)
	}

	// 20 days out: one alert, however often it is read; and now it is
	// read daily.
	set(20*24*time.Hour, false)
	due("go.northpeak.example", 8*24*time.Hour)
	check()
	due("go.northpeak.example", 25*time.Hour)
	if n := check(); n != 1 {
		t.Fatalf("a domain near its expiry wasn't read after a day: %d", n)
	}
	list := heard()
	if len(list) != 1 || list[0]["title"] != "go.northpeak.example expires in 20 days" || list[0]["path"] != "domains" ||
		!strings.Contains(list[0]["text"].(string), "The registration of northpeak.example runs out on ") {
		t.Fatalf("at 20 days: %v", list)
	}
	// 5 days out, then run out: one more each. The older alerts read the
	// domain as it is now.
	set(5*24*time.Hour, false)
	due("go.northpeak.example", 25*time.Hour)
	check()
	set(-3*time.Hour, false)
	due("go.northpeak.example", 25*time.Hour)
	check()
	list = heard()
	if len(list) != 3 || list[0]["title"] != "go.northpeak.example has expired" || list[2]["title"] != "go.northpeak.example has expired" {
		t.Fatalf("after it ran out: %v", list)
	}
	var keys []string
	if err := e.pool.QueryRow(ctx, `SELECT array_agg(data->>'threshold' ORDER BY created_at) FROM alerts WHERE workspace_id = $1 AND kind = 'tracking_domain.expiring'`, ws).Scan(&keys); err != nil ||
		strings.Join(keys, ",") != "30,7,0" {
		t.Fatalf("warnings: %v %v", keys, err)
	}
	// Renewed: the alerts say so, and a later expiry warns afresh.
	set(400*24*time.Hour, false)
	due("go.northpeak.example", 25*time.Hour)
	check()
	if list = heard(); len(list) != 3 || list[0]["title"] != "go.northpeak.example was renewed" {
		t.Fatalf("after the renewal: %v", list)
	}

	// The registry down: the date stays, an event says so, and it is tried
	// again a day later, not at once.
	set(0, true)
	due("go.northpeak.example", 8*24*time.Hour)
	if n := check(); n != 1 || domain("go.northpeak.example")["registration_expires_at"] == nil {
		t.Fatalf("with the registry down: %d", n)
	}
	if n := check(); n != 0 {
		t.Fatalf("a failed read was tried again at once: %d", n)
	}
	due("go.northpeak.example", 8*24*time.Hour)
	set(400*24*time.Hour, false)

	// Two tracking domains under one registrable domain share one read; a
	// registry that gives no date leaves it empty.
	ownTrackingDomain(t, e, ws, brand, "links.northpeak.example")
	ownTrackingDomain(t, e, ws, brand, "go.nodate.example")
	before := hits["/domain/northpeak.example"]
	if n := check(); n != 3 {
		t.Fatalf("read %d of three due", n)
	}
	if hits["/domain/northpeak.example"] != before+1 || domain("links.northpeak.example")["registration_expires_at"] == nil ||
		domain("go.nodate.example")["registration_expires_at"] != nil {
		t.Fatalf("hits %v", hits)
	}

	ev := got()
	failed := named(ev, "tracking_domain.expiry_check_failed")
	if len(failed) != 1 || failed[0].ErrorCode != "unavailable" || failed[0].Props["tracking_domain_id"] == nil {
		t.Fatalf("tracking_domain.expiry_check_failed: %+v", failed)
	}
	for _, x := range failed {
		if strings.Contains(fmt.Sprint(x.Props), "northpeak") {
			t.Fatalf("the event names the domain: %v", x.Props)
		}
	}
	if n := len(named(ev, "alert.raised")); n != 3 {
		t.Fatalf("alert.raised: %d", n)
	}
}
