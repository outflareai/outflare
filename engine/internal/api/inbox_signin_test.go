package api_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/inbox"
	"github.com/outflareai/outflare/engine/internal/mailbox/mailtest"
	"github.com/outflareai/outflare/engine/internal/oauth"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A mailbox whose OAuth grant was revoked asks to be connected again and
// stops syncing, instead of failing every few minutes unseen. A provider
// outage is only noted, and the mailbox keeps its turn.
func TestInboxSyncRevokedSignIn(t *testing.T) {
	e := freshEnv(t)
	f := e.useMailFake(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// A Gmail sign-in whose connection has no access token yet, so every
	// sign-in asks Google for one.
	id := legacyMailbox(t, e, ws, "li@gw.test", "google", "rt-li", nil)
	if _, err := oauth.MoveLegacy(ctx, e.integrations, slog.Default()); err != nil {
		t.Fatal(err)
	}

	// The provider is down: noted on the mailbox, which stays active.
	got := track.Capture(t)
	f.setRefresh("down")
	if _, err := e.sync.Sync(ctx, ws, id); err != nil {
		t.Fatalf("outage returned %v", err)
	}
	m := owner.must(200, "GET", base+"/mailboxes/"+id, nil)
	if m["status"] != "active" || m["last_synced_at"] == nil || !strings.Contains(m["sync_error"].(string), "HTTP 503") {
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
	if m["status"] != "error" || m["imap_ok"] != false || !strings.Contains(m["status_reason"].(string), "no longer accepts") ||
		!strings.Contains(m["sync_error"].(string), "no longer accepts") {
		t.Fatalf("after revoke: %v", m)
	}
	ev := got()
	if failed := named(ev, inbox.EventSyncFailed); len(failed) != 1 || failed[0].ErrorCode != "mailbox_sign_in_lost" || failed[0].WorkspaceID != ws ||
		failed[0].Outcome != track.Failure || failed[0].Props["turned_to_error"] != true {
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
	ev = got()
	if v := named(ev, "mailbox.verify_failed"); len(v) != 0 {
		t.Fatalf("recorded again: %+v", v)
	}
	if failed := named(ev, inbox.EventSyncFailed); len(failed) != 1 || failed[0].Props["turned_to_error"] != false || failed[0].Outcome != track.Failure {
		t.Fatalf("second failure events: %+v", failed)
	}
}

// A mailbox whose server is only busy keeps its turn, under its own code,
// and is tried less often while it keeps failing. One whose server keeps
// refusing the password turns to error after inbox.RejectedLimit syncs in a
// row, instead of failing every few minutes forever.
func TestInboxSyncRefusedSignIn(t *testing.T) {
	e := freshEnv(t)
	srv := mailtest.Start(t, map[string]string{"ana@acme.test": "app-pass-1", "bo@acme.test": "app-pass-2"})
	e.mail.Dialer.TLSConfig = srv.ClientTLS
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	csv := fmt.Sprintf("Email,Password,SMTP Host,SMTP Port,IMAP Host,IMAP Port\n"+
		"ana@acme.test,app-pass-1,%[1]s,%[2]d,%[1]s,%[3]d\n"+
		"bo@acme.test,changed-since,%[1]s,%[2]d,%[1]s,%[3]d\n", srv.Host, srv.SMTPPort, srv.IMAPPort)
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv", csv); code != 200 || res["created"] != 2.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	ids := map[string]string{}
	for _, m := range owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any) {
		ids[m.(map[string]any)["email"].(string)] = m.(map[string]any)["id"].(string)
	}
	// Both were working when connected; bo's password has changed since.
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET status = 'active', has_login = true WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	failures := func(id string) int {
		t.Helper()
		var n int
		if err := e.pool.QueryRow(ctx, `SELECT sync_failures FROM mailboxes WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Busy: noted, its own code, still active.
	ana := ids["ana@acme.test"]
	srv.Busy("ana@acme.test", true)
	got := track.Capture(t)
	if _, err := e.sync.Sync(ctx, ws, ana); err != nil {
		t.Fatal(err)
	}
	if ev := named(got(), inbox.EventSyncFailed); len(ev) != 1 || ev[0].ErrorCode != "mailbox_throttled" || ev[0].Props["sign_in_lost"] != false || ev[0].Outcome != track.Error {
		t.Fatalf("busy events: %+v", ev)
	}
	if m := owner.must(200, "GET", base+"/mailboxes/"+ana, nil); m["status"] != "active" || m["sync_error"] == nil || failures(ana) != 1 {
		t.Fatalf("after busy: %v", m)
	}
	// After one failure it waits 4 minutes, not 2.
	due := func(ago string) int {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET last_synced_at = now() WHERE workspace_id = $1 AND id <> $2`, ws, ana); err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET last_synced_at = now() - $2::interval WHERE id = $1`, ana, ago); err != nil {
			t.Fatal(err)
		}
		n, err := e.sync.QueueDue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := due("3 minutes"); n != 0 {
		t.Fatalf("queued %d after 3 minutes", n)
	}
	if n := due("5 minutes"); n != 1 {
		t.Fatalf("queued %d after 5 minutes", n)
	}
	// Busy again and again never counts as a lost sign-in.
	for range inbox.RejectedLimit {
		if _, err := e.sync.Sync(ctx, ws, ana); err != nil {
			t.Fatal(err)
		}
	}
	if m := owner.must(200, "GET", base+"/mailboxes/"+ana, nil); m["status"] != "active" {
		t.Fatalf("busy turned to error: %v", m)
	}
	// Working again: the count starts over.
	srv.Busy("ana@acme.test", false)
	if _, err := e.sync.Sync(ctx, ws, ana); err != nil {
		t.Fatal(err)
	}
	if m := owner.must(200, "GET", base+"/mailboxes/"+ana, nil); m["sync_error"] != nil || failures(ana) != 0 {
		t.Fatalf("after recovery: %v, %d", m, failures(ana))
	}

	// Refused: a note until the limit, then error with the reason.
	bo := ids["bo@acme.test"]
	got = track.Capture(t)
	for i := 1; i < inbox.RejectedLimit; i++ {
		if _, err := e.sync.Sync(ctx, ws, bo); err != nil {
			t.Fatal(err)
		}
	}
	if m := owner.must(200, "GET", base+"/mailboxes/"+bo, nil); m["status"] != "active" {
		t.Fatalf("turned to error too soon: %v", m)
	}
	if ev := named(got(), inbox.EventSyncFailed); len(ev) != inbox.RejectedLimit-1 || ev[0].ErrorCode != "mailbox_auth" || ev[0].Outcome != track.Failure {
		t.Fatalf("refused events: %+v", ev)
	}
	got = track.Capture(t)
	if _, err := e.sync.Sync(ctx, ws, bo); err != nil {
		t.Fatal(err)
	}
	m := owner.must(200, "GET", base+"/mailboxes/"+bo, nil)
	if m["status"] != "error" || m["imap_ok"] != false || !strings.Contains(m["status_reason"].(string), "connect the mailbox again") {
		t.Fatalf("after %d refusals: %v", inbox.RejectedLimit, m)
	}
	ev := got()
	if failed := named(ev, inbox.EventSyncFailed); len(failed) != 1 || failed[0].ErrorCode != "mailbox_sign_in_lost" ||
		failed[0].Outcome != track.Failure || failed[0].Props["turned_to_error"] != true {
		t.Fatalf("lost events: %+v", failed)
	}
	if v := named(ev, "mailbox.verify_failed"); len(v) != 1 {
		t.Fatalf("verify_failed: %+v", v)
	}
}

// A burst of new mail is fetched a few messages at a time, each chunk
// stored before the next is asked for, so all of it arrives in one sync
// and a slow server can't make the sync start over.
func TestInboxSyncFetchesInChunks(t *testing.T) {
	e := freshEnv(t)
	srv := mailtest.Start(t, map[string]string{"ana@acme.test": "app-pass-1"})
	e.mail.Dialer.TLSConfig = srv.ClientTLS
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	csv := fmt.Sprintf("Email,Password,SMTP Host,SMTP Port,IMAP Host,IMAP Port\nana@acme.test,app-pass-1,%[1]s,%[2]d,%[1]s,%[3]d\n",
		srv.Host, srv.SMTPPort, srv.IMAPPort)
	if code, res := owner.raw("POST", base+"/mailboxes/import", "text/csv", csv); code != 200 || res["created"] != 1.0 {
		t.Fatalf("import: %d %v", code, res)
	}
	id := owner.must(200, "GET", base+"/mailboxes", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET status = 'active', has_login = true WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	const total = 60 // more than two FETCH chunks
	for i := range total {
		srv.Deliver(t, "ana@acme.test", fmt.Sprintf("From: li%d@one-io.example\nTo: ana@acme.test\nSubject: Hello %d\nMessage-ID: <burst%d@one-io.example>\n\nHi there.\n", i, i, i))
	}
	n, err := e.sync.Sync(ctx, ws, id)
	if err != nil || n != total {
		t.Fatalf("synced %d, %v", n, err)
	}
	var stored int
	var syncErr *string
	if err := e.pool.QueryRow(ctx, `SELECT count(*), (SELECT sync_error FROM mailboxes WHERE id = $1) FROM inbound_messages WHERE mailbox_id = $1`, id).Scan(&stored, &syncErr); err != nil {
		t.Fatal(err)
	}
	if stored != total || syncErr != nil {
		t.Fatalf("stored %d, sync_error %v", stored, syncErr)
	}
	// Nothing new: the next sync reads nothing again.
	if n, err := e.sync.Sync(ctx, ws, id); err != nil || n != 0 {
		t.Fatalf("second sync %d, %v", n, err)
	}
}
