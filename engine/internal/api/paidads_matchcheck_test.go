package api_test

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// The match check reads stored visits and says how many more people an ad
// would be credited with if visits from the same network and device were
// one person, and how often that guess is right where the truth is known.
func TestPaidAdsMatchCheck(t *testing.T) {
	e := freshEnv(t)
	ctx := context.Background()
	owner, ws := setupOwner(t, e.h)
	var brand string
	if err := e.pool.QueryRow(ctx, `SELECT outflare_first_brand($1)::text`, ws).Scan(&brand); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	person := func(name string) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, `INSERT INTO records (brand_id, workspace_id, record_type, name) VALUES ($1, $2, 'person', $3) RETURNING id::text`, brand, ws, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// visit stores one visit: who (nil is unknown), the browser, the
	// network and city that make its signature, and whether an ad sent it.
	visit := func(record any, anon string, asn int, city string, paid bool, ago time.Duration) {
		t.Helper()
		clicks := "{}"
		if paid {
			clicks = `{"gclid": "Cj0test"}`
		}
		if _, err := e.pool.Exec(ctx, `INSERT INTO site_sessions (brand_id, workspace_id, record_id, anonymous_id, started_at, last_seen_at, click_ids, asn, city, country, browser, os, device)
			VALUES ($1, $2, $3, $4, $5, $5, $6, $7, $8, 'PT', 'Chrome', 'macOS', 'desktop')`, brand, ws, record, anon, now.Add(-ago), clicks, asn, city); err != nil {
			t.Fatal(err)
		}
	}
	day := 24 * time.Hour

	// Ana signed up in one browser; another browser on her network clicked
	// an ad two days before. One candidate: she would be credited.
	ana := person("Ana")
	visit(ana, "ana-laptop", 64500, "Porto", false, 2*day)
	visit(nil, "ana-phone", 64500, "Porto", true, 4*day)
	// Ben's signature has two unknown browsers that clicked ads: ambiguous.
	ben := person("Ben")
	visit(ben, "ben-laptop", 64501, "Faro", false, 2*day)
	visit(nil, "faro-1", 64501, "Faro", true, 3*day)
	visit(nil, "faro-2", 64501, "Faro", true, 5*day)
	// Cara has no ad click near her: no match. The click after her visit
	// and the one on another network don't count.
	cara := person("Cara")
	visit(cara, "cara-laptop", 64502, "Braga", false, 6*day)
	visit(nil, "braga-late", 64502, "Braga", true, day/2)
	visit(nil, "elsewhere", 64999, "Braga", true, 8*day)
	// Dan is already credited to an ad.
	dan := person("Dan")
	visit(dan, "dan-laptop", 64503, "Evora", true, 3*day)
	if _, err := e.pool.Exec(ctx, `INSERT INTO ad_acquisitions (workspace_id, brand_id, record_id, connector, clicked_at, window_days) VALUES ($1, $2, $3, 'google_ads', $4, 30)`, ws, brand, dan, now.Add(-3*day)); err != nil {
		t.Fatal(err)
	}
	// What is known: Eve has two browsers with one signature (right), and
	// Fay shares a signature with Gil (wrong).
	eve := person("Eve")
	visit(eve, "eve-1", 64504, "Lagos", false, 3*day)
	visit(eve, "eve-2", 64504, "Lagos", false, 2*day)
	fay, gil := person("Fay"), person("Gil")
	visit(fay, "fay-1", 64505, "Tavira", false, 3*day)
	visit(gil, "gil-1", 64505, "Tavira", false, 2*day)
	// A visit with no network has no signature.
	visit(nil, "no-network", 0, "", false, day/2)

	got := owner.must(200, "GET", "/v1/brands/"+brand+"/ads/match-check", nil)
	want := map[string]float64{
		"days": 30, "window_days": 30, "visits": 14, "visits_usable": 13, "browsers": 13, "browsers_unique": 2,
		"people": 7, "people_credited": 1, "would_credit": 1, "ambiguous": 1, "no_match": 4,
		"known_pairs": 2, "known_pairs_right": 1,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v (%v)", k, got[k], v, got)
		}
	}
	if got["accuracy"] != 0.5 || got["verdict"] != "not_enough_data" {
		t.Fatalf("accuracy %v, verdict %v", got["accuracy"], got["verdict"])
	}

	// A shorter period leaves out what is older, and bad input is refused.
	short := owner.must(200, "GET", "/v1/brands/"+brand+"/ads/match-check?days=1", nil)
	if short["days"] != 1.0 || short["people"] != 0.0 || short["visits"] != 2.0 || short["browsers"] != 1.0 {
		t.Fatalf("one day: %v", short)
	}
	for _, bad := range []string{"0", "91", "x"} {
		owner.must(400, "GET", fmt.Sprintf("/v1/brands/%s/ads/match-check?days=%s", brand, bad), nil)
	}
}
