package api_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/quota"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeQuota refuses the resources in refuse, or fails every check.
type fakeQuota struct {
	mu     sync.Mutex
	refuse map[string]bool
	fail   bool
	asked  []string
}

func (f *fakeQuota) Allow(_ context.Context, _, resource string, _ int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, resource)
	if f.fail {
		return errors.New("the checker's database is down")
	}
	if f.refuse[resource] {
		return &quota.Error{Resource: resource, Message: "The Free plan allows no more " + resource + ". Add a card to keep going."}
	}
	return nil
}

func (f *fakeQuota) set(fail bool, refuse ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.fail, f.refuse = fail, map[string]bool{}
	for _, r := range refuse {
		f.refuse[r] = true
	}
}

func TestQuotaRefusesNewThings(t *testing.T) {
	q := &fakeQuota{refuse: map[string]bool{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Quota = q })
	events := track.Capture(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	// Allowed: the checker is asked, and the work goes ahead.
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})
	owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Ana"})
	if !slices.Contains(q.asked, quota.Brands) || !slices.Contains(q.asked, quota.Records) {
		t.Fatalf("asked: %v", q.asked)
	}

	// Refused: 402 quota_exceeded with the checker's message, and nothing
	// is added.
	q.set(false, quota.Brands, quota.Records, quota.TrackingDomains, quota.Mailboxes)
	for _, c := range []struct {
		path string
		body map[string]any
	}{
		{"/brands", map[string]any{"name": "Acme"}},
		{"/records", map[string]any{"type": "person", "name": "Ben"}},
		{"/tracking-domains", map[string]any{"domain": "links.acme.example"}},
		{"/mailboxes", map[string]any{"email": "eve@gw.test", "password": "pw"}},
	} {
		out := owner.must(402, "POST", base+c.path, c.body)
		er := out["error"].(map[string]any)
		if er["code"] != "quota_exceeded" || !strings.Contains(er["message"].(string), "Add a card") {
			t.Fatalf("%s: %v", c.path, out)
		}
	}
	if n := len(owner.must(200, "GET", base+"/tracking-domains", nil)["data"].([]any)); n != 0 {
		t.Fatalf("a refused tracking domain was added: %d", n)
	}

	// A checker that fails allows the work, and says so.
	q.set(true)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme"})
	if !slices.ContainsFunc(events(), func(ev track.Event) bool {
		return ev.Name == "quota.check_failed" && ev.WorkspaceID == ws && ev.Props["resource"] == quota.Brands
	}) {
		t.Fatal("no quota.check_failed event")
	}
}

// A refused send waits and goes once the workspace may send again; it is
// never failed or blocked.
func TestQuotaHoldsSends(t *testing.T) {
	e := freshEnv(t)
	q := &fakeQuota{refuse: map[string]bool{quota.EmailsSent: true}}
	e.send.Quota = q
	clock := &fakeClock{t: time.Now()}
	if u := clock.Now().UTC(); u.Hour() == 23 {
		clock.Add(2 * time.Hour)
	}
	owner, base, _ := sendingSetup(t, e, clock)
	ctx := context.Background()
	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\np1@one-io.example\np2@one-io.example\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Quota", "daily_new_limit": 100, "send_days": []int{1, 2, 3, 4, 5, 6, 7}, "send_start": "00:00", "send_end": "23:59",
		"steps": []map[string]any{{"subject": "Hi", "body": "Hello {{unsubscribe_url}}"}},
	})
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	count := func(q string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, q, seqID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM messages WHERE sequence_id = $1`); n != 0 {
		t.Fatalf("%d messages went past the quota", n)
	}
	if n := count(`SELECT count(*) FROM enrollments WHERE sequence_id = $1 AND status = 'active' AND status_reason = 'quota_exceeded'`); n != 2 {
		t.Fatalf("%d enrollments wait for the quota, want 2", n)
	}

	// Allowed again: after the wait, both go.
	q.set(false)
	clock.Add(time.Hour)
	for range 4 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := count(`SELECT count(*) FROM messages WHERE sequence_id = $1 AND status = 'sent'`); n != 2 {
		t.Fatalf("%d sent after the quota allowed them, want 2", n)
	}
}

// Events past the quota are refused before anything is stored: on the
// workspace and brand APIs and from the website snippet.
func TestQuotaRefusesEvents(t *testing.T) {
	q := &fakeQuota{refuse: map[string]bool{}}
	e := freshEnvWith(t, func(o *api.Options) { o.Quota = q })
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak"})["id"].(string)
	key := owner.must(201, "POST", base+"/site-keys", map[string]any{"name": "northpeak.example", "origins": []string{"https://northpeak.example"}})["key"].(string)
	ev := map[string]any{"type": "track", "userId": "u_1", "event": "Signed Up"}
	page := map[string]any{"type": "page", "anonymousId": "a-1", "context": map[string]any{"page": map[string]any{"url": "https://northpeak.example/", "path": "/"}}}

	// Allowed: each route asks, and stores.
	owner.must(200, "POST", base+"/customer-events", ev)
	owner.must(202, "POST", "/v1/brands/"+brand+"/events", ev)
	if code, out := sendSite(t, e.h, "https://northpeak.example", key, page); code != 200 {
		t.Fatalf("site: %d %v", code, out)
	}
	if n := len(slices.DeleteFunc(slices.Clone(q.asked), func(r string) bool { return r != quota.TrackedEvents })); n != 3 {
		t.Fatalf("asked about events %d times, want 3: %v", n, q.asked)
	}

	// Refused: 402 quota_exceeded, and nothing is stored.
	q.set(false, quota.TrackedEvents)
	var before int
	if err := e.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM customer_events WHERE workspace_id = $1) + (SELECT count(*) FROM event_inbox WHERE workspace_id = $1)`, ws).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{base + "/customer-events", "/v1/brands/" + brand + "/events"} {
		out := owner.must(402, "POST", path, map[string]any{"batch": []any{ev, ev}})
		if er := out["error"].(map[string]any); er["code"] != "quota_exceeded" || !strings.Contains(er["message"].(string), "Add a card") {
			t.Fatalf("%s: %v", path, out)
		}
	}
	if code, out := sendSite(t, e.h, "https://northpeak.example", key, page); code != 402 || out["error"].(map[string]any)["code"] != "quota_exceeded" {
		t.Fatalf("site: %d %v", code, out)
	}
	var after int
	if err := e.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM customer_events WHERE workspace_id = $1) + (SELECT count(*) FROM event_inbox WHERE workspace_id = $1)`, ws).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("refused events were stored: %d then %d", before, after)
	}
}
