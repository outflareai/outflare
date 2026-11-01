package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

// A decline can be seen, with why it was made, and cleared, which records
// its event and lets the allocator choose that mailbox again.
func TestAllocationDeclinesListAndClear(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, _ := sendingSetup(t, e, clock)
	steps := []map[string]any{{"subject": "Hello", "body": "Hi"}}

	if l := owner.must(200, "GET", base+"/allocation/declines", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("declines before any: %v", l)
	}

	a := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "A", "target_daily": 1, "steps": steps})
	aID := a["id"].(string)
	owner.must(200, "PATCH", base+"/sequences/"+aID, map[string]any{"status": "active"})
	first := only(allocated(t, owner, base, aID))
	if first == "" {
		t.Fatal("nothing allocated")
	}

	// Undoing the allocator's choice declines it, and the list says so.
	changes := owner.must(200, "GET", base+"/allocation/changes?sequence_id="+aID, nil)["data"].([]any)
	owner.must(200, "POST", base+"/allocation/changes/"+changes[0].(map[string]any)["id"].(string)+"/undo", nil)
	list := owner.must(200, "GET", base+"/allocation/declines?sequence_id="+aID, nil)
	data := list["data"].([]any)
	if len(data) != 1 || list["next_offset"] != nil {
		t.Fatalf("declines: %v", list)
	}
	d := data[0].(map[string]any)
	if d["mailbox_email"] != first || d["sequence_name"] != "A" || d["source"] != "undone" || d["actor_name"] == nil || d["declined_at"] == nil {
		t.Fatalf("decline: %v", d)
	}
	if l := owner.must(200, "GET", base+"/allocation/declines?mailbox_id=00000000-0000-4000-8000-000000000000", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("other mailbox's declines: %v", l)
	}
	owner.must(400, "GET", base+"/allocation/declines?sequence_id=nope", nil)

	// Clearing it removes it and records the event, in the same transaction.
	id := d["id"].(string)
	owner.must(404, "DELETE", base+"/allocation/declines/not-a-uuid", nil)
	owner.must(204, "DELETE", base+"/allocation/declines/"+id, nil)
	if l := owner.must(200, "GET", base+"/allocation/declines", nil)["data"].([]any); len(l) != 0 {
		t.Fatalf("after clearing: %v", l)
	}
	cleared := named(events(), "mailbox.allocation.decline_cleared")
	if len(cleared) != 1 || cleared[0].Outcome != "success" || cleared[0].Props["target_type"] != "mailbox" || cleared[0].Props["target_id"] != d["mailbox_id"] {
		t.Fatalf("event: %+v", cleared)
	}
	var audited int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE action = 'mailbox.allocation.decline_cleared'`).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("audit: %d %v", audited, err)
	}
	owner.must(404, "DELETE", base+"/allocation/declines/"+id, nil)
}
