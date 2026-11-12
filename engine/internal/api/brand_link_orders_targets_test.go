package api_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestLinkOrderTargetsSuggestionsAndAnchors(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	ctx := context.Background()
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Northpeak", "handle": "northpeak", "website": "https://northpeak.example"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Acme", "handle": "acme", "website": "https://acme.example"})["id"].(string)
	root := "/v1/brands/" + brand + "/link-orders"

	var calls atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "provider must not be called", http.StatusTeapot)
	}))
	t.Cleanup(fake.Close)
	if e.integrations.Client.BaseURLs == nil {
		e.integrations.Client.BaseURLs = map[string]string{}
	}
	for _, id := range []string{"dataforseo", "ahrefs", "seranking"} {
		e.integrations.Client.BaseURLs[id] = fake.URL
	}

	exec := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := e.pool.QueryRow(ctx, q+` RETURNING id::text`, args...).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	site := exec(`INSERT INTO seo_sites (workspace_id, brand_id, host, start_url, verify_token) VALUES ($1, $2, 'northpeak.example', 'https://northpeak.example/', 'tok')`, ws, brand)
	comp := exec(`INSERT INTO seo_competitors (workspace_id, site_id, brand_id, host, name, status) VALUES ($1, $2, $3, 'rival.example', 'Rival', 'tracked')`, ws, site, brand)
	// Connections are rows only: nothing here may open them.
	ser := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'seranking', 'SE Ranking', '\x00')`, ws)
	dfs := exec(`INSERT INTO integrations (workspace_id, connector, name, credentials) VALUES ($1, 'dataforseo', 'DataForSEO', '\x00')`, ws)
	if _, err := e.pool.Exec(ctx, `DELETE FROM jobs WHERE kind LIKE 'seo.%' OR kind LIKE 'rank%'`); err != nil {
		t.Fatal(err)
	}
	snap := func(integration, connector, host, key, url, metrics string) {
		t.Helper()
		if _, err := e.pool.Exec(ctx, `INSERT INTO seo_snapshots (workspace_id, integration_id, connector, host, report, day, key, url, metrics)
			VALUES ($1, $2, $3, $4, 'keywords', current_date, $5, $6, $7::jsonb)`, ws, integration, connector, host, key, url, metrics); err != nil {
			t.Fatal(err)
		}
	}
	// The site's keywords: SE Ranking's snapshot and DataForSEO's.
	snap(ser, "seranking", "northpeak.example", "free proxy", "https://northpeak.example/free-proxy-list", `{"position": 13, "volume": 40500}`)
	snap(ser, "seranking", "northpeak.example", "proxy site", "https://northpeak.example/free-proxy-list", `{"position": 28, "volume": 135000}`)
	snap(dfs, "dataforseo", "northpeak.example", "residential proxies", "https://northpeak.example/proxies/residential", `{"position": 4, "volume": 20000}`)
	// The competitor's: a gap on free proxy, none on residential proxies.
	snap(dfs, "dataforseo", "rival.example", "free proxy", "https://rival.example/free", `{"position": 2, "volume": 40500}`)
	snap(dfs, "dataforseo", "rival.example", "residential proxies", "https://rival.example/residential", `{"position": 3, "volume": 20000}`)
	// A stored link read of who links to the competitor's residential page.
	read := exec(`INSERT INTO link_reads (workspace_id, site_id, brand_id, integration_id, connector, target, scope, reason, competitor_id, status)
		VALUES ($1, $2, $3, $4, 'dataforseo', 'rival.example', 'domain', 'competitor_presence', $5, 'done')`, ws, site, brand, dfs, comp)
	for _, d := range []string{"a.example", "b.example"} {
		if _, err := e.pool.Exec(ctx, `INSERT INTO link_read_rows (read_id, workspace_id, source_url, source_domain, target_url)
			VALUES ($1, $2, $3, $4, 'https://rival.example/residential')`, read, ws, "https://"+d+"/post", d); err != nil {
			t.Fatal(err)
		}
	}
	// A keyword target on the Keywords page.
	if _, err := e.pool.Exec(ctx, `INSERT INTO page_targets (workspace_id, site_id, url, keyword, secondary)
		VALUES ($1, $2, 'https://northpeak.example/proxies/residential', 'residential proxies', '{buy residential proxies}')`, ws, site); err != nil {
		t.Fatal(err)
	}

	before := calls.Load()

	// Saved targets: add, re-save the same page, tick and untick.
	art := owner.must(201, "POST", root+"/targets", map[string]any{"url": "https://www.northpeak.example/blog/proxy-guide/"})
	if art["url"] != "https://northpeak.example/blog/proxy-guide" || art["kind"] != "article" || art["selected"] != true || art["source"] != "manual" {
		t.Fatalf("saved article: %v", art)
	}
	same := owner.must(200, "POST", root+"/targets", map[string]any{"url": "northpeak.example/blog/proxy-guide", "title": "Proxy guide"})
	if same["id"] != art["id"] || same["title"] != "Proxy guide" || same["kind"] != "article" {
		t.Fatalf("re-save: %v", same)
	}
	prod := owner.must(201, "POST", root+"/targets", map[string]any{"url": "https://northpeak.example/pricing", "kind": "product", "source": "suggested"})
	owner.must(400, "POST", root+"/targets", map[string]any{"url": "https://rival.example/pricing"})
	owner.must(400, "POST", root+"/targets", map[string]any{"url": "https://northpeak.example/x", "kind": "category"})
	owner.must(400, "POST", root+"/targets", map[string]any{"url": "ftp://northpeak.example/x"})
	off := owner.must(200, "PATCH", root+"/targets/"+prod["id"].(string), map[string]any{"selected": false})
	if off["selected"] != false || off["kind"] != "product" {
		t.Fatalf("untick: %v", off)
	}
	owner.must(400, "PATCH", root+"/targets/"+prod["id"].(string), map[string]any{"kind": "page"})
	owner.must(404, "PATCH", "/v1/brands/"+other+"/link-orders/targets/"+prod["id"].(string), map[string]any{"selected": true})
	list := owner.must(200, "GET", root+"/targets?selected=true", nil)["data"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["id"] != art["id"] {
		t.Fatalf("ticked list: %v", list)
	}
	if n := len(owner.must(200, "GET", root+"/targets", nil)["data"].([]any)); n != 2 {
		t.Fatalf("all targets: %d", n)
	}
	owner.must(400, "GET", root+"/targets?kind=page", nil)
	if n := len(owner.must(200, "GET", "/v1/brands/"+other+"/link-orders/targets", nil)["data"].([]any)); n != 0 {
		t.Fatalf("other brand sees %d targets", n)
	}

	// Suggestions from stored data only.
	sug := owner.must(200, "GET", root+"/targets/suggestions", nil)
	rows := sug["data"].([]any)
	byURL := map[string]map[string]any{}
	for _, raw := range rows {
		m := raw.(map[string]any)
		byURL[m["url"].(string)] = m
	}
	free := byURL["https://northpeak.example/free-proxy-list"]
	if free == nil || rows[0].(map[string]any)["url"] != "https://northpeak.example/free-proxy-list" {
		t.Fatalf("free-proxy-list should rank first: %v", rows)
	}
	reason := free["reasons"].([]any)[0].(map[string]any)
	if reason["kind"] != "keyword_gap" || reason["competitor"] != "rival.example" || reason["keyword"] != "free proxy" || reason["your_position"] != 13.0 {
		t.Fatalf("free reason: %v", reason)
	}
	primary := free["anchors"].(map[string]any)["primary"].([]any)
	if first := primary[0].(map[string]any); first["text"] != "proxy site" || first["source"] != "se_ranking" {
		t.Fatalf("primary anchor should be SE Ranking's: %v", primary)
	}
	res := byURL["https://northpeak.example/proxies/residential"]
	if res == nil {
		t.Fatalf("residential missing: %v", rows)
	}
	rr := res["reasons"].([]any)[0].(map[string]any)
	if rr["kind"] != "competitor_links" || rr["referring_domains"] != 2.0 || rr["competitor_url"] != "https://rival.example/residential" {
		t.Fatalf("residential reason: %v", rr)
	}
	if p := res["anchors"].(map[string]any)["primary"].([]any)[0].(map[string]any); p["source"] != "page_target" {
		t.Fatalf("residential primary should come from the page target: %v", p)
	}
	if g := byURL["https://northpeak.example/blog/proxy-guide"]; g == nil || g["target_id"] != art["id"] || g["selected"] != true || g["kind_guessed"] != false {
		t.Fatalf("ticked saved target should be listed: %v", g)
	}
	if _, ok := byURL["https://northpeak.example/pricing"]; ok {
		t.Fatal("unticked target suggested")
	}
	src := sug["sources"].(map[string]any)
	if src["se_ranking_keywords"] != 2.0 || src["competitor_link_pages"] != 1.0 || src["page_targets"] != 1.0 {
		t.Fatalf("sources: %v", src)
	}
	arts := owner.must(200, "GET", root+"/targets/suggestions?kind=article", nil)["data"].([]any)
	if len(arts) != 1 {
		t.Fatalf("articles: %v", arts)
	}
	if n := len(owner.must(200, "GET", "/v1/brands/"+other+"/link-orders/targets/suggestions", nil)["data"].([]any)); n != 0 {
		t.Fatalf("other brand got %d suggestions", n)
	}

	anchors := owner.must(200, "GET", root+"/targets/anchors?page=https://www.northpeak.example/free-proxy-list/", nil)
	if anchors["url"] != "https://northpeak.example/free-proxy-list" {
		t.Fatalf("anchors url: %v", anchors)
	}
	sec := anchors["anchors"].(map[string]any)["secondary"].(map[string]any)
	if b := sec["brand"].([]any); len(b) != 1 || b[0].(map[string]any)["text"] != "Northpeak" {
		t.Fatalf("brand anchors: %v", sec)
	}
	if k := sec["keyword"].([]any); len(k) != 1 || k[0].(map[string]any)["text"] != "free proxy" {
		t.Fatalf("keyword anchors: %v", sec)
	}
	owner.must(400, "GET", root+"/targets/anchors", nil)

	if calls.Load() != before {
		t.Fatalf("targets called a provider %d times", calls.Load()-before)
	}

	// Orders with a ticked target and two anchors.
	donor := owner.must(201, "POST", root+"/catalog", map[string]any{"domain": "donor.example"})["id"]
	order := owner.must(201, "POST", root, map[string]any{
		"donor_profile_id": donor, "target_id": art["id"], "target_choice": "suggested",
		"anchor_primary": "proxy site", "anchor_primary_source": "se_ranking",
		"anchor_secondary": "Northpeak", "anchor_secondary_type": "brand",
	})
	if order["target_url"] != "https://northpeak.example/blog/proxy-guide" || order["target_kind"] != "article" || order["target_choice"] != "suggested" ||
		order["anchor_primary_source"] != "se_ranking" || order["anchor_secondary_type"] != "brand" || order["status"] != "requested" {
		t.Fatalf("order: %v", order)
	}
	typed := owner.must(201, "POST", root, map[string]any{"donor_profile_id": donor, "target_url": "https://northpeak.example/free-proxy-list", "anchor_primary": "free proxy"})
	if typed["target_id"] != nil || typed["target_choice"] != "manual" || typed["anchor_primary_source"] != "manual" || typed["anchor_secondary_type"] != "" {
		t.Fatalf("typed order: %v", typed)
	}
	for name, body := range map[string]map[string]any{
		"unticked target":       {"donor_profile_id": donor, "target_id": prod["id"]},
		"url and target differ": {"donor_profile_id": donor, "target_id": art["id"], "target_url": "https://northpeak.example/other"},
		"no target":             {"donor_profile_id": donor},
		"secondary no type":     {"donor_profile_id": donor, "target_url": "https://northpeak.example/", "anchor_primary": "a", "anchor_secondary": "b"},
		"same anchors":          {"donor_profile_id": donor, "target_url": "https://northpeak.example/", "anchor_primary": "Free proxy", "anchor_secondary": "free proxy", "anchor_secondary_type": "keyword"},
		"url anchor not url":    {"donor_profile_id": donor, "target_url": "https://northpeak.example/", "anchor_primary": "a", "anchor_secondary": "not a url", "anchor_secondary_type": "url"},
		"bad source":            {"donor_profile_id": donor, "target_url": "https://northpeak.example/", "anchor_primary": "a", "anchor_primary_source": "paid_pull"},
		"bad kind":              {"donor_profile_id": donor, "target_url": "https://northpeak.example/", "target_kind": "category"},
		"bad choice":            {"donor_profile_id": donor, "target_url": "https://northpeak.example/", "target_choice": "auto"},
	} {
		if code, out := owner.raw("POST", root, "application/json", mustJSON(body)); code != 400 {
			t.Errorf("%s: %d %s", name, code, out)
		}
	}

	// Patch: change only the second anchor; the rest is kept.
	id := order["id"].(string)
	p := owner.must(200, "PATCH", root+"/"+id, map[string]any{"anchor_secondary": "northpeak.example/blog/proxy-guide", "anchor_secondary_type": "url"})
	if p["anchor_primary"] != "proxy site" || p["anchor_primary_source"] != "se_ranking" || p["anchor_secondary_type"] != "url" {
		t.Fatalf("patch anchors: %v", p)
	}
	p = owner.must(200, "PATCH", root+"/"+id, map[string]any{"anchor_primary": "proxy sites"})
	if p["anchor_primary_source"] != "manual" {
		t.Fatalf("an edited primary anchor is manual: %v", p)
	}
	owner.must(400, "PATCH", root+"/"+id, map[string]any{"anchor_secondary_type": "slogan"})
	p = owner.must(200, "PATCH", root+"/"+id, map[string]any{"target_url": "https://northpeak.example/free-proxy-list"})
	if p["target_id"] != nil || p["target_choice"] != "manual" || p["target_kind"] != "" {
		t.Fatalf("typed URL drops the saved target: %v", p)
	}
	p = owner.must(200, "PATCH", root+"/"+id, map[string]any{"target_id": art["id"]})
	if p["target_url"] != "https://northpeak.example/blog/proxy-guide" || p["target_kind"] != "article" {
		t.Fatalf("back to the saved target: %v", p)
	}

	// Nothing here created orders on its own, approved or paid.
	var n int
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_link_orders WHERE workspace_id = $1 AND (status <> 'requested' OR approved_at IS NOT NULL OR paid_at IS NOT NULL)`, ws).Scan(&n); err != nil || n != 0 {
		t.Fatalf("orders moved: %d %v", n, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM coverage_link_orders WHERE workspace_id = $1`, ws).Scan(&n); err != nil || n != 2 {
		t.Fatalf("orders: %d %v", n, err)
	}

	// Read and manage scopes.
	token := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Reader", "scopes": []string{"coverage.read"}})
	reader := &client{t: t, h: e.h, bearer: token["token"].(string)}
	reader.must(200, "GET", root+"/targets", nil)
	reader.must(200, "GET", root+"/targets/suggestions", nil)
	reader.must(403, "POST", root+"/targets", map[string]any{"url": "https://northpeak.example/a"})
	reader.must(403, "PATCH", root+"/targets/"+art["id"].(string), map[string]any{"selected": false})
	if calls.Load() != before {
		t.Fatalf("provider was called %d times", calls.Load()-before)
	}
}
