package api_test

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A mailbox whose OAuth grant was revoked asks to be connected again and
// stops syncing, instead of failing every few minutes unseen. A provider
// outage is only noted, and the mailbox keeps its turn.
func TestInboxSyncRevokedSignIn(t *testing.T) {
	e := freshEnv(t)
	f := e.useFakeProvider(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	state := follow(t, owner, base+"/oauth/google/authorize").Query().Get("state")
	follow(t, owner, "/v1/oauth/callback?code=good-code&state="+url.QueryEscape(state))
	id := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	// As if its sign-in check had passed.
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET status = 'active' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}

	// The provider is down: noted on the mailbox, which stays active.
	got := track.Capture(t)
	f.setRefresh("down")
	if _, err := e.sync.Sync(ctx, ws, id); err != nil {
		t.Fatalf("outage returned %v", err)
	}
	m := owner.must(200, "GET", base+"/mailboxes/"+id, nil)
	if m["status"] != "active" || m["last_synced_at"] == nil || !strings.Contains(m["sync_error"].(string), "temporarily_unavailable") {
		t.Fatalf("after outage: %v", m)
	}
	if ev := named(got(), inbox.EventSyncFailed); len(ev) != 1 || ev[0].ErrorCode != "mailbox_auth" || ev[0].Props["sign_in_lost"] != false {
		t.Fatalf("outage events: %+v", ev)
	}

	// The grant is revoked: the mailbox turns to error with the reason.
	got = track.Capture(t)
	f.setRefresh("revoked")
	if _, err := e.sync.Sync(ctx, ws, id); err != nil {
		t.Fatalf("revoked returned %v", err)
	}
	m = owner.must(200, "GET", base+"/mailboxes/"+id, nil)
	if m["status"] != "error" || m["imap_ok"] != false || !strings.Contains(m["status_reason"].(string), "invalid_grant") ||
		!strings.Contains(m["sync_error"].(string), "invalid_grant") {
		t.Fatalf("after revoke: %v", m)
	}
	ev := got()
	if failed := named(ev, inbox.EventSyncFailed); len(failed) != 1 || failed[0].ErrorCode != "mailbox_sign_in_lost" || failed[0].WorkspaceID != ws {
		t.Fatalf("revoke events: %+v", failed)
	}
	if v := named(ev, "mailbox.verify_failed"); len(v) != 1 {
		t.Fatalf("verify_failed: %+v", v)
	}

	// It is no longer queued, and a second failure records nothing twice.
	if n, err := e.sync.QueueDue(ctx); err != nil || n != 0 {
		t.Fatalf("queued %d, %v", n, err)
	}
	got = track.Capture(t)
	if _, err := e.sync.Sync(ctx, ws, id); err != nil {
		t.Fatal(err)
	}
	if v := named(got(), "mailbox.verify_failed"); len(v) != 0 {
		t.Fatalf("recorded again: %+v", v)
	}
}
