package seo

import (
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

func TestSERankingTrackedRanks(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"GET /project-management/sites": []any{
			map[string]any{"id": 7, "title": "Other", "name": "https://other.example/"},
			map[string]any{"id": 9, "title": "Main site", "name": "https://www.example.com", "is_active": 1},
		},
		"GET /project-management/system/search-engines": []any{
			map[string]any{"id": "200", "name": "Google USA", "regionid": "182"},
			map[string]any{"id": "201", "name": "Google Mobile United Kingdom", "regionid": "1"},
			map[string]any{"id": "202", "name": "Google Atlantis", "regionid": "2"},
		},
		"GET /project-management/sites/search-engines": []any{
			map[string]any{"site_engine_id": 1, "search_engine_id": 200},
			map[string]any{"site_engine_id": 2, "search_engine_id": "201"},
			map[string]any{"site_engine_id": 3, "search_engine_id": 202},
			map[string]any{"site_engine_id": 4, "search_engine_id": 200},
		},
		"GET /project-management/keywords": []any{
			map[string]any{"id": "11", "name": "Cheap Proxies"},
			map[string]any{"id": 12, "name": "proxy pricing"},
		},
		"GET /project-management/sites/positions": []any{
			map[string]any{"site_engine_id": 1, "keywords": []any{
				map[string]any{"id": "11", "positions": []any{
					map[string]any{"date": "2026-11-03", "pos": 4},
					map[string]any{"date": "2026-11-04", "pos": 0},
					map[string]any{"date": "2026-11-05", "pos": 2, "is_map": 1},
				}, "landing_pages": []any{map[string]any{"url": "https://www.example.com/cheap", "date": "2026-10-01"}}},
				map[string]any{"id": 12, "positions": []any{map[string]any{"date": "2026-11-05", "pos": 7}},
					"landing_pages": []any{
						map[string]any{"url": "https://www.example.com/pricing", "date": "2026-11-05"},
						map[string]any{"url": "https://www.example.com/", "date": "2026-10-01"},
					}},
			}},
			map[string]any{"site_engine_id": 2, "keywords": []any{
				map[string]any{"id": "11", "positions": []any{map[string]any{"date": "2026-11-05", "pos": 9}}},
			}},
			map[string]any{"site_engine_id": 3, "keywords": []any{
				map[string]any{"id": "11", "positions": []any{map[string]any{"date": "2026-11-05", "pos": 1}}},
			}},
		},
	})
	s := SERanking{}
	if !s.Definition().Has(c.CapTrackedRanks) {
		t.Fatal("capability")
	}
	got, err := s.TrackedRanks(ctx, cl, c.Creds{"api_key": "k"}, c.TrackedRankQuery{Hosts: []string{"www.example.com", "example.com"}, From: "2026-10-29", To: "2026-11-05"})
	if err != nil || got.Project != "Main site" || len(got.Ranks) != 3 || len(got.Skipped) != 2 {
		t.Fatalf("ranks: %+v %v", got, err)
	}
	gb, us, pricing := got.Ranks[0], got.Ranks[1], got.Ranks[2]
	if gb.Keyword != "Cheap Proxies" || gb.Country != "GB" || gb.Device != "mobile" || gb.Engine != "google" || *gb.Days[0].Position != 9 {
		t.Fatalf("gb: %+v", gb)
	}
	if us.Country != "US" || us.Device != "desktop" || len(us.Days) != 3 || *us.Days[0].Position != 4 || us.Days[0].URL != "https://www.example.com/cheap" ||
		us.Days[1].Position != nil || us.Days[2].Position != nil {
		t.Fatalf("us: %+v", us)
	}
	if pricing.Keyword != "proxy pricing" || pricing.Days[0].URL != "https://www.example.com/pricing" {
		t.Fatalf("pricing: %+v", pricing)
	}
	last := (*calls)[len(*calls)-1]
	if !strings.Contains(last, "site_id=9") || !strings.Contains(last, "date_from=2026-10-29") || !strings.Contains(last, "with_landing_pages=1") {
		t.Fatalf("positions call: %s", last)
	}

	// No project for the site is no ranks, not an error.
	got, err = s.TrackedRanks(ctx, cl, c.Creds{"api_key": "k"}, c.TrackedRankQuery{Hosts: []string{"nowhere.example"}})
	if err != nil || got.Project != "" || len(got.Ranks) != 0 {
		t.Fatalf("no project: %+v %v", got, err)
	}
}
