package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/outflareai/outflare/engine/internal/aivis"
)

// Cohorts count the competitors tracked in SEO for their brand, after
// their own, with one company's hosts grouped under its name.
func TestAICohortTrackedCompetitors(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	ctx := context.Background()

	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "website": "acme.example"})
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.acme.example/", "brand_id": "acme"})["id"].(string)
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Beta", "website": "beta.example"})
	other := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://beta.example/", "brand_id": "beta"})["id"].(string)
	add := func(siteID, host, name string, aliases ...string) string {
		c := owner.must(201, "POST", base+"/seo-competitors", map[string]any{"site_id": siteID, "host": host})
		if name != "" || len(aliases) > 0 {
			owner.must(200, "PATCH", base+"/seo-competitors/"+c["id"].(string), map[string]any{"name": name, "aliases": append([]string{}, aliases...)})
		}
		return c["id"].(string)
	}
	add(site, "northpeak.example", "Northpeak", "NP")
	add(site, "northpeak.test", "Northpeak")
	add(site, "rival.example", "")
	add(site, "own.example", "Own Rival")
	dismissed := add(site, "gone.example", "Gone")
	owner.must(200, "PATCH", base+"/seo-competitors/"+dismissed, map[string]any{"status": "dismissed"})
	add(other, "elsewhere.example", "Elsewhere") // Beta's site (its domain), not Acme's

	cohort := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Buyers", "brand_id": "acme",
		"competitors": []map[string]any{{"name": "Own Rival", "domains": []string{"own-rival.example"}}}})
	tracked := cohort["tracked_competitors"].([]any)
	if len(tracked) != 2 {
		t.Fatalf("tracked: %v", tracked)
	}
	np := tracked[0].(map[string]any)
	if np["name"] != "Northpeak" || len(np["domains"].([]any)) != 2 || np["aliases"].([]any)[0] != "NP" {
		t.Fatalf("grouped: %v", np)
	}
	// The whole list counts, however long: no cap at 25.
	for i := range 30 {
		add(site, fmt.Sprintf("rival%02d.example", i), "")
	}
	cid := cohort["id"].(string)
	if tc := owner.must(200, "GET", base+"/ai-cohorts/"+cid, nil)["tracked_competitors"].([]any); len(tc) != 32 {
		t.Fatalf("tracked after 30 more: %d", len(tc))
	}
	if r := tracked[1].(map[string]any); r["name"] != "rival.example" || r["domains"].([]any)[0] != "rival.example" {
		t.Fatalf("unnamed: %v", r)
	}

	// A run counts both lists in full, the cohort's own first.
	c, err := aivis.LoadCohort(ctx, e.pool, ws, cohort["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Competitors) != 33 || c.Competitors[0].Name != "Own Rival" || c.Competitors[1].Name != "Northpeak" {
		t.Fatalf("run competitors: %+v", c.Competitors)
	}
	// Another brand's cohort counts only that brand's tracked competitors.
	company := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Beta buyers", "brand_id": "beta"})
	if tc := company["tracked_competitors"].([]any); len(tc) != 1 || tc[0].(map[string]any)["name"] != "Elsewhere" {
		t.Fatalf("company: %v", tc)
	}
}
