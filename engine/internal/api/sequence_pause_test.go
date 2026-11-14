package api_test

import (
	"testing"
	"time"
)

// A paused campaign sends nothing, keeps every lead where it was, and on
// resume carries on from the next step through the same mailbox, so a
// thread never changes sender across a pause.
func TestSequencePauseThenResume(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\none@one-io.example\ntwo@two-io.example\n")
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Paused", "daily_new_limit": 10, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{
			{"subject": "Hello", "body": "Hi there"},
			{"delay_days": 1, "subject": "", "body": "Bumping this."},
		},
	})
	seqID := seq["id"].(string)
	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	first := srv.Messages()
	if len(first) != 2 {
		t.Fatalf("first step: %d sent", len(first))
	}
	from := map[string]string{}
	for _, m := range first {
		from[m.To[0]] = parse(t, m).Header.Get("From")
	}

	// Paused past the follow-up's due time: nothing goes out, and no lead
	// is completed, dropped or moved to another mailbox.
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "paused"})
	clock.Add(2 * 24 * time.Hour)
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := len(srv.Messages()); n != 2 {
		t.Fatalf("sent while paused: %d", n)
	}
	for who, en := range enrollments(t, owner, base, seqID) {
		if en["status"] != "active" {
			t.Fatalf("%s while paused: %v", who, en)
		}
	}

	// Resumed: each lead gets the follow-up, once, from its first sender.
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	all := srv.Messages()
	if len(all) != 4 {
		t.Fatalf("after resume: %d sent", len(all))
	}
	for _, m := range all[2:] {
		if got := parse(t, m).Header.Get("From"); got != from[m.To[0]] {
			t.Fatalf("%s: follow-up from %s, first from %s", m.To[0], got, from[m.To[0]])
		}
	}
	for who, en := range enrollments(t, owner, base, seqID) {
		if en["status"] != "completed" {
			t.Fatalf("%s after resume: %v", who, en)
		}
	}
}
