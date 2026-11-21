package api_test

import (
	"context"
	"testing"
)

// Query keeps the older tables to one brand as well, and asks each table
// for the read scope its API asks for.
func TestQueryOneBrandAndAPIScopes(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()
	np := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak"})["id"].(string)
	acme := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme"})["id"].(string)
	for _, b := range []struct {
		id    string
		cents int
	}{{np, 1200}, {acme, 9900}} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO spend_entries (workspace_id, brand_id, category, channel, amount_cents) VALUES ($1, $2, 'ads', 'paid_ads', $3)`, ws, b.id, b.cents); err != nil {
			t.Fatal(err)
		}
	}
	// Spend with no brand is no brand's.
	if _, err := e.pool.Exec(ctx, `INSERT INTO spend_entries (workspace_id, category, channel, amount_cents) VALUES ($1, 'software', '', 500)`, ws); err != nil {
		t.Fatal(err)
	}
	out := owner.must(200, "POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select sum(amount_cents) from spend"})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != 1200.0 {
		t.Fatalf("northpeak spend: %v", out)
	}
	out = owner.must(200, "POST", "/v1/brands/"+acme+"/query", map[string]any{"sql": "select sum(amount_cents) from spend"})
	if rows := queryRows(t, out); len(rows) != 1 || rows[0][0] != 9900.0 {
		t.Fatalf("acme spend: %v", out)
	}

	// A token reads a table only with the scope the API asks for the same
	// data: visitors, heatmaps and who an ad brought in are customer data,
	// not analytics.
	can := func(scope, table string) bool {
		t.Helper()
		tok := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": scope + " " + table, "scopes": []string{scope}, "brand": "northpeak"})
		agent := &client{t: t, h: e.h, bearer: tok["token"].(string)}
		code, _ := agent.do("POST", "/v1/brands/"+np+"/query", map[string]any{"sql": "select count(*) from " + table})
		return code == 200
	}
	for _, c := range []struct{ table, scope, not string }{
		{"site_visitors", "customers.read", "analytics.read"},
		{"heatmap_clicks", "customers.read", "analytics.read"},
		{"ad_acquisitions", "customers.read", "analytics.read"},
		{"conversation_sentiments", "customers.read", "inbox.read"},
		{"experiments", "seo.read", "analytics.read"},
		{"support_sla_policies", "inbox.read", "customers.read"},
		{"ideal_customer_profiles", "profiles.read", "customers.read"},
		{"suppressions", "leads.read", "customers.read"},
		{"journey_versions", "emails.read", "customers.read"},
		{"link_pitches", "sequences.read", "seo.read"},
		{"help_centre_days", "workspace.read", "customers.read"},
		{"memories", "workspace.read", "missions.read"},
		{"brand_findings", "workspace.read", "agents.read"},
		{"alerts", "workspace.read", "analytics.read"},
	} {
		if !can(c.scope, c.table) {
			t.Errorf("%s isn't readable with %s", c.table, c.scope)
		}
		if can(c.not, c.table) {
			t.Errorf("%s is readable with %s alone", c.table, c.not)
		}
	}
}
