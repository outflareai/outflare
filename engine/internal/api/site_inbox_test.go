package api_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSiteEventsWaitInTheInbox(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	k := owner.must(201, "POST", "/v1/workspaces/"+ws+"/site-keys", map[string]any{"name": "acme.example", "origins": []string{"https://acme.example"}})
	key := k["key"].(string)
	if _, err := e.pool.Exec(ctx, `DELETE FROM site_event_inbox`); err != nil {
		t.Fatal(err)
	}

	// A collector only checks the key and the events' shape, then answers.
	srv := api.NewServer(e.pool, slog.New(slog.NewTextHandler(io.Discard, nil)), api.Options{SiteInbox: true})
	h := srv.Handler()
	rec := sendSiteAs(t, h, "https://acme.example", key, desktopUA, true,
		map[string]any{"type": "page", "anonymousId": "a-inbox-1", "context": map[string]any{"page": map[string]any{"url": "https://acme.example/", "path": "/"}}},
		map[string]any{"type": "track", "event": "Signup Started", "anonymousId": "a-inbox-1"},
		map[string]any{"type": "identify", "anonymousId": "a-inbox-1"})
	if rec.Code != 200 || rec.Body.String() != "{\"accepted\":2,\"rejected\":1}\n" {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	if c := rec.Result().Cookies(); len(c) != 1 || c[0].Value != "a-inbox-1" {
		t.Fatalf("consent cookie: %v", c)
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, sql, ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	events := `SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND anonymous_id = 'a-inbox-1'`
	waiting := `SELECT count(*) FROM site_event_inbox WHERE workspace_id = $1 AND failed_at IS NULL`
	if count(events) != 0 || count(waiting) != 1 {
		t.Fatalf("stored before a worker ran: %d events, %d waiting", count(events), count(waiting))
	}

	// A worker stores them as of when they arrived, in their session.
	if n, err := srv.DrainSiteInbox(ctx); err != nil || n != 1 {
		t.Fatalf("drain: %d %v", n, err)
	}
	if count(events) != 2 || count(waiting) != 0 {
		t.Fatalf("after a worker: %d events, %d waiting", count(events), count(waiting))
	}
	if count(`SELECT count(*) FROM site_sessions WHERE workspace_id = $1 AND anonymous_id = 'a-inbox-1' AND pageviews = 1 AND events = 2`) != 1 {
		t.Fatal("the visit has no session")
	}
	if count(`SELECT count(*) FROM site_keys WHERE workspace_id = $1 AND last_used_at IS NOT NULL`) != 1 {
		t.Fatal("the key was not marked used")
	}
	if n, err := srv.DrainSiteInbox(ctx); err != nil || n != 0 {
		t.Fatalf("second drain: %d %v", n, err)
	}

	// A row that can't be stored is tried again, then set aside with its
	// error, and never holds up the rows after it.
	got := track.Capture(t)
	if _, err := e.pool.Exec(ctx, `INSERT INTO site_event_inbox (workspace_id, key_id, batch) SELECT workspace_id, id, '{"not": "a list"}' FROM site_keys WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	sendSiteAs(t, h, "https://acme.example", key, desktopUA, false, map[string]any{"type": "page", "anonymousId": "a-inbox-2"})
	for i := 0; i < 5; i++ {
		if _, err := srv.DrainSiteInbox(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND anonymous_id = 'a-inbox-2'`) != 1 {
		t.Fatal("a bad row held up the next one")
	}
	if count(waiting) != 0 || count(`SELECT count(*) FROM site_event_inbox WHERE workspace_id = $1 AND attempts = 5 AND failed_at IS NOT NULL AND last_error <> ''`) != 1 {
		t.Fatal("the bad row was not set aside")
	}
	failed := named(got(), "site_events.store_failed")
	if len(failed) != 5 || failed[0].WorkspaceID != ws || failed[0].ErrorCode != "internal" || failed[4].Props["set_aside"] != true {
		t.Fatalf("failure events: %+v", failed)
	}

	// Without the inbox (the default), events are stored during the request.
	if code, out := sendSite(t, e.h, "https://acme.example", key, map[string]any{"type": "page", "anonymousId": "a-inbox-3"}); code != 200 || out["accepted"] != 1.0 {
		t.Fatalf("inline: %d %v", code, out)
	}
	if count(`SELECT count(*) FROM customer_events WHERE workspace_id = $1 AND anonymous_id = 'a-inbox-3'`) != 1 {
		t.Fatal("inline send not stored")
	}
}
