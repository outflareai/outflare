package api_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/outflareai/outflare/engine/internal/track"
)

// TestConnectionKinds: a connection is one brand's, split by account or a
// shared tool. A brand connection with no brand, and a site nobody
// assigned, are listed as unassigned and their data shows in no brand,
// until someone assigns them.
func TestConnectionKinds(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	// One brand: everything is that brand's, so nothing is unassigned.
	first := owner.must(200, "GET", base+"/brands", nil)["data"].([]any)[0].(map[string]any)["id"].(string)
	stripe := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Plausible", "brand_kind": "brand", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	if stripe["brand_kind"] != "brand" || stripe["brand_id"] != first {
		t.Fatalf("a brand connection in a one-brand workspace is that brand's: %v", stripe)
	}
	ga := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Company Plausible", "brand_kind": "split", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})["id"].(string)
	for site, host := range map[string]string{"s/acme": "acme.example", "s/north": "northpeak.example"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO analytics_syncs (integration_id, workspace_id, site, host, site_name) VALUES ($1, $2, $3, $4, $3)`, ga, ws, site, host); err != nil {
			t.Fatal(err)
		}
	}
	if u := owner.must(200, "GET", base+"/connections/unassigned", nil); len(u["data"].([]any)) != 0 {
		t.Fatalf("one brand, nothing unassigned: %v", u)
	}

	// A second brand: the split connection's sites need assigning.
	north := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "website": "northpeak.example"})["id"].(string)
	zb := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "zerobounce", "credentials": map[string]string{"api_key": "good-key"}})
	if zb["brand_kind"] != "shared" || zb["brand_id"] != nil {
		t.Fatalf("a verifier is a shared tool: %v", zb)
	}
	loose := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Loose", "brand_kind": "brand", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	if loose["brand_id"] != nil {
		t.Fatalf("two brands: a brand connection waits for its brand: %v", loose)
	}
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Bad", "brand_kind": "split", "brand_id": "northpeak", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	owner.must(400, "POST", base+"/integrations", map[string]any{"connector": "plausible", "name": "Bad", "brand_kind": "everyone", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})

	u := owner.must(200, "GET", base+"/connections/unassigned", nil)
	if u["connections"] != 1.0 || u["accounts"] != 2.0 {
		t.Fatalf("unassigned: %v", u)
	}
	whole := func() map[string]any {
		for _, x := range owner.must(200, "GET", base+"/connections/unassigned", nil)["data"].([]any) {
			if x := x.(map[string]any); x["account"] == nil {
				return x
			}
		}
		t.Fatal("no whole connection unassigned")
		return nil
	}
	if w := whole(); w["connected_by"] == nil || w["connected_at"] == "" || w["suggested_brand_id"] != nil || w["suggested_because"] != nil {
		t.Fatalf("who connected it and when, no clue to its brand yet: %v", w)
	}
	// What the provider reports says whose it is: a container named after
	// the brand, then a site on its domain.
	for details, why := range map[string]string{
		`{"email": "ana@gmail.example", "details": {"containers": "Northpeak web (GTM-N1)"}}`:                         "name",
		`{"email": "ana@gmail.example", "details": {"containers": "Web (GTM-N1)", "sites": "www.northpeak.example"}}`: "site",
		`{"email": "ana@northpeak.example", "details": {}}`:                                                           "email_domain",
	} {
		if _, err := e.pool.Exec(ctx, `UPDATE integrations SET account = $2 WHERE id = $1`, loose["id"], details); err != nil {
			t.Fatal(err)
		}
		if w := whole(); w["suggested_brand_id"] != north || w["suggested_because"] != why || w["signed_in_as"] == "" {
			t.Fatalf("%s: %v", why, w)
		}
	}
	// The same Google sign-in is the first brand's in another connection,
	// made by the same person just before: it was signed in together.
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET auth_method = 'oauth2', account = '{"id": "g-1", "email": "ana@gmail.example"}' WHERE id IN ($1, $2)`, loose["id"], stripe["id"]); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pool.Exec(ctx, `UPDATE integrations SET connector = 'google_analytics' WHERE id = $1`, stripe["id"]); err != nil {
		t.Fatal(err)
	}
	if w := whole(); w["suggested_brand_id"] != first || w["suggested_because"] != "same_sign_in" || w["connected_with"] != "Plausible" {
		t.Fatalf("same sign-in: %v", w)
	}
	accts := owner.must(200, "GET", base+"/integrations/"+ga+"/accounts", nil)["data"].([]any)
	for _, a := range accts {
		a := a.(map[string]any)
		if a["source"] != "none" || a["brand_id"] != nil {
			t.Fatalf("a domain only suggests: %v", a)
		}
		if a["account"] == "s/north" && a["suggested_brand_id"] != north {
			t.Fatalf("suggestion: %v", a)
		}
	}

	// Assigning a site moves it and its data.
	if _, err := e.pool.Exec(ctx, `INSERT INTO search_completeness (workspace_id, integration_id, site, day, clicks, impressions, visible_clicks, visible_impressions, rows_read, row_limit, capped)
		VALUES ($1, $2, 's/north', current_date, 1, 1, 1, 1, 1, 1, false)`, ws, ga); err != nil {
		t.Fatal(err)
	}
	a := owner.must(200, "PUT", base+"/integrations/"+ga+"/accounts/"+url.PathEscape("s/north"), map[string]any{"brand_id": "northpeak"})
	if a["brand_id"] != north || a["source"] != "assigned" {
		t.Fatalf("assigned: %v", a)
	}
	var moved *string
	if err := e.pool.QueryRow(ctx, `SELECT brand_id::text FROM search_completeness WHERE integration_id = $1`, ga).Scan(&moved); err != nil || moved == nil || *moved != north {
		t.Fatalf("stored data moves with the site: %v %v", moved, err)
	}
	owner.must(404, "PUT", base+"/integrations/"+ga+"/accounts/nope", map[string]any{"brand_id": "northpeak"})
	owner.must(400, "PUT", base+"/integrations/"+ga+"/accounts/"+url.PathEscape("s/acme"), map[string]any{"brand_id": "nope"})
	owner.must(200, "PATCH", base+"/integrations/"+loose["id"].(string), map[string]any{"brand_id": "northpeak"})
	u = owner.must(200, "GET", base+"/connections/unassigned", nil)
	if u["connections"] != 0.0 || u["accounts"] != 1.0 {
		t.Fatalf("after assigning: %v", u)
	}
	// Taking the assignment away leaves it with no brand again.
	if a := owner.must(200, "PUT", base+"/integrations/"+ga+"/accounts/"+url.PathEscape("s/north"), map[string]any{"brand_id": nil}); a["source"] != "none" {
		t.Fatalf("unassigned again: %v", a)
	}
	// A new kind clears what doesn't suit it.
	if p := owner.must(200, "PATCH", base+"/integrations/"+loose["id"].(string), map[string]any{"brand_kind": "shared"}); p["brand_kind"] != "shared" || p["brand_id"] != nil {
		t.Fatalf("shared: %v", p)
	}
	if n := len(named(got(), "integration.account_assigned")); n != 2 {
		t.Fatalf("account_assigned events: %d", n)
	}
}
