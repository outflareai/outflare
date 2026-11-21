package api_test

import (
	"context"
	"io"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/outflareai/outflare/engine/internal/adconv"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/customers"
	"github.com/outflareai/outflare/engine/internal/track"
)

// An audience is one of the brand's lists, sent to the ad accounts the
// brand chooses: the first sync makes the list on the network and adds
// who may be added, later ones send only what changed.
func TestAdAudiences(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	metaAudience.Lock()
	metaAudience.calls = nil
	metaAudience.Unlock()
	brand := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	b := "/v1/brands/" + brand + "/ads/audiences"

	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "meta_ads", "credentials": map[string]string{"api_key": "mt"}})
	if _, err := e.ads.SyncOnce(ctx, ws, i["id"].(string)); err != nil {
		t.Fatal(err)
	}
	acct := owner.must(200, "GET", base+"/ads/accounts", nil)["data"].([]any)[0].(map[string]any)["id"].(string)

	// Ana is in the US, Ben in Germany without consent, and Dan has
	// nothing to match on.
	person := func(email, country string) string {
		t.Helper()
		id, err := customers.EnsurePerson(ctx, e.pool, ws, "email", email, true, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (workspace_id, anonymous_id, record_id, country, started_at, last_seen_at)
			VALUES ($1, $2, $3, $4, now(), now())`, ws, "anon-"+email, id, country); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ana, ben := person("ana@acme.example", "US"), person("ben@northpeak.example", "DE")
	dan := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": "Dan"})["id"].(string)
	var list, companies string
	if err := e.pool.QueryRow(ctx, `INSERT INTO smart_lists (workspace_id, brand_id, name, kind) VALUES ($1, $2, 'Big spenders', 'static') RETURNING id`, ws, brand).Scan(&list); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO smart_lists (workspace_id, brand_id, name, kind, record_type) VALUES ($1, $2, 'Accounts', 'static', 'account') RETURNING id`, ws, brand).Scan(&companies); err != nil {
		t.Fatal(err)
	}
	join := func(ids ...string) {
		t.Helper()
		for _, id := range ids {
			if _, err := e.pool.Exec(ctx, `INSERT INTO static_list_members (workspace_id, list_id, record_id) VALUES ($1, $2, $3)`, ws, list, id); err != nil {
				t.Fatal(err)
			}
		}
	}
	join(ana, ben, dan)

	// The page lists the accounts an audience can go to.
	page := owner.must(200, "GET", b, nil)
	if a := page["accounts"].([]any); len(page["data"].([]any)) != 0 || len(a) != 1 || a[0].(map[string]any)["can_sync"] != true || a[0].(map[string]any)["connector_name"] != "Meta Ads" {
		t.Fatalf("page: %v", page)
	}
	for _, bad := range []map[string]any{
		{"name": "", "list_id": list},
		{"name": "Spenders", "list_id": "nope"},
		{"name": "Spenders", "list_id": companies},
		{"name": "Spenders", "list_id": list, "ad_account_ids": []string{brand}},
	} {
		owner.must(400, "POST", b, bad)
	}
	a := owner.must(201, "POST", b, map[string]any{"name": "Spenders", "list_id": list, "ad_account_ids": []string{acct}})
	id := a["id"].(string)
	dest := a["destinations"].([]any)[0].(map[string]any)
	if a["enabled"] != true || a["list"].(map[string]any)["name"] != "Big spenders" || dest["status"] != "waiting" || dest["external_id"] != "" || dest["connector"] != "meta_ads" {
		t.Fatalf("created: %v", a)
	}
	owner.must(409, "POST", b, map[string]any{"name": "spenders", "list_id": list})
	owner.must(404, "GET", b+"/"+brand, nil)

	// The first sync makes the list and adds Ana; Ben has no consent
	// and Dan nothing to match on.
	s := &adconv.Sender{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	sync := func() adconv.AudienceResult {
		t.Helper()
		res, err := s.SyncAudience(ctx, ws, dest["id"].(string))
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := sync(); !res.Created || res.Added != 1 || res.Skipped != 2 || res.Members != 1 || res.Removed != 0 {
		t.Fatalf("first sync: %+v", res)
	}
	calls := func() []string {
		metaAudience.Lock()
		defer metaAudience.Unlock()
		return slices.Clone(metaAudience.calls)
	}
	anaHash := connectors.HashID("ana@acme.example")
	if c := calls(); len(c) != 1 || c[0] != "POST EMAIL_SHA256 "+anaHash {
		t.Fatalf("calls: %v", c)
	}
	d := owner.must(200, "GET", b+"/"+id, nil)["destinations"].([]any)[0].(map[string]any)
	if d["status"] != "synced" || d["external_id"] != "801" || d["members"] != 1.0 || d["skipped"] != 2.0 || d["synced_at"] == nil {
		t.Fatalf("after sync: %v", d)
	}
	// Nothing changed: nothing is sent again.
	if res := sync(); res.Created || res.Added != 0 || res.Removed != 0 || res.Members != 1 || len(calls()) != 1 {
		t.Fatalf("second sync: %+v %v", res, calls())
	}
	// Ana leaves the list: she comes out.
	if _, err := e.pool.Exec(ctx, `DELETE FROM static_list_members WHERE list_id = $1 AND record_id = $2`, list, ana); err != nil {
		t.Fatal(err)
	}
	if res := sync(); res.Removed != 1 || res.Members != 0 {
		t.Fatalf("third sync: %+v", res)
	}
	if c := calls(); len(c) != 2 || c[1] != "DELETE EMAIL_SHA256 "+anaHash {
		t.Fatalf("calls: %v", c)
	}

	// The file holds whoever may be added, hashed.
	join(ana)
	code, ct, body := owner.text(b + "/" + id + "/file")
	if code != 200 || !strings.HasPrefix(ct, "text/csv") || body != "email_sha256,phone_number_sha256\n"+anaHash+",\n" {
		t.Fatalf("file: %d %s %q", code, ct, body)
	}

	// Asking for a sync queues one; an audience that is off can't sync.
	if q := owner.must(202, "POST", b+"/"+id+"/sync", nil); q["queued"] != true {
		t.Fatalf("sync: %v", q)
	}
	off := owner.must(200, "PATCH", b+"/"+id, map[string]any{"enabled": false, "name": "Top spenders"})
	if off["enabled"] != false || off["name"] != "Top spenders" || len(off["destinations"].([]any)) != 1 {
		t.Fatalf("off: %v", off)
	}
	owner.must(409, "POST", b+"/"+id+"/sync", nil)
	if _, err := s.SyncAudience(ctx, ws, dest["id"].(string)); err != connectors.ErrNotFound {
		t.Fatalf("an audience that is off: %v", err)
	}
	// Taking the account away leaves the audience with its file only.
	if none := owner.must(200, "PATCH", b+"/"+id, map[string]any{"ad_account_ids": []string{}}); len(none["destinations"].([]any)) != 0 {
		t.Fatalf("no destinations: %v", none)
	}
	owner.must(204, "DELETE", b+"/"+id, nil)
	owner.must(404, "GET", b+"/"+id, nil)

	names := map[string]bool{}
	for _, ev := range got() {
		names[ev.Name] = true
	}
	for _, name := range []string{"ad_audience.created", "ad_audience.updated", "ad_audience.sync_requested", "ad_audience.exported", "ad_audience.deleted"} {
		if !names[name] {
			t.Errorf("no %s event", name)
		}
	}
}
