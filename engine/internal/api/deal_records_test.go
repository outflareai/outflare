package api_test

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/outcomes"
)

// Every deal is one CRM record: a deal logged for outreach is a Deal record
// with the same id, linked to its person and company, and a deal made in
// the CRM is counted in outcomes, whichever side changes.
func TestDealIsOneRecord(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	acme := owner.must(201, "POST", base+"/records", map[string]any{"type": "account", "name": "Acme"})["id"].(string)
	lead := owner.must(201, "POST", base+"/leads", map[string]any{"email": "sam@acme.example", "first_name": "Sam"})
	sam := lead["record_id"].(string)
	owner.must(200, "PATCH", base+"/records/"+sam, map[string]any{"attributes": map[string]any{"companies": []string{acme}}})

	// A deal logged for outreach is a deal record too.
	d := owner.must(201, "POST", base+"/deals", map[string]any{"name": "Acme pilot", "lead_id": lead["id"], "stage": "Proposal",
		"amount_cents": 500000, "currency": "EUR", "opened_at": "2026-10-01T00:00:00Z"})
	id := d["id"].(string)
	rec := owner.must(200, "GET", base+"/records/"+id, nil)
	attrs := rec["attributes"].(map[string]any)
	if rec["type"] != "deal" || rec["name"] != "Acme pilot" || attrs["stage"] != "proposal" ||
		fmt.Sprint(attrs["value"]) != "map[amount_minor:500000 currency:EUR]" ||
		fmt.Sprint(attrs["associated_people"]) != fmt.Sprint([]any{sam}) || attrs["associated_company"] != acme {
		t.Fatalf("deal record: %v", rec)
	}
	// It is on Sam's and Acme's links, and in the deal's history.
	links := owner.must(200, "GET", base+"/records/"+acme+"/relationships", nil)["data"].([]any)
	if !slices.ContainsFunc(links, func(l any) bool { return l.(map[string]any)["field_key"] == "associated_company" }) {
		t.Fatalf("acme links: %v", links)
	}
	hist := owner.must(200, "GET", base+"/records/"+id+"/history", nil)["data"].([]any)
	if len(hist) < 4 {
		t.Fatalf("history: %v", hist)
	}

	// Won through the outcomes API: the record's stage and close date follow.
	owner.must(200, "PATCH", base+"/deals/"+id, map[string]any{"state": "won", "closed_at": "2026-10-20T09:00:00Z"})
	attrs = owner.must(200, "GET", base+"/records/"+id, nil)["attributes"].(map[string]any)
	if attrs["stage"] != "won" || attrs["close_date"] != "2026-10-20" {
		t.Fatalf("won record: %v", attrs)
	}

	// Moved back in the CRM: outcomes follow.
	owner.must(200, "PATCH", base+"/records/"+id, map[string]any{"attributes": map[string]any{"stage": "negotiation"}})
	if d := owner.must(200, "GET", base+"/deals/"+id, nil); d["state"] != "open" || d["stage"] != "Negotiation" || d["closed_at"] != nil {
		t.Fatalf("reopened in the CRM: %v", d)
	}

	// A deal made in the CRM is in outcomes under its own id, and the job
	// ties it to Sam's outreach.
	crm := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme expansion",
		"attributes": map[string]any{"stage": "qualified", "associated_people": []string{sam}}})["id"].(string)
	od := owner.must(200, "GET", base+"/deals/"+crm, nil)
	if od["source"] != "crm" || od["stage"] != "Qualified" || od["state"] != "open" || od["attribution"] != "none" {
		t.Fatalf("crm deal in outcomes: %v", od)
	}
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = 'deals.attribute' AND payload ->> 'deal_id' = $1`, crm).Scan(&queued); err != nil || queued != 1 {
		t.Fatalf("attribution job: %d %v", queued, err)
	}
	if err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error { return outcomes.AttributeDeal(ctx, tx, ws, crm) }); err != nil {
		t.Fatal(err)
	}
	if od := owner.must(200, "GET", base+"/deals/"+crm, nil); od["attribution"] != "lead" || od["lead_id"] != lead["id"] {
		t.Fatalf("attributed: %v", od)
	}
	// A deal with only a company is on the company's own timeline, and a
	// deal's timeline has the deal itself.
	co := owner.must(201, "POST", base+"/records", map[string]any{"type": "deal", "name": "Acme services",
		"attributes": map[string]any{"associated_company": acme}})["id"].(string)
	onTimeline := func(rec, query, want string) {
		t.Helper()
		tl := owner.must(200, "GET", base+"/records/"+rec+"/timeline?kinds=deal"+query, nil)["data"].([]any)
		if !slices.ContainsFunc(tl, func(it any) bool { return it.(map[string]any)["id"] == want }) {
			t.Fatalf("%s timeline has no deal %s: %v", rec, want, tl)
		}
	}
	onTimeline(acme, "&people=false", co)
	onTimeline(acme, "", id)
	onTimeline(co, "", co)
	if n := len(owner.must(200, "GET", base+"/deals", nil)["data"].([]any)); n != 3 {
		t.Fatalf("deals: %d", n)
	}

	// Archiving the record takes it out of outcomes; deleting a deal
	// archives its record.
	owner.must(200, "PATCH", base+"/records/"+crm, map[string]any{"archived": true})
	owner.must(404, "GET", base+"/deals/"+crm, nil)
	owner.must(204, "DELETE", base+"/deals/"+id, nil)
	if r := owner.must(200, "GET", base+"/records/"+id, nil); r["archived"] != true {
		t.Fatalf("deleted deal's record: %v", r)
	}

	// A HubSpot deal: the sync writes its record as crm_sync. HubSpot is
	// its source of truth: an open stage HubSpot names that the CRM doesn't
	// have keeps the record's, and won closes it.
	sync := func(stage, state string) {
		t.Helper()
		err := pgx.BeginFunc(ctx, e.pool, func(tx pgx.Tx) error {
			_, err := outcomes.UpsertSyncedDeal(ctx, tx, ws, "hubspot", "", outcomes.SyncedDeal{ExternalID: "hs-1", Name: "Acme renewal",
				Stage: stage, State: state, OpenedAt: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), ContactEmails: []string{"Sam@acme.example"}})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	stage := func(id string) any {
		return owner.must(200, "GET", base+"/records/"+id, nil)["attributes"].(map[string]any)["stage"]
	}
	sync("Qualified", "open")
	var hid string
	if err := e.pool.QueryRow(ctx, `SELECT id FROM deals WHERE workspace_id = $1 AND external_id = 'hs-1'`, ws).Scan(&hid); err != nil {
		t.Fatal(err)
	}
	r := owner.must(200, "GET", base+"/records/"+hid, nil)
	if r["origin"] != "crm_sync" || stage(hid) != "qualified" || r["attribute_sources"].(map[string]any)["stage"] != "crm_sync" ||
		fmt.Sprint(r["attributes"].(map[string]any)["associated_people"]) != fmt.Sprint([]any{sam}) {
		t.Fatalf("hubspot deal record: %v", r)
	}
	owner.must(200, "PATCH", base+"/records/"+hid, map[string]any{"attributes": map[string]any{"stage": "proposal"}})
	sync("Contract sent", "open")
	if s := stage(hid); s != "proposal" {
		t.Fatalf("stage after an unknown HubSpot stage: %v", s)
	}
	if d := owner.must(200, "GET", base+"/deals/"+hid, nil); d["stage"] != "Contract sent" || d["source"] != "hubspot" || d["attribution"] != "lead" {
		t.Fatalf("hubspot deal: %v", d)
	}
	sync("Closed won", "won")
	if s := stage(hid); s != "won" {
		t.Fatalf("stage after a won sync: %v", s)
	}
}
