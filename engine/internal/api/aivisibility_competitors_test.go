package api_test

import (
	"context"
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
	other := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://company.example/"})["id"].(string)
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
	add(other, "elsewhere.example", "Elsewhere") // the company's site, not Acme's

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
	if r := tracked[1].(map[string]any); r["name"] != "rival.example" || r["domains"].([]any)[0] != "rival.example" {
		t.Fatalf("unnamed: %v", r)
	}

	// A run counts both lists, the cohort's own first.
	c, err := aivis.LoadCohort(ctx, e.pool, ws, cohort["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Competitors) != 3 || c.Competitors[0].Name != "Own Rival" || c.Competitors[1].Name != "Northpeak" {
		t.Fatalf("run competitors: %+v", c.Competitors)
	}
	// A company cohort counts the company's tracked competitors.
	company := owner.must(201, "POST", base+"/ai-cohorts", map[string]any{"name": "Company"})
	if tc := company["tracked_competitors"].([]any); len(tc) != 1 || tc[0].(map[string]any)["name"] != "Elsewhere" {
		t.Fatalf("company: %v", tc)
	}
}
