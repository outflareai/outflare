package api_test

import (
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
)

var dataForSEOCalls atomic.Int64

// dfsLinks is what the fake DataForSEO's links report answers.
var dfsLinks struct {
	sync.Mutex
	items []any
}

// dfsRank is the fake's backlinks rank for every domain; dfsRivals are
// its competitors.
var (
	dfsRank   atomic.Int64
	dfsRivals struct {
		sync.Mutex
		items []any
	}
)

// dfsTargets holds per-domain answers for the keywords and referring
// domains reports, by target; others get the default keywords.
var dfsTargets struct {
	sync.Mutex
	keywords, domains map[string][]any
}

func setDFSTarget(target string, keywords, domains []any) {
	dfsTargets.Lock()
	defer dfsTargets.Unlock()
	if dfsTargets.keywords == nil {
		dfsTargets.keywords, dfsTargets.domains = map[string][]any{}, map[string][]any{}
	}
	if keywords == nil && domains == nil {
		delete(dfsTargets.keywords, target)
		delete(dfsTargets.domains, target)
		return
	}
	dfsTargets.keywords[target], dfsTargets.domains[target] = keywords, domains
}

// dfsTarget reads the target of a DataForSEO task.
func dfsTarget(r *http.Request) string {
	var tasks []map[string]any
	_ = json.NewDecoder(r.Body).Decode(&tasks)
	if len(tasks) == 0 {
		return ""
	}
	t, _ := tasks[0]["target"].(string)
	return t
}

func setDFSRivals(items ...any) {
	dfsRivals.Lock()
	dfsRivals.items = items
	dfsRivals.Unlock()
}

func setDFSLinks(items ...any) {
	dfsLinks.Lock()
	dfsLinks.items = items
	dfsLinks.Unlock()
}

// fakeDataForSEO answers DataForSEO for the login "jo" and password "pw".
type fakeDataForSEO struct{}

func (fakeDataForSEO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	dataForSEOCalls.Add(1)
	if u, p, _ := r.BasicAuth(); u != "jo" || p != "pw" {
		json.NewEncoder(w).Encode(map[string]any{"status_code": 40100, "status_message": "You are not authorized"})
		return
	}
	task := func(cost float64, result map[string]any) map[string]any {
		return map[string]any{"status_code": 20000, "cost": cost, "tasks": []any{map[string]any{"status_code": 20000, "cost": cost, "result": []any{result}}}}
	}
	var out map[string]any
	switch r.URL.Path {
	case "/v3/appendix/user_data":
		out = task(0, map[string]any{"login": "jo", "money": map[string]any{"balance": 50}})
	case "/v3/dataforseo_labs/google/domain_rank_overview/live":
		out = task(0.0101, map[string]any{"items": []any{map[string]any{"metrics": map[string]any{"organic": map[string]any{"count": 1200, "etv": 3400, "pos_1": 5, "pos_2_3": 10}}}}})
	case "/v3/backlinks/summary/live":
		rank := dfsRank.Load()
		if rank == 0 {
			rank = 40
		}
		out = task(0.02, map[string]any{"rank": rank, "backlinks": 9000, "referring_domains": 300})
	case "/v3/dataforseo_labs/google/competitors_domain/live":
		dfsRivals.Lock()
		out = task(0.011, map[string]any{"total_count": len(dfsRivals.items), "items": dfsRivals.items})
		dfsRivals.Unlock()
	case "/v3/backlinks/referring_domains/live":
		dfsTargets.Lock()
		items := dfsTargets.domains[dfsTarget(r)]
		dfsTargets.Unlock()
		out = task(0.0203, map[string]any{"total_count": len(items), "items": items})
	case "/v3/dataforseo_labs/google/ranked_keywords/live":
		dfsTargets.Lock()
		items, ok := dfsTargets.keywords[dfsTarget(r)]
		dfsTargets.Unlock()
		if ok {
			out = task(0.0105, map[string]any{"total_count": len(items), "items": items})
			break
		}
		out = task(0.0105, map[string]any{"total_count": 1200, "items": []any{
			map[string]any{"keyword_data": map[string]any{"keyword": "cold email", "keyword_info": map[string]any{"search_volume": 900}},
				"ranked_serp_element": map[string]any{"serp_item": map[string]any{"url": "https://example.com/", "rank_group": 4, "etv": 80}}},
			map[string]any{"keyword_data": map[string]any{"keyword": "outflare", "keyword_info": map[string]any{"search_volume": 50}},
				"ranked_serp_element": map[string]any{"serp_item": map[string]any{"url": "https://example.com/", "rank_group": 1, "etv": 40}}},
		}})
	case "/v3/serp/google/organic/live/regular":
		var tasks []struct {
			Keyword string `json:"keyword"`
			Device  string `json:"device"`
		}
		json.NewDecoder(r.Body).Decode(&tasks)
		organic := func(pos int, u, domain string) map[string]any {
			return map[string]any{"type": "organic", "rank_group": pos, "url": u, "domain": domain, "title": "A result"}
		}
		list := []any{map[string]any{"type": "featured_snippet", "rank_group": 1, "url": "https://rival.example/", "domain": "rival.example"},
			organic(1, "https://rival.example/pricing", "rival.example")}
		if len(tasks) == 1 && tasks[0].Keyword == "proxy pricing" {
			pos := 3
			if tasks[0].Device == "mobile" {
				pos = 2
			}
			list = append(list, organic(pos, "https://www.example.com/pricing", "www.example.com"))
		}
		out = task(0.02, map[string]any{"items": list})
	case "/v3/backlinks/backlinks/live":
		dfsLinks.Lock()
		out = task(0.021, map[string]any{"total_count": len(dfsLinks.items), "items": dfsLinks.items})
		dfsLinks.Unlock()
	default:
		http.NotFound(w, r)
		return
	}
	json.NewEncoder(w).Encode(out)
}

func TestSEOReportsBudgetAndSnapshots(t *testing.T) {
	e := freshEnv(t)
	owner, ws := setupOwner(t, e.h)
	base := "/v1/workspaces/" + ws

	if got := owner.must(200, "GET", base+"/seo", nil)["data"].([]any); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
	// An analytics tool's domain is what SEO follows until domains are set.
	pl := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "plausible", "credentials": map[string]string{"api_key": "pk", "site_id": "example.com"}})
	owner.must(200, "POST", base+"/analytics/"+pl["id"].(string)+"/sync", nil)

	owner.must(422, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "wrong"}})
	i := owner.must(201, "POST", base+"/integrations", map[string]any{"connector": "dataforseo", "credentials": map[string]string{"login": "jo", "password": "pw"}})
	id := i["id"].(string)

	src := owner.must(200, "GET", base+"/seo", nil)["data"].([]any)[0].(map[string]any)
	budget := src["budget"].(map[string]any)
	if src["domains_set"] != false || len(src["domains"].([]any)) != 1 || src["domains"].([]any)[0].(map[string]any)["host"] != "example.com" ||
		budget["unit"] != "USD" || budget["limit"].(float64) != 10 || budget["spent"].(float64) != 0 || src["country"] != "US" {
		t.Fatalf("source: %v", src)
	}

	// A live report costs money, is recorded, and is kept for a day.
	rep := owner.must(200, "GET", base+"/seo/"+id+"/reports/overview", nil)
	cost := rep["cost"].(map[string]any)["usd"].(float64)
	if rep["target"] != "example.com" || rep["totals"].(map[string]any)["referring_domains"].(float64) != 300 || cost < 0.03 || cost > 0.031 || rep["cached"] != false {
		t.Fatalf("report: %v", rep)
	}
	calls := dataForSEOCalls.Load()
	again := owner.must(200, "GET", base+"/seo/"+id+"/reports/overview?target=www.example.com", nil)
	if again["cached"] != true || dataForSEOCalls.Load() != calls {
		t.Fatalf("second ask should be kept: %v", again)
	}
	spend := owner.must(200, "GET", base+"/spend", nil)["data"].([]any)
	if len(spend) != 1 || spend[0].(map[string]any)["provider"] != "dataforseo" || spend[0].(map[string]any)["unit_name"] != "USD" {
		t.Fatalf("spend: %v", spend)
	}
	owner.must(400, "GET", base+"/seo/"+id+"/reports/overview?target=not%20a%20domain", nil)
	owner.must(404, "GET", base+"/seo/"+id+"/reports/nope", nil)
	owner.must(422, "GET", base+"/seo/"+id+"/reports/audit", nil)

	// A snapshot stores the overview and keywords, and joins the overview by domain.
	snap := owner.must(200, "POST", base+"/seo/"+id+"/sync", nil)
	if snap["rows"].(float64) != 3 || snap["unit"] != "USD" || snap["cost"].(float64) < 0.04 {
		t.Fatalf("sync: %v", snap)
	}
	ov := owner.must(200, "GET", base+"/analytics/overview", nil)["data"].([]any)[0].(map[string]any)
	if seo := ov["seo"].([]any); len(seo) != 1 || seo[0].(map[string]any)["organic_keywords"].(float64) != 1200 || seo[0].(map[string]any)["top3"].(float64) != 15 {
		t.Fatalf("overview seo: %v", ov)
	}
	kw := owner.must(200, "GET", base+"/seo/history?report=keywords&host=example.com", nil)["data"].([]any)
	if len(kw) != 2 || kw[0].(map[string]any)["key"] != "cold email" || kw[0].(map[string]any)["position"].(float64) != 4 {
		t.Fatalf("keywords: %v", kw)
	}
	if hist := owner.must(200, "GET", base+"/seo/history", nil)["data"].([]any); len(hist) != 1 {
		t.Fatalf("overview history: %v", hist)
	}

	// Settings: chosen domains, then a budget that stops the next call.
	owner.must(400, "PUT", base+"/seo/"+id+"/settings", map[string]any{"domains": []string{"not a domain"}})
	owner.must(400, "PUT", base+"/seo/"+id+"/settings", map[string]any{"sync": "daily"})
	set := owner.must(200, "PUT", base+"/seo/"+id+"/settings", map[string]any{"domains": []string{"https://www.Example.org/", "example.org"}, "country": "au", "monthly_budget": 0.05})
	if d := set["domains"].([]any); set["domains_set"] != true || len(d) != 1 || d[0].(map[string]any)["host"] != "example.org" || set["country"] != "AU" {
		t.Fatalf("settings: %v", set)
	}
	owner.must(402, "GET", base+"/seo/"+id+"/reports/keywords?target=example.org", nil)
	owner.must(402, "POST", base+"/seo/"+id+"/sync", nil)
	set = owner.must(200, "PUT", base+"/seo/"+id+"/settings", map[string]any{"domains": nil, "monthly_budget": nil})
	if set["domains_set"] != false || set["budget"].(map[string]any)["default"] != true {
		t.Fatalf("back to defaults: %v", set)
	}
}
