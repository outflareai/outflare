package api_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/attribution"
	"github.com/outflareai/outflare/engine/internal/track"
)

// A workspace defines its conversions on the customer events stream;
// attribution credits only the events a definition's filters let through,
// with the definition's value.
func TestConversionDefinitions(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	if list := owner.must(200, "GET", base+"/conversion-definitions", nil)["data"].([]any); len(list) != 0 {
		t.Fatalf("definitions = %v", list)
	}
	presets := owner.must(200, "GET", base+"/conversion-definitions/presets", nil)["data"].([]any)
	if len(presets) != 4 {
		t.Fatalf("presets = %d", len(presets))
	}
	for _, bad := range []map[string]any{
		{"key": "Call", "name": "Call", "event": "call.completed"},
		{"key": "call", "name": "Call", "event": "call.completed", "filters": []any{map[string]any{"property": "duration_s", "op": "gt", "value": "long"}}},
		{"key": "call", "name": "Call", "event": "call.completed", "value_mode": "fixed", "fixed_value_minor": 5000},
		{"key": "refund", "name": "Refund", "event": "payment.refunded"},
	} {
		owner.must(400, "POST", base+"/conversion-definitions", bad)
	}
	call := owner.must(201, "POST", base+"/conversion-definitions", map[string]any{
		"key": "qualified_call", "name": "Qualified call", "event": "call.completed",
		"filters":    []any{map[string]any{"property": "duration_s", "op": "gt", "value": 60}},
		"value_mode": "fixed", "fixed_value_minor": 5000, "currency": "USD",
		"network_events": map[string]any{"meta": "Schedule"},
	})
	if call["attribution"] != true || call["value_unit"] != "major" || len(call["filters"].([]any)) != 1 {
		t.Fatalf("created %v", call)
	}
	owner.must(409, "POST", base+"/conversion-definitions", map[string]any{"key": "qualified_call", "name": "Again", "event": "signed_up"})

	// A preset adds what's missing, once.
	first := owner.must(200, "POST", base+"/conversion-definitions/presets/booked_calls", nil)
	if len(first["added"].([]any)) != 3 || len(first["skipped"].([]any)) != 0 {
		t.Fatalf("preset: %v", first)
	}
	if again := owner.must(200, "POST", base+"/conversion-definitions/presets/booked_calls", nil); len(again["added"].([]any)) != 0 || len(again["skipped"].([]any)) != 3 {
		t.Fatalf("preset twice: %v", again)
	}
	owner.must(404, "POST", base+"/conversion-definitions/presets/crypto", nil)

	// A change keeps what it leaves out; switching to a property value
	// clears the fixed one.
	held := owner.must(200, "PATCH", base+"/conversion-definitions/held_call", map[string]any{"value_mode": "property", "value_property": "deal_value"})
	if held["event"] != "meeting.held" || held["value_mode"] != "property" || held["fixed_value_minor"] != nil {
		t.Fatalf("patched %v", held)
	}
	owner.must(404, "PATCH", base+"/conversion-definitions/nope", map[string]any{"name": "x"})
	owner.must(204, "DELETE", base+"/conversion-definitions/deal_won", nil)
	owner.must(409, "PATCH", base+"/conversion-definitions/deal_won", map[string]any{"name": "x"})
	if list := owner.must(200, "GET", base+"/conversion-definitions", nil)["data"].([]any); len(list) != 3 {
		t.Fatalf("active definitions = %d", len(list))
	}
	if list := owner.must(200, "GET", base+"/conversion-definitions?archived=true", nil)["data"].([]any); len(list) != 4 {
		t.Fatalf("all definitions = %d", len(list))
	}
	if d := owner.must(200, "GET", base+"/conversion-definitions/qualified_call", nil); d["name"] != "Qualified call" {
		t.Fatalf("get %v", d)
	}

	// Two calls after a paid click: only the long one counts, at $50.
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
	exec(`INSERT INTO site_sessions (workspace_id, record_id, anonymous_id, started_at, utm_source, utm_medium) VALUES ($1, $2, 'anon-1', $3, 'google', 'cpc') RETURNING id::text`,
		ws, rec, now.Add(-48*time.Hour))
	long := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, occurred_at) VALUES ($1, $2, 'track', 'call.completed', '{"duration_s": 300}', $3) RETURNING id::text`,
		ws, rec, now.Add(-2*time.Hour))
	short := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, properties, occurred_at) VALUES ($1, $2, 'track', 'call.completed', '{"duration_s": 20}', $3) RETURNING id::text`,
		ws, rec, now.Add(-3*time.Hour))
	run := &attribution.Runner{Pool: e.pool, Log: slog.New(slog.DiscardHandler), Settle: time.Minute}
	if _, _, err := run.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	var value int64
	var currency, defKey string
	if err := e.pool.QueryRow(ctx, `SELECT c.value_minor, c.currency, d.key FROM attribution_conversions c JOIN conversion_definitions d ON d.id = c.definition_id
		WHERE c.event_id = $1`, long).Scan(&value, &currency, &defKey); err != nil {
		t.Fatalf("long call not credited: %v", err)
	}
	if value != 5000 || currency != "USD" || defKey != "qualified_call" {
		t.Errorf("long call = %d %s %s", value, currency, defKey)
	}
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribution_conversions WHERE event_id = $1`, short).Scan(&n); err != nil || n != 0 {
		t.Errorf("short call credited: %d %v", n, err)
	}
	// The default events still count as they are.
	signup := exec(`INSERT INTO customer_events (workspace_id, record_id, kind, name, occurred_at) VALUES ($1, $2, 'track', 'signed_up', $3) RETURNING id::text`,
		ws, rec, now.Add(-time.Hour))
	if _, _, err := run.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM attribution_conversions WHERE event_id = $1 AND definition_id IS NULL`, signup).Scan(&n); err != nil || n != 1 {
		t.Errorf("signed_up credited %d times, %v", n, err)
	}

	seen := map[string]bool{}
	for _, ev := range got() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"conversion_definition.created", "conversion_definition.updated", "conversion_definition.archived", "conversion_definitions.preset_added"} {
		if !seen[name] {
			t.Errorf("no %s event", name)
		}
	}
}
