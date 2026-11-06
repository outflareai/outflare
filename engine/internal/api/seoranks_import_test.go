package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/outflareai/outflare/engine/internal/ranks"
	"github.com/outflareai/outflare/engine/internal/track"
)

// fakeSERanking answers SE Ranking's subscription check and one project for
// www.example.com tracking two keywords on Google USA.
type fakeSERanking struct{}

func (fakeSERanking) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Token k" {
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{"message": "Incorrect token"})
		return
	}
	var out any
	switch r.URL.Path {
	case "/account/subscription":
		out = map[string]any{"subscription_info": map[string]any{"status": "active", "units_left": 1000}}
	case "/project-management/sites":
		out = []any{map[string]any{"id": 9, "title": "Main", "name": "https://www.example.com/"}}
	case "/project-management/system/search-engines":
		out = []any{map[string]any{"id": "200", "name": "Google USA"}}
	case "/project-management/sites/search-engines":
		out = []any{map[string]any{"site_engine_id": 1, "search_engine_id": 200}}
	case "/project-management/keywords":
		out = []any{map[string]any{"id": "1", "name": "Cheap Proxies"}, map[string]any{"id": "2", "name": "proxy pricing"}}
	case "/project-management/sites/positions":
		out = []any{map[string]any{"site_engine_id": 1, "keywords": []any{
			map[string]any{"id": "1", "positions": []any{
				map[string]any{"date": "2026-11-04", "pos": 6},
				map[string]any{"date": "2026-11-05", "pos": 4},
			}, "landing_pages": []any{map[string]any{"url": "https://www.example.com/cheap", "date": "2026-10-01"}}},
			map[string]any{"id": "2", "positions": []any{map[string]any{"date": "2026-11-05", "pos": 2}}},
		}}}
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

func TestRankImport(t *testing.T) {
	got := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://www.example.com/"})["id"].(string)
	sb := base + "/seo-sites/" + site
	ctx := context.Background()

	// A keyword checked through DataForSEO (the default) keeps its own source.
	dfs := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})["id"].(string)
	owner.must(201, "POST", sb+"/rankings", map[string]any{"keywords": []string{"proxy pricing"}})
	ser := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "seranking", "credentials": map[string]string{"api_key": "k"}})["id"].(string)

	svc := &ranks.Service{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if n, err := svc.QueueImports(ctx); err != nil || n < 1 {
		t.Fatalf("queue imports: %d %v", n, err)
	}
	res, err := svc.Import(ctx, ws, site, ser)
	if err != nil || res.Project != "Main" || res.Added != 1 || res.Keywords != 1 || res.Days != 2 {
		t.Fatalf("import: %+v %v", res, err)
	}

	l := owner.must(200, "GET", sb+"/rankings", nil)
	if im := l["imports"].([]any); len(im) != 1 || im[0].(map[string]any)["integration_id"] != ser {
		t.Fatalf("imports: %v", l["imports"])
	}
	byKey := map[string]map[string]any{}
	for _, r := range l["data"].([]any) {
		r := r.(map[string]any)
		byKey[r["keyword"].(string)] = r
	}
	cp := byKey["cheap proxies"]
	if cp["integration_id"] != ser || cp["position"].(float64) != 4 || cp["previous_position"].(float64) != 6 || cp["url"] != "https://www.example.com/cheap" ||
		cp["frequency"] != "daily" || cp["last_checked_at"] == nil {
		t.Fatalf("imported: %v", cp)
	}
	if pp := byKey["proxy pricing"]; pp["integration_id"] != nil || pp["position"] != nil || dfs == "" {
		t.Fatalf("dataforseo keyword: %v", pp)
	}
	if h := owner.must(200, "GET", sb+"/rankings/"+cp["id"].(string)+"/history", nil)["data"].([]any); len(h) != 2 {
		t.Fatalf("history: %v", h)
	}

	// Imported keywords are never queued for a paid check.
	owner.must(200, "PATCH", sb+"/rankings/"+byKey["proxy pricing"]["id"].(string), map[string]any{"status": "paused"})
	e.pool.Exec(ctx, `UPDATE tracked_keywords SET last_checked_at = NULL WHERE workspace_id = $1`, ws)
	var before int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, ranks.JobCheck, ws).Scan(&before)
	if _, err := svc.QueueDue(ctx); err != nil {
		t.Fatal(err)
	}
	var after int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, ranks.JobCheck, ws).Scan(&after)
	if after != before {
		t.Fatalf("check jobs queued for imported keywords: %d -> %d", before, after)
	}

	// Check now reads the tracker again, and a second import adds nothing.
	owner.must(202, "POST", sb+"/rankings/check", nil)
	var imports int
	e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1 AND workspace_id = $2`, ranks.JobImport, ws).Scan(&imports)
	if imports < 2 {
		t.Fatalf("import jobs: %d", imports)
	}
	if res, err := svc.Import(ctx, ws, site, ser); err != nil || res.Added != 0 || res.Keywords != 1 {
		t.Fatalf("second import: %+v %v", res, err)
	}

	ok := false
	for _, ev := range got() {
		if ev.Name == "rank.imported" && ev.WorkspaceID == ws && ev.Props["keywords"] == 1 {
			ok = true
		}
	}
	if !ok {
		t.Fatal("no rank.imported event")
	}
}
