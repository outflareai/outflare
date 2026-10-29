package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/digests"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A workspace's own model is credited like the built-in ones, a change is a
// new version, a pinned version keeps its credits, and reports say which
// version they read.
func TestAttributionCustomModels(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	exec := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, sql, args...).Scan(&id); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return id
	}
	rec := exec(`INSERT INTO records (workspace_id, record_type, name) VALUES ($1, 'person', 'Ana') RETURNING id::text`, ws)
	now := time.Now()
	day := 24 * time.Hour
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, referrer_host) VALUES ($1, $2, 'anon-1', $3, 'www.google.com') RETURNING id::text`,
		ws, rec, now.Add(-20*day))
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium) VALUES ($1, $2, 'anon-1', $3, 'google', 'cpc') RETURNING id::text`,
		ws, rec, now.Add(-10*day))
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium) VALUES ($1, $2, 'anon-1', $3, 'outflare', 'email') RETURNING id::text`,
		ws, rec, now.Add(-2*day))
	exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'signed_up', $3) RETURNING id::text`,
		ws, rec, now.Add(-day))
	run := &attribution.Runner{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Minute}
	tick := func() {
		t.Helper()
		if _, _, err := run.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	winner := func(model string) (string, any) {
		t.Helper()
		path := base + "/attribution/channels"
		if model != "" {
			path += "?model=" + model
		}
		res := owner.must(200, "GET", path, nil)
		for _, x := range res["data"].([]any) {
			if m := x.(map[string]any); m["conversions"] == float64(1) {
				return m["channel"].(string), res["model_version"]
			}
		}
		return "", res["model_version"]
	}
	if ch, v := winner("last_non_direct"); ch != "lifecycle_email" || v != nil {
		t.Fatalf("built in: %s %v", ch, v)
	}

	list := owner.must(200, "GET", base+"/attribution/models", nil)
	if len(list["built_in"].([]any)) != 5 || len(list["data"].([]any)) != 0 {
		t.Fatalf("models = %v", list)
	}
	for _, bad := range []map[string]any{
		{"key": "linear", "name": "Mine", "spec": map[string]any{"base": "linear"}},
		{"key": "Paid", "name": "Mine", "spec": map[string]any{"base": "linear"}},
		{"key": "paid-first", "name": "Mine", "spec": map[string]any{"base": "w_shaped"}},
		{"key": "paid-first", "name": "Mine", "spec": map[string]any{"base": "linear", "exclude_channels": []string{"tv"}}},
		{"key": "paid-first", "spec": map[string]any{"base": "linear"}},
	} {
		owner.must(400, "POST", base+"/attribution/models", bad)
	}
	// First touch, leaving organic search out: the paid click.
	m := owner.must(201, "POST", base+"/attribution/models", map[string]any{"key": "paid-first", "name": "Paid first",
		"spec": map[string]any{"base": "first_touch", "exclude_channels": []string{"seo"}}})
	if m["version"] != float64(1) || len(m["pinned_by"].([]any)) != 0 {
		t.Fatalf("created %v", m)
	}
	owner.must(409, "POST", base+"/attribution/models", map[string]any{"key": "paid-first", "name": "Again", "spec": map[string]any{"base": "linear"}})
	if s := owner.must(200, "GET", base+"/attribution/settings", nil); s["backfilled_at"] != nil {
		t.Fatalf("not credited again: %v", s)
	}
	tick()
	if ch, v := winner("paid-first"); ch != "paid_ads" || v != float64(1) {
		t.Fatalf("v1: %s %v", ch, v)
	}
	// The built-in credits are still there after the recredit.
	if ch, _ := winner("last_non_direct"); ch != "lifecycle_email" {
		t.Fatalf("built in after recredit: %s", ch)
	}

	// An affiliate program pins version 1.
	if _, err := attribution.Pin(ctx, e.pool, ws, "paid-first", "partners", "program-1"); err != nil {
		t.Fatal(err)
	}
	// Paid ads only count for 7 days now: version 2 credits email.
	m = owner.must(200, "PATCH", base+"/attribution/models/paid-first", map[string]any{
		"spec": map[string]any{"base": "first_touch", "exclude_channels": []string{"seo"}, "channel_lookback_days": map[string]any{"paid_ads": 7}}})
	if m["version"] != float64(2) {
		t.Fatalf("updated %v", m)
	}
	// A new name alone keeps the version.
	if m = owner.must(200, "PATCH", base+"/attribution/models/paid-first", map[string]any{"name": "Paid first, 7 days"}); m["version"] != float64(2) || m["name"] != "Paid first, 7 days" {
		t.Fatalf("renamed %v", m)
	}
	tick()
	if ch, v := winner("paid-first"); ch != "lifecycle_email" || v != float64(2) {
		t.Fatalf("v2: %s %v", ch, v)
	}
	if ch, v := winner("paid-first@1"); ch != "paid_ads" || v != float64(1) {
		t.Fatalf("pinned v1: %s %v", ch, v)
	}
	versions := owner.must(200, "GET", base+"/attribution/models/paid-first", nil)["data"].([]any)
	if len(versions) != 2 || len(versions[1].(map[string]any)["pinned_by"].([]any)) != 1 {
		t.Fatalf("versions %v", versions)
	}

	// The default can be a workspace model, which can't then be archived.
	owner.must(400, "PATCH", base+"/attribution/settings", map[string]any{"model": "paid-first@2"})
	owner.must(200, "PATCH", base+"/attribution/settings", map[string]any{"model": "paid-first"})
	if ch, v := winner(""); ch != "lifecycle_email" || v != float64(2) {
		t.Fatalf("default: %s %v", ch, v)
	}
	// The weekly digest reads the default model's credits too.
	secs, err := digests.Build(ctx, e.pool, ws, digests.Period{Start: now.AddDate(0, 0, -7), End: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, sec := range secs {
		for _, m := range sec.Metrics {
			if m.Key == "channel_conversions" {
				found = true
				if m.Value != 1 {
					t.Errorf("digest under the workspace's model: %v conversions", m.Value)
				}
			}
		}
	}
	if !found {
		t.Error("no channel_conversions in the digest")
	}
	owner.must(409, "DELETE", base+"/attribution/models/paid-first", nil)
	owner.must(200, "PATCH", base+"/attribution/settings", map[string]any{"model": "last_non_direct"})
	owner.must(204, "DELETE", base+"/attribution/models/paid-first", nil)
	owner.must(400, "GET", base+"/attribution/channels?model=paid-first", nil)
	owner.must(400, "GET", base+"/attribution/channels?model=paid-first@2", nil)
	// The pinned version still reads after the model is archived.
	if ch, _ := winner("paid-first@1"); ch != "paid_ads" {
		t.Fatalf("pinned after archive: %s", ch)
	}
	owner.must(404, "GET", base+"/attribution/models/nope", nil)

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"attribution.model_created", "attribution.model_updated", "attribution.model_renamed", "attribution.model_archived"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
