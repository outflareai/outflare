package api_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOutcomesAPI(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	seq := owner.must(201, "POST", base+"/sequences", map[string]any{"name": "Agencies"})["id"].(string)
	var lead string
	if err := e.pool.QueryRow(ctx, `INSERT INTO leads (workspace_id, email) VALUES ($1, 'sam@acme.test') RETURNING id`, ws).Scan(&lead); err != nil {
		t.Fatal(err)
	}

	// A meeting logged by hand against the sequence; a bad one is refused
	// with a reason.
	owner.must(400, "POST", base+"/meetings", map[string]any{"starts_at": "2026-10-20T10:00:00Z"})
	m := owner.must(201, "POST", base+"/meetings", map[string]any{
		"lead_id": lead, "sequence_id": seq, "starts_at": "2026-10-20T10:00:00Z", "booked_at": "2026-10-15T10:00:00Z", "title": "Intro"})
	if m["attribution"] != "manual" || m["sequence_name"] != "Agencies" || m["attendee_email"] != "sam@acme.test" {
		t.Fatalf("meeting: %v", m)
	}
	owner.must(200, "PATCH", base+"/meetings/"+m["id"].(string), map[string]any{"status": "held", "qualified": true})

	// Spend: money, units with a rate, and a retried ref.
	owner.must(201, "POST", base+"/spend", map[string]any{"category": "data", "provider": "Apollo", "amount_cents": 5000,
		"sequence_id": seq, "occurred_at": "2026-10-14T00:00:00Z", "ref": "apollo:run:1"})
	owner.must(200, "POST", base+"/spend", map[string]any{"category": "data", "provider": "Apollo", "amount_cents": 5000,
		"sequence_id": seq, "occurred_at": "2026-10-14T00:00:00Z", "ref": "apollo:run:1"})
	owner.must(201, "POST", base+"/spend", map[string]any{"category": "verification", "provider": "ZeroBounce", "units": 2500,
		"unit_name": "credit", "sequence_id": seq, "occurred_at": "2026-10-14T00:00:00Z"})
	owner.must(400, "POST", base+"/spend", map[string]any{"category": "data"})
	rates := owner.must(200, "GET", base+"/spend-rates", nil)["data"].([]any)
	if len(rates) != 1 || rates[0].(map[string]any)["unpriced_units"].(float64) != 2500 {
		t.Fatalf("rates before pricing: %v", rates)
	}
	owner.must(200, "PUT", base+"/spend-rates", map[string]any{"provider": "ZeroBounce", "unit_name": "credit", "unit_price_micros": 2000})
	owner.must(201, "POST", base+"/deals", map[string]any{"name": "Acme", "lead_id": lead, "state": "won", "amount_cents": 1200000,
		"opened_at": "2026-10-21T00:00:00Z"})

	rep := owner.must(200, "GET", base+"/outcomes/report?from=2026-10-01&to=2026-10-31&group_by=sequence", nil)
	tot := rep["totals"].(map[string]any)
	// $50 + 2500 credits at $0.002 = $55, one held meeting.
	if tot["meetings"].(float64) != 1 || tot["held"].(float64) != 1 || tot["spend"].(map[string]any)["total_cents"].(float64) != 5500 ||
		tot["cost_per_meeting_cents"].(float64) != 5500 || tot["won_cents"].(float64) != 1200000 {
		t.Fatalf("report totals: %v", tot)
	}
	owner.must(400, "GET", base+"/outcomes/report?from=nope", nil)
	owner.must(400, "GET", base+"/outcomes/report?group_by=team", nil)

	// CSV export for a warehouse or a spreadsheet.
	req := httptest.NewRequest("GET", base+"/outcomes/export?dataset=meetings&from=2026-10-01&to=2026-10-31", nil)
	req.AddCookie(owner.cookie)
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.HasPrefix(rec.Body.String(), "id,starts_at") || !strings.Contains(rec.Body.String(), "Agencies") {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}

	// A read-only token sees outcomes but cannot log them.
	tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reports", "scopes": []string{"outcomes.read"}})["token"].(string)
	bot := &client{t: t, h: e.h, bearer: tok}
	bot.must(200, "GET", base+"/meetings", nil)
	bot.must(403, "POST", base+"/spend", map[string]any{"category": "data", "amount_cents": 1})

	// Another workspace's ids are refused.
	var other, otherSeq string
	if err := e.pool.QueryRow(ctx, `INSERT INTO workspaces (name, slug) VALUES ('Other', 'other') RETURNING id`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO sequences (workspace_id, name) VALUES ($1, 'Theirs') RETURNING id`, other).Scan(&otherSeq); err != nil {
		t.Fatal(err)
	}
	owner.must(400, "POST", base+"/spend", map[string]any{"category": "data", "amount_cents": 1, "sequence_id": otherSeq})
	owner.must(400, "POST", base+"/meetings", map[string]any{"lead_id": lead, "sequence_id": otherSeq, "starts_at": "2026-10-20T10:00:00Z"})

	// Every write is audited.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action IN
		('meeting.created', 'meeting.updated', 'spend.recorded', 'spend.rate_set', 'deal.created')`, ws).Scan(&n); err != nil || n != 6 {
		t.Fatalf("audit events: %d %v", n, err)
	}
}
