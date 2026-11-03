package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/api"
	"github.com/outflareai/outflare/engine/internal/rules"
)

// fakeArchive stands in for the ClickHouse event store.
type fakeArchive struct {
	events []rules.ArchivedEvent
	// Events by name, when set (absence rules read two names).
	byName map[string][]rules.ArchivedEvent
	asked  time.Time
}

func (f *fakeArchive) RuleEvents(_ context.Context, _, name string, since time.Time, limit int) ([]rules.ArchivedEvent, error) {
	f.asked = since
	events := f.events
	if f.byName != nil {
		events = f.byName[name]
	}
	var out []rules.ArchivedEvent
	for _, e := range events {
		if !e.OccurredAt.Before(since) && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

// With the event store, an event rule's dry run replays its events from
// there, over up to a year; other rules still replay Postgres, over up to
// 90 days.
func TestRuleSimulationFromTheEventStore(t *testing.T) {
	archive := &fakeArchive{}
	e := freshEnvWith(t, func(o *api.Options) { o.EventArchive = archive })
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	person := func(name, attrs string) string {
		t.Helper()
		id := owner.must(201, "POST", base+"/records", map[string]any{"type": "person", "name": name})["id"].(string)
		if _, err := e.pool.Exec(context.Background(), `UPDATE records SET attributes = $2::jsonb WHERE id = $1`, id, attrs); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ana, bo := person("Ana", `{"plan": "pro"}`), person("Bo", `{"plan": "free"}`)
	rule := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Pro upgrade", "trigger": map[string]any{"kind": "event", "event": "plan_upgraded"},
		"conditions": []map[string]any{{"property": "plan", "op": "eq", "value": "pro"}}, "emit_event": "customer.upgraded_pro", "fire": "once"})["id"].(string)
	now := time.Now()
	archive.events = []rules.ArchivedEvent{
		{RecordID: ana, Properties: map[string]any{"plan": "pro"}, OccurredAt: now.AddDate(0, 0, -200)},
		{RecordID: ana, Properties: map[string]any{"plan": "pro"}, OccurredAt: now.AddDate(0, 0, -150)},
		{RecordID: bo, Properties: map[string]any{"plan": "free"}, OccurredAt: now.AddDate(0, 0, -120)},
	}
	sim := owner.must(200, "POST", base+"/rules/"+rule+"/simulate", map[string]any{"days": 365})
	if sim["source"] != "clickhouse" || sim["records"] != 1.0 || sim["firings"] != 1.0 || sim["checked"] != 3.0 || sim["sample"].([]any)[0] != ana {
		t.Fatalf("simulation: %v", sim)
	}
	if d := time.Since(archive.asked); d < 364*24*time.Hour || d > 366*24*time.Hour {
		t.Fatalf("asked the store since %v", archive.asked)
	}
	owner.must(400, "POST", base+"/rules/"+rule+"/simulate", map[string]any{"days": 366})

	// An absence rule: trials started with no subscription within 14 days.
	// Ana's ran out unanswered; Bo subscribed in time; Cy's is too recent
	// to have run out.
	cy := person("Cy", `{"plan": "free"}`)
	absent := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Trial not converted", "emit_event": "customer.trial_lost",
		"trigger": map[string]any{"kind": "absence", "start_event": "trial_started", "event": "subscribed", "within_seconds": 14 * 86400}})["id"].(string)
	archive.byName = map[string][]rules.ArchivedEvent{
		"trial_started": {
			{RecordID: ana, OccurredAt: now.AddDate(0, 0, -200)},
			{RecordID: bo, OccurredAt: now.AddDate(0, 0, -100)},
			{RecordID: cy, OccurredAt: now.AddDate(0, 0, -3)},
		},
		"subscribed": {{RecordID: bo, OccurredAt: now.AddDate(0, 0, -95)}, {RecordID: ana, OccurredAt: now.AddDate(0, 0, -150)}},
	}
	sim = owner.must(200, "POST", base+"/rules/"+absent+"/simulate", map[string]any{"days": 365})
	if sim["source"] != "clickhouse" || sim["records"] != 1.0 || sim["sample"].([]any)[0] != ana {
		t.Fatalf("absence simulation: %v", sim)
	}
	archive.byName = nil

	// A change rule looks at records as they are now, in Postgres.
	change := owner.must(201, "POST", base+"/rules", map[string]any{"name": "Went pro", "trigger": map[string]any{"kind": "change"},
		"conditions": []map[string]any{{"attribute": "plan", "op": "eq", "value": "pro"}}, "emit_event": "customer.went_pro"})["id"].(string)
	owner.must(400, "POST", base+"/rules/"+change+"/simulate", map[string]any{"days": 200})
	if sim := owner.must(200, "POST", base+"/rules/"+change+"/simulate", map[string]any{"days": 30}); sim["source"] != "postgres" || sim["records"] != 1.0 {
		t.Fatalf("change simulation: %v", sim)
	}
}
