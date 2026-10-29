package api_test

import (
	"fmt"
	"testing"
	"time"
)

func TestSequenceDailySendLimit(t *testing.T) {
	e := freshEnv(t)
	clock := &fakeClock{}
	clock.Set(time.Date(2030, 1, 8, 10, 0, 0, 0, time.UTC))
	owner, base, srv := sendingSetup(t, e, clock)

	_, imp := owner.raw("POST", base+"/leads/import?list=L", "text/csv", "email\none@one-io.example\ntwo@two-io.example\nthree@three-io.example\n")
	owner.must(400, "POST", base+"/sequences", map[string]any{"name": "Zero", "daily_send_limit": 0})
	seq := owner.must(201, "POST", base+"/sequences", map[string]any{
		"name": "Capped", "target_daily": 1000, "daily_send_limit": 1, "send_days": []int{1, 2, 3, 4, 5, 6, 7},
		"steps": []map[string]any{{"subject": "Hello", "body": "Hi there"}},
	})
	seqID := seq["id"].(string)
	if seq["daily_send_limit"] != 1.0 {
		t.Fatalf("created: %v", seq["daily_send_limit"])
	}

	owner.must(200, "POST", base+"/sequences/"+seqID+"/enrollments", map[string]any{"list_id": imp["list_id"]})
	owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"status": "active"})
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := len(srv.Messages()); n != 1 {
		t.Fatalf("sent under a sequence limit of 1: %d", n)
	}
	reasons := map[string]int{}
	for _, en := range enrollments(t, owner, base, seqID) {
		reasons[fmt.Sprint(en["status"], ":", en["status_reason"])]++
	}
	if reasons["active:sequence_daily_send_limit"] != 2 {
		t.Fatalf("reasons: %v", reasons)
	}

	// Lifting it lets the rest go.
	if got := owner.must(200, "PATCH", base+"/sequences/"+seqID, map[string]any{"daily_send_limit": nil})["daily_send_limit"]; got != nil {
		t.Fatalf("lifted: %v", got)
	}
	clock.Add(2 * time.Hour)
	for range 3 {
		e.pump(t)
		clock.Add(5 * time.Minute)
	}
	if n := len(srv.Messages()); n != 3 {
		t.Fatalf("sent after lifting the limit: %d", n)
	}
}
