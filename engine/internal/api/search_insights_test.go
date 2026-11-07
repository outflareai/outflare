package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/searchinsights"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/track"
)

func TestSearchInsightReportsAndBrandIsolation(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Search Example", "website": "https://example.com"})["id"].(string)
	other := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Other Example", "website": "https://other.example"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://example.com", "brand_id": brand})["id"].(string)
	root := "/v1/brands/" + brand + "/seo-sites/" + site
	for _, report := range []string{"opportunities", "ctr", "decay", "segments", "groups", "forecast", "completeness", "changes", "attribution", "ai-referrals", "migrations", "vitals"} {
		owner.must(200, "GET", root+"/insights?report="+report, nil)
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/seo-sites/"+site+"/insights", nil)
	owner.must(400, "GET", root+"/insights?report=unknown", nil)
	owner.must(400, "POST", root+"/search-groups", map[string]any{"name": "Broken", "kind": "topic", "rules": []any{map[string]any{"operator": "regex", "value": "["}}})
	g := owner.must(200, "POST", root+"/search-groups", map[string]any{"name": "Blog", "kind": "content", "rules": []any{map[string]any{"operator": "prefix", "value": "/blog/"}}})
	id := g["id"].(string)
	groups := owner.must(200, "GET", root+"/search-groups", nil)["data"].([]any)
	if len(groups) != 1 {
		t.Fatal(groups)
	}
	// Same name is an idempotent replacement, not a duplicate.
	g2 := owner.must(200, "POST", root+"/search-groups", map[string]any{"name": "Blog", "kind": "content", "rules": []any{map[string]any{"operator": "contains", "value": "/blog"}}})
	if g2["id"] != id {
		t.Fatal(g2)
	}
	owner.must(404, "DELETE", "/v1/brands/"+other+"/seo-sites/"+site+"/search-groups/"+id, nil)
	sc := connectSearchConsole(t, e, owner, base, &fakeSearchConsole{})
	ctx := context.Background()
	newest := time.Now().UTC().AddDate(0, 0, -3)
	// Seed settled daily evidence to exercise every stored-data calculation.
	_, err := e.pool.Exec(ctx, `INSERT INTO search_syncs(integration_id,site,workspace_id,host,oldest_day,newest_day) VALUES($1,'sc-domain:example.com',$2,'example.com',$3::date-100,$3) ON CONFLICT(integration_id,site) DO UPDATE SET oldest_day=EXCLUDED.oldest_day,newest_day=EXCLUDED.newest_day`, sc, ws, newest)
	if err != nil {
		t.Fatal(err)
	}
	// No brand, no data: the Search Console site is the brand's once
	// assigned to it; its stored rows move with it.
	owner.must(200, "PUT", base+"/integrations/"+sc+"/accounts/"+url.PathEscape("sc-domain:example.com"), map[string]any{"brand_id": brand})
	for i := 0; i < 100; i++ {
		day := newest.AddDate(0, 0, -i)
		clicks := 20
		if i < 28 {
			clicks = 5
		}
		_, err = e.pool.Exec(ctx, `INSERT INTO search_daily(workspace_id,integration_id,site,host,day,page,query,clicks,impressions,ctr,position) VALUES($1,$2,'sc-domain:example.com','example.com',$3,'https://example.com/blog/a','example query',$4,500,0.01,8) ON CONFLICT DO NOTHING`, ws, sc, day, clicks)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, report := range []string{"opportunities", "ctr", "groups", "forecast", "decay", "changes"} {
		v := owner.must(200, "GET", root+"/insights?report="+report+"&from="+newest.AddDate(0, 0, -99).Format(time.DateOnly)+"&to="+newest.Format(time.DateOnly), nil)
		if len(v["data"].([]any)) == 0 {
			t.Fatalf("%s: %v", report, v)
		}
	}

	// Work uses server-side evidence, retains it on edits, and creates one shared fix.
	insight := owner.must(200, "GET", root+"/insights?report=opportunities", nil)["data"].([]any)[0].(map[string]any)
	workBody := map[string]any{"report": "opportunities", "key": insight["key"], "tags": []string{"refresh", "refresh", "priority"}, "note": "Review title intent"}
	work := owner.must(200, "POST", root+"/insights/items", workBody)
	if len(work["tags"].([]any)) != 2 {
		t.Fatal(work)
	}
	workID := work["id"].(string)
	owner.must(409, "POST", root+"/insights/items", map[string]any{"report": "opportunities", "key": "invented"})
	linked := owner.must(200, "POST", root+"/insights/items/"+workID+"/fix", nil)
	again := owner.must(200, "POST", root+"/insights/items/"+workID+"/fix", nil)
	if linked["fix_id"] == nil || linked["fix_id"] != again["fix_id"] {
		t.Fatal(linked, again)
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/seo-sites/"+site+"/insights/workspace", nil)
	owner.must(404, "POST", "/v1/brands/"+other+"/seo-sites/"+site+"/insights/items/"+workID+"/fix", nil)
	saved := owner.must(200, "POST", root+"/insights/reports", map[string]any{"name": "Weekly opportunities", "tags": []string{"client"}, "filters": map[string]any{"report": "opportunities"}})
	reportID := saved["id"].(string)
	snapshot := owner.must(200, "POST", root+"/insights/reports/"+reportID+"/run", nil)
	if snapshot["status"] != "succeeded" || snapshot["result"] == nil {
		t.Fatal(snapshot)
	}
	schedule := owner.must(200, "POST", root+"/insights/schedules", map[string]any{"name": "Weekly updates", "frequency": "weekly", "weekday": 1, "hour": 8, "timezone": "UTC"})
	if schedule["next_run_at"] == nil {
		t.Fatal(schedule)
	}
	scheduleID := schedule["id"].(string)
	paused := owner.must(200, "POST", root+"/insights/schedules", map[string]any{"id": scheduleID, "name": "Weekly updates", "frequency": "off", "weekday": 1, "hour": 8, "timezone": "UTC"})
	if paused["next_run_at"] != nil || paused["revision"].(float64) <= schedule["revision"].(float64) {
		t.Fatal(paused)
	}
	state := owner.must(200, "GET", root+"/insights/workspace", nil)
	if len(state["items"].([]any)) != 1 || len(state["reports"].([]any)) != 1 || len(state["report_runs"].([]any)) != 1 || len(state["schedules"].([]any)) != 1 {
		t.Fatal(state)
	}

	snapshotID := snapshot["id"].(string)
	stored := owner.must(200, "GET", root+"/insights/report-runs/"+snapshotID, nil)
	if stored["result"] == nil {
		t.Fatal(stored)
	}
	if state["report_runs"].([]any)[0].(map[string]any)["result"] != nil {
		t.Fatal("workspace must not load full snapshots")
	}
	owner.must(404, "GET", "/v1/brands/"+other+"/seo-sites/"+site+"/insights/report-runs/"+snapshotID, nil)
	// Paused revisions must skip both the scheduler tick and queued provider work.
	scheduler := &searchinsights.Service{Pool: e.pool}
	oldRevision := int64(schedule["revision"].(float64))
	tickAt, _ := time.Parse(time.RFC3339Nano, schedule["next_run_at"].(string))
	tick, _ := json.Marshal(searchinsights.ScheduleRequest{WorkspaceID: ws, BrandID: brand, SiteID: site, ScheduleID: scheduleID, Revision: oldRevision, At: tickAt})
	if err := scheduler.Schedule(ctx, jobs.Job{Payload: tick}); err != nil {
		t.Fatal(err)
	}
	stale, _ := json.Marshal(searchinsights.Request{WorkspaceID: ws, BrandID: brand, SiteID: site, ScheduleID: scheduleID, Revision: oldRevision})
	if err := scheduler.Refresh(ctx, jobs.Job{Payload: stale}); err != nil {
		t.Fatal(err)
	}

	// The scheduler commits the next tick before a provider job can fail.
	resumed := owner.must(200, "POST", root+"/insights/schedules", map[string]any{"id": scheduleID, "name": "Daily updates", "frequency": "daily", "weekday": 1, "hour": 8, "timezone": "UTC"})
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	if _, err := e.pool.Exec(ctx, `UPDATE search_insight_schedules SET next_run_at=$2 WHERE id=$1`, scheduleID, past); err != nil {
		t.Fatal(err)
	}
	activeTick, _ := json.Marshal(searchinsights.ScheduleRequest{WorkspaceID: ws, BrandID: brand, SiteID: site, ScheduleID: scheduleID, Revision: int64(resumed["revision"].(float64)), At: past})
	if err := scheduler.Schedule(ctx, jobs.Job{Payload: activeTick}); err != nil {
		t.Fatal(err)
	}
	var next time.Time
	var queued int
	if err := e.pool.QueryRow(ctx, `SELECT next_run_at FROM search_insight_schedules WHERE id=$1`, scheduleID).Scan(&next); err != nil || !next.After(time.Now()) {
		t.Fatal(next, err)
	}
	if err := e.pool.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind='search_insights.refresh' AND payload->>'schedule_id'=$1`, scheduleID).Scan(&queued); err != nil || queued != 1 {
		t.Fatal(queued, err)
	}
	digest := owner.must(200, "GET", "/v1/brands/"+brand+"/digests/preview", nil)
	hasSearch := false
	for _, section := range digest["sections"].([]any) {
		if section.(map[string]any)["id"] == "search_insights" {
			hasSearch = true
		}
	}
	if !hasSearch {
		t.Fatal("missing search work from digest", digest)
	}
	transient := owner.must(200, "POST", root+"/search-groups", map[string]any{"name": "Temporary", "kind": "content", "rules": []any{map[string]any{"operator": "prefix", "value": "/blog/"}}})["id"].(string)
	broken := owner.must(200, "POST", root+"/insights/reports", map[string]any{"name": "Stale group", "filters": map[string]any{"report": "opportunities", "group_id": transient}})["id"].(string)
	owner.must(204, "DELETE", root+"/search-groups/"+transient, nil)
	failed := owner.must(200, "POST", root+"/insights/reports/"+broken+"/run", nil)
	if failed["status"] != "failed" || failed["error"] == "" || failed["result"] != nil {
		t.Fatal(failed)
	}
	owner.must(204, "DELETE", root+"/insights/reports/"+broken, nil)
	owner.must(400, "POST", root+"/insights/schedules", map[string]any{"name": "Bad", "frequency": "daily", "timezone": "Wrong/Zone"})
	owner.must(204, "DELETE", root+"/insights/schedules/"+scheduleID, nil)
	owner.must(204, "DELETE", root+"/insights/reports/"+reportID, nil)

	// Separate dimensions never inflate page/query totals; another brand's
	// rows on the same connection must not appear in this site's reports.
	for _, fixture := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO search_segments(workspace_id,brand_id,integration_id,site,host,day,page,country,device,clicks,impressions,position) VALUES($1,$2,$3,'sc-domain:example.com','example.com',$4,'https://example.com/blog/a','usa','MOBILE',7,100,4)`, []any{ws, brand, sc, newest}},
		{`INSERT INTO search_completeness(workspace_id,brand_id,integration_id,site,day,clicks,impressions,visible_clicks,visible_impressions,rows_read,row_limit,capped) VALUES($1,$2,$3,'sc-domain:example.com',$4,10,100,7,90,50000,50000,true)`, []any{ws, brand, sc, newest}},
		{`INSERT INTO search_ga4_outcomes(workspace_id,brand_id,integration_id,site,day,host,path,source,sessions,conversions,revenue_micros,currency) VALUES($1,$2,$3,'123',$4,'example.com','/blog/a','google / organic',20,4,10000000,'EUR'),($1,$2,$3,'123',$4,'example.com','/blog/a','chatgpt.com / referral',5,1,2000000,'EUR'),($1,$2,$3,'123',$4,'example.com','/blog/a','fakechatgpt.com / referral',3,0,0,'EUR')`, []any{ws, brand, sc, newest}},
		{`INSERT INTO search_vitals(workspace_id,brand_id,site_id,page,device,day,metric,p75,good_share) VALUES($1,$2,$3,'https://example.com','PHONE',$4,'largest_contentful_paint',4000,.5),($1,$2,$3,'https://example.com','PHONE',$4::date-7,'largest_contentful_paint',2800,.7)`, []any{ws, brand, site, newest}},
		{`INSERT INTO search_algorithm_updates(workspace_id,brand_id,site_id,id,name,started_at) VALUES($1,$2,$3,'test-update','Core update',$4)`, []any{ws, brand, site, newest}},
	} {
		if _, err = e.pool.Exec(ctx, fixture.sql, fixture.args...); err != nil {
			t.Fatal(err)
		}
	}
	seg := owner.must(200, "GET", root+"/insights?report=segments&country=usa&device=MOBILE", nil)["data"].([]any)
	if len(seg) != 1 || seg[0].(map[string]any)["clicks"] != 7.0 {
		t.Fatal(seg)
	}
	completeness := owner.must(200, "GET", root+"/insights?report=completeness", nil)["data"].([]any)
	if len(completeness) != 1 || completeness[0].(map[string]any)["after"] != 3.0 || completeness[0].(map[string]any)["status"] != "capped" {
		t.Fatal(completeness)
	}
	owner.must(201, "POST", base+"/brands", map[string]any{"name": "Docs example", "website": "https://docs.example.com"})
	scoped := owner.must(200, "GET", root+"/insights?report=completeness", nil)
	if scoped["status"] != "property_spans_brands" {
		t.Fatal(scoped)
	}
	ai := owner.must(200, "GET", root+"/insights?report=ai-referrals", nil)
	if ai["summary"].(map[string]any)["ai_sessions"] != 5.0 || len(ai["data"].([]any)) != 1 {
		t.Fatal(ai)
	}
	attr := owner.must(200, "GET", root+"/insights?report=attribution", nil)["data"].([]any)
	if len(attr) != 2 {
		t.Fatal(attr)
	}
	for _, x := range attr {
		v := x.(map[string]any)
		if v["status"] == "estimated_query_allocation" && (v["conversions"] != 4.0 || v["revenue_micros"] != 10000000.0) {
			t.Fatal(v)
		}
	}
	vitals := owner.must(200, "GET", root+"/insights?report=vitals", nil)["data"].([]any)
	if len(vitals) != 2 || vitals[0].(map[string]any)["status"] != "regressed" {
		t.Fatal(vitals)
	}
	// A read-only brand token can read its report but cannot save a group.
	token := owner.must(201, "POST", base+"/api-tokens", map[string]any{"name": "Search reader", "scopes": []string{"seo.read"}, "brand": brand})["token"].(string)
	reader := &client{t: t, h: e.h, bearer: token}
	reader.must(200, "GET", root+"/insights", nil)
	reader.must(200, "GET", root+"/insights/workspace", nil)
	reader.must(403, "POST", root+"/insights/items", map[string]any{"report": "opportunities", "key": "forbidden"})
	reader.must(403, "POST", root+"/insights/schedules", map[string]any{"name": "forbidden", "frequency": "off", "weekday": 1, "hour": 8, "timezone": "UTC"})
	reader.must(403, "POST", root+"/search-groups", map[string]any{"name": "No permission", "kind": "content", "rules": []any{map[string]any{"operator": "prefix", "value": "/"}}})
	// Public update collection does not require an API key; provider calls
	// are queued and are never made by the request handler.
	owner.must(200, "POST", root+"/insights/refresh", map[string]any{})
	owner.must(400, "POST", root+"/insights/refresh", map[string]any{"device": "BAD"})
	owner.must(400, "GET", root+"/insights?report=forecast&growth=NaN", nil)
	owner.must(400, "GET", root+"/insights?report=decay&bucket=day", nil)
	owner.must(400, "GET", root+"/insights?report=segments&country=us", nil)
	owner.must(400, "GET", root+"/insights?report=segments&device=bad", nil)
	// A saved migration only references sites inside this brand.
	otherSite := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://other.example", "brand_id": other})["id"].(string)
	owner.must(404, "POST", root+"/search-migrations", map[string]any{"name": "Move", "target_site_id": otherSite, "go_live": "2026-10-01", "mappings": []any{}})
	migration := owner.must(200, "POST", root+"/search-migrations", map[string]any{"name": "Restructure", "target_site_id": site, "go_live": newest.AddDate(0, 0, -14).Format(time.DateOnly), "mappings": []any{map[string]any{"source": "https://example.com/old", "target": "https://example.com/blog/a"}}})
	owner.must(200, "GET", root+"/search-migrations", nil)
	m := owner.must(200, "GET", root+"/insights?report=migrations", nil)
	if m["data"].([]any)[0].(map[string]any)["status"] != "unknown" {
		t.Fatal(m)
	}
	owner.must(204, "DELETE", root+"/search-migrations/"+migration["id"].(string), nil)
	owner.must(204, "DELETE", root+"/search-groups/"+id, nil)
	owner.must(404, "GET", root+"/insights?group_id="+id, nil)
	seen := map[string]bool{}
	for _, ev := range events() {
		seen[ev.Name] = true
	}
	for _, name := range []string{"search_group.saved", "search_group.deleted", "search_migration.saved", "search_migration.deleted"} {
		if !seen[name] {
			t.Error(fmt.Sprintf("missing event %s", name))
		}
	}
}

func TestSearchInsightCollectionAndFixes(t *testing.T) {
	events := track.Capture(t)
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws
	brand := owner.must(201, "POST", base+"/brands", map[string]any{"name": "Vitals Example", "website": "https://example.com"})["id"].(string)
	site := owner.must(201, "POST", base+"/seo-sites", map[string]any{"url": "https://example.com", "brand_id": brand})["id"].(string)
	root := "/v1/brands/" + brand + "/seo-sites/" + site
	latest := time.Now().UTC().AddDate(0, 0, -3)
	prior := latest.AddDate(0, 0, -7)
	date := func(d time.Time) map[string]int {
		return map[string]int{"year": d.Year(), "month": int(d.Month()), "day": d.Day()}
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/records:queryRecord":
			json.NewEncoder(w).Encode(map[string]any{"record": map[string]any{}})
		case "/v1/records:queryHistoryRecord":
			if r.URL.Query().Get("key") != "test_own_crux_key" {
				t.Error("provider did not receive workspace key")
			}
			json.NewEncoder(w).Encode(map[string]any{"record": map[string]any{"collectionPeriods": []any{map[string]any{"lastDate": date(prior)}, map[string]any{"lastDate": date(latest)}}, "metrics": map[string]any{"largest_contentful_paint": map[string]any{"percentilesTimeseries": map[string]any{"p75s": []int{2800, 4000}}, "histogramTimeseries": []any{map[string]any{"densities": []float64{.7, .5}}}}}}})
		case "/incidents.json":
			json.NewEncoder(w).Encode([]any{map[string]any{"id": "rank1", "begin": latest.Format(time.RFC3339), "external_desc": "Core update", "affected_products": []any{map[string]any{"title": "Ranking"}}}, map[string]any{"id": "index1", "begin": latest.Format(time.RFC3339), "external_desc": "Indexing incident", "affected_products": []any{map[string]any{"title": "Indexing"}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	for _, key := range []string{"google_crux", "google_crux.current", "google_search_updates"} {
		e.integrations.Client.BaseURLs[key] = provider.URL
	}
	conn := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "google_crux", "name": "Own field data", "brand_id": brand, "credentials": map[string]string{"api_key": "test_own_crux_key"}})["id"].(string)
	owner.must(200, "POST", root+"/insights/refresh", map[string]any{"integration_id": conn, "device": "PHONE"})
	payload, _ := json.Marshal(searchinsights.Request{WorkspaceID: ws, BrandID: brand, SiteID: site, IntegrationID: conn, Page: "https://example.com", Device: "PHONE"})
	svc := &searchinsights.Service{Pool: e.pool, Store: e.integrations, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	if err := svc.Refresh(context.Background(), jobs.Job{Payload: payload}); err != nil {
		t.Fatal(err)
	}
	data := owner.must(200, "GET", root+"/insights?report=vitals", nil)["data"].([]any)
	if len(data) != 2 || data[0].(map[string]any)["status"] != "regressed" {
		t.Fatal(data)
	}
	fixes := owner.must(200, "GET", base+"/fixes?brand="+brand+"&source=seo_keywords&subject_id="+site, nil)["data"].([]any)
	if len(fixes) != 1 || fixes[0].(map[string]any)["rule"].(map[string]any)["id"] != "web_vitals_regressed" {
		t.Fatal(fixes)
	}
	owner.must(200, "GET", base+"/fixes/"+fixes[0].(map[string]any)["id"].(string)+"/prompt", nil)
	var updates, scheduled int
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM search_algorithm_updates WHERE workspace_id=$1 AND brand_id=$2 AND site_id=$3`, ws, brand, site).Scan(&updates); err != nil {
		t.Fatal(err)
	}
	if err := e.pool.QueryRow(context.Background(), `SELECT count(*) FROM jobs WHERE workspace_id=$1 AND kind='search_insights.refresh' AND run_at>now()`, ws).Scan(&scheduled); err != nil {
		t.Fatal(err)
	}
	if updates != 1 || scheduled != 0 {
		t.Fatalf("updates=%d scheduled=%d", updates, scheduled)
	}
	seen := false
	for _, ev := range events() {
		if ev.Name == "search_vitals.synced" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("missing collection event")
	}
}
