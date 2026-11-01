package api_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"testing"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/catalog"
	"github.com/outflareai/outflare/engine/internal/vendorsync"
)

// Connecting a supplier queues its first sync and its webhook in the same
// transaction, before anyone opens the list; replacing its key reads it
// again. No job runner runs here, so only the writes could have queued them.
func TestSupplierSyncStartsOnConnect(t *testing.T) {
	e := freshEnv(t)
	fake := &fakeCheapInboxes{passwords: map[string]string{}, appPassword: map[string]string{}, hooks: map[string]string{}}
	ci := httptest.NewServer(fake)
	t.Cleanup(ci.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	store := &connectors.Store{Pool: e.pool, Box: e.box, Registry: catalog.New(),
		Client: &connectors.Client{BaseURLs: map[string]string{"cheapinboxes": ci.URL}}}
	vendors := &vendorsync.Service{Pool: e.pool, Box: e.box, Store: store, Mail: e.mail, Log: log, PublicURL: "https://outflare.example.com"}
	store.OnConnect = append(store.OnConnect, vendors.Connected)
	store.OnReconnect = append(store.OnReconnect, vendors.Reconnected)
	h := handler(t, api.NewServer(e.pool, log, api.Options{Mailboxes: e.mail, Sending: e.send, Inbox: e.sync, OAuth: e.oauth,
		Box: e.box, Integrations: store, Vendors: vendors}))
	owner, ws := setupOwner(t, h)
	base := "/v1/workspaces/" + ws

	ctx := context.Background()
	count := func(kind, id string) (n int) {
		t.Helper()
		if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND payload->>'integration_id' = $2`, kind, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	id := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "cheapinboxes", "credentials": map[string]string{"api_key": "ci_live_test"}})["id"].(string)
	var syncing bool
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vendor_syncs WHERE workspace_id = $1 AND integration_id = $2)`, ws, id).Scan(&syncing); err != nil || !syncing {
		t.Fatalf("not syncing after connect: %v %v", syncing, err)
	}
	if s, w := count(vendorsync.JobSync, id), count(vendorsync.JobWebhook, id); s != 1 || w != 1 {
		t.Fatalf("after connect: %d syncs, %d webhooks", s, w)
	}
	// Its settings read straight away: nothing waits for the list.
	if st := owner.must(200, "GET", base+"/integrations/"+id+"/mailbox-sync", nil); st["last_synced_at"] != nil {
		t.Fatalf("settings: %v", st)
	}

	// A new key reads the supplier again and retries its webhook.
	owner.must(200, "PATCH", base+"/integrations/"+id, map[string]any{"credentials": map[string]string{"api_key": "ci_live_test"}})
	if s, w := count(vendorsync.JobSync, id), count(vendorsync.JobWebhook, id); s != 2 || w != 2 {
		t.Fatalf("after reconnect: %d syncs, %d webhooks", s, w)
	}
	// Renaming is not a reconnect.
	owner.must(200, "PATCH", base+"/integrations/"+id, map[string]any{"name": "Supplier"})
	if n := count(vendorsync.JobSync, id); n != 2 {
		t.Fatalf("after rename: %d syncs", n)
	}

	// A supplier whose sync never started reads as not synced, not an
	// error, and Sync now starts it.
	if _, err := e.pool.Exec(ctx, `DELETE FROM vendor_syncs WHERE integration_id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if l := owner.must(200, "GET", base+"/integrations/"+id+"/mailboxes", nil); l["synced"] != false {
		t.Fatalf("list without a sync: %v", l)
	}
	owner.must(200, "GET", base+"/integrations/"+id+"/mailbox-sync", nil)
	owner.must(202, "POST", base+"/integrations/"+id+"/mailboxes/sync", nil)
	if err := e.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vendor_syncs WHERE workspace_id = $1 AND integration_id = $2)`, ws, id).Scan(&syncing); err != nil || !syncing {
		t.Fatalf("Sync now didn't start syncing: %v %v", syncing, err)
	}
	if n := count(vendorsync.JobSync, id); n != 3 {
		t.Fatalf("after Sync now: %d syncs", n)
	}
}
