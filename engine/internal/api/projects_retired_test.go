package api_test

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// TestProjectsRetiredMigration runs the data steps of migration 0266 again
// on projects made through the API, and checks nothing a project did is
// lost: briefs, the switch, the cap and the customer filters.
func TestProjectsRetiredMigration(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	branded := owner.must(201, "POST", base+"/projects", map[string]any{
		"name": "Fintech CFOs", "offer": "Month-end close in two days", "audience": "CFOs at Series B fintechs",
		"market": "Australia", "brief": "Lead with the audit story.", "daily_send_limit": 10,
		"icp": map[string]any{"titles": []string{"CFO"}},
	})["id"].(string)
	owner.must(200, "POST", base+"/brands/"+brand+"/add", map[string]any{"project_ids": []string{branded}})
	shared := owner.must(201, "POST", base+"/projects", map[string]any{"name": "Agencies", "offer": "White-label reports"})["id"].(string)
	owner.must(201, "POST", base+"/projects", map[string]any{"name": "Empty"})

	seqs := make([]string, 3)
	for i := range seqs {
		seqs[i] = owner.must(201, "POST", base+"/sequences", map[string]any{"name": fmt.Sprint("S", i), "project_id": branded})["id"].(string)
	}
	other := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Agency", "project_id": shared})["id"].(string)
	list := owner.must(201, "POST", base+"/lead-lists", map[string]any{"name": "CFOs", "project_id": branded})["id"].(string)
	if _, err := e.pool.Exec(ctx, `UPDATE sequences SET status = 'active' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	owner.must(200, "PATCH", base+"/projects/"+shared, map[string]any{"status": "paused"})

	src, err := os.ReadFile("../db/migrations/0266_projects_retired.sql")
	if err != nil {
		t.Fatal(err)
	}
	_, data, ok := strings.Cut(string(src), "-- 1. Customer filters")
	if !ok {
		t.Fatal("no data steps in 0266")
	}
	// The rest of the comment line the marker starts.
	_, data, _ = strings.Cut(data, "\n")
	tx, err := e.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, data); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("data steps: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	// Briefs: the brand's go to its context, the shared one to the company's.
	var brandNotes, companyNotes string
	if err := e.pool.QueryRow(ctx, `SELECT notes FROM brand_context WHERE brand_id = $1`, brand).Scan(&brandNotes); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## Fintech CFOs (was a project)", "**Offer:** Month-end close in two days", "**Audience:** CFOs at Series B fintechs", "**Market:** Australia", "Lead with the audit story."} {
		if !strings.Contains(brandNotes, want) {
			t.Errorf("brand notes lack %q:\n%s", want, brandNotes)
		}
	}
	if err := e.pool.QueryRow(ctx, `SELECT notes FROM workspace_context WHERE workspace_id = $1`, ws).Scan(&companyNotes); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(companyNotes, "## Agencies (was a project)\n\n**Offer:** White-label reports") || strings.Contains(companyNotes, "Empty") || strings.Contains(companyNotes, "Fintech") {
		t.Errorf("company notes:\n%s", companyNotes)
	}

	// The switch and the cap move to the sequences.
	for _, id := range seqs {
		s := owner.must(200, "GET", base+"/sequences/"+id, nil)
		if s["status"] != "active" || s["daily_send_limit"] != 3.0 {
			t.Errorf("sequence in the capped project: %v %v", s["status"], s["daily_send_limit"])
		}
	}
	if s := owner.must(200, "GET", base+"/sequences/"+other, nil); s["status"] != "paused" || s["daily_send_limit"] != nil {
		t.Errorf("sequence in the paused project: %v %v", s["status"], s["daily_send_limit"])
	}
	if p := owner.must(200, "GET", base+"/projects/"+shared, nil); p["status"] != "active" {
		t.Errorf("paused project left paused: %v", p["status"])
	}

	// The list scores against the project's profile.
	l := owner.must(200, "GET", base+"/lead-lists/"+list, nil)
	p := owner.must(200, "GET", base+"/projects/"+branded, nil)
	if l["profile_id"] == nil || l["profile_id"] != p["profile_id"] {
		t.Errorf("list profile %v, project profile %v", l["profile_id"], p["profile_id"])
	}
}
