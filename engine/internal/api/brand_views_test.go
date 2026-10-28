package api_test

import (
	"context"
	"testing"
)

// TestBrandViewsShowOnlyTheBrand: with one brand picked, infrastructure,
// mailbox health, Home's status, leads and allocation count only that
// brand's records; brand=none counts the ones no brand has.
func TestBrandViewsShowOnlyTheBrand(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	geo := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Geonode", "website": "geonode.com"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Repocket", "website": "repocket.com"})
	ctx := context.Background()
	mine := addMailbox(t, e, ws, "ana@geonode.example")
	addMailbox(t, e, ws, "bo@unassigned.example")
	addMailbox(t, e, ws, "cy@unassigned.example")
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET lifecycle_stage = 'ready', brand_id = $2 WHERE id = $1`, mine, geo); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE mailboxes SET lifecycle_stage = 'ready' WHERE workspace_id = $1`, ws); err != nil {
		t.Fatal(err)
	}
	ready := func(q string) float64 {
		t.Helper()
		return owner.must(200, "GET", base+"/infrastructure"+q, nil)["mailboxes"].(map[string]any)["ready"].(float64)
	}
	if ready("") != 3 || ready("?brand=geonode") != 1 || ready("?brand=repocket") != 0 || ready("?brand=none") != 2 {
		t.Fatalf("ready: %v %v %v %v", ready(""), ready("?brand=geonode"), ready("?brand=repocket"), ready("?brand=none"))
	}
	if n := len(owner.must(200, "GET", base+"/mailbox-health?brand=repocket", nil)["data"].([]any)); n != 0 {
		t.Fatalf("repocket health: %d", n)
	}
	if n := len(owner.must(200, "GET", base+"/mailbox-health?brand=geonode", nil)["data"].([]any)); n != 1 {
		t.Fatalf("geonode health: %d", n)
	}
	// Home: Repocket has no mailboxes, whatever the others have.
	codes := func(q string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		for _, b := range owner.must(200, "GET", base+"/status"+q, nil)["blockers"].([]any) {
			out[b.(map[string]any)["code"].(string)] = true
		}
		return out
	}
	if !codes("?brand=repocket")["no_mailboxes"] || codes("?brand=geonode")["no_mailboxes"] || codes("")["no_mailboxes"] {
		t.Fatal("no_mailboxes should show for Repocket only")
	}
	owner.must(404, "GET", base+"/status?brand=nope", nil)
	// Mailboxes in no brand show on Home under All brands, and go once
	// they are moved into one.
	if !codes("")["records_unassigned"] || codes("?brand=geonode")["records_unassigned"] {
		t.Fatal("records_unassigned should show under All brands only")
	}
	var loose []string
	for _, m := range owner.must(200, "GET", base+"/mailboxes?brand=none", nil)["data"].([]any) {
		loose = append(loose, m.(map[string]any)["id"].(string))
	}
	if len(loose) != 2 {
		t.Fatalf("unassigned mailboxes: %v", loose)
	}
	owner.must(200, "POST", base+"/brands/repocket/add", map[string]any{"mailbox_ids": loose})
	if codes("")["records_unassigned"] || ready("?brand=repocket") != 2 {
		t.Fatal("moved mailboxes still unassigned")
	}

	// Leads count for a brand when they are on its lists.
	var list, lead string
	if err := e.pool.QueryRow(ctx, `INSERT INTO lead_lists (workspace_id, name, brand_id) VALUES ($1, 'Geonode list', $2) RETURNING id::text`, ws, geo).Scan(&list); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(ctx, `INSERT INTO leads (workspace_id, email) VALUES ($1, 'dee@acme.example') RETURNING id::text`, ws).Scan(&lead); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO leads (workspace_id, email) VALUES ($1, 'eve@acme.example')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `INSERT INTO lead_list_members (list_id, lead_id) VALUES ($1, $2)`, list, lead); err != nil {
		t.Fatal(err)
	}
	leads := func(q string) float64 {
		t.Helper()
		return owner.must(200, "GET", base+"/leads"+q, nil)["total"].(float64)
	}
	if leads("") != 2 || leads("?brand=geonode") != 1 || leads("?brand=repocket") != 0 || leads("?brand=none") != 1 {
		t.Fatalf("leads: %v %v %v %v", leads(""), leads("?brand=geonode"), leads("?brand=repocket"), leads("?brand=none"))
	}
	owner.must(200, "GET", base+"/allocation/attention?brand=geonode", nil)
	owner.must(200, "GET", base+"/infra-plans?brand=geonode", nil)
	owner.must(200, "GET", base+"/meetings?brand=geonode", nil)
	owner.must(200, "GET", base+"/deals?brand=none", nil)
	owner.must(200, "GET", base+"/allocation/suggestions?brand=geonode", nil)
	owner.must(200, "GET", base+"/allocation/changes?brand=geonode", nil)
}
