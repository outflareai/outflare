package seo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// fake serves canned answers by "METHOD path" and records each request.
func fake(t *testing.T, routes map[string]any) (*c.Client, *[]string) {
	t.Helper()
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery+" "+string(b))
		if r.Header.Get("Authorization") == "Bearer bad" {
			w.WriteHeader(401)
			return
		}
		v, ok := routes[r.Method+" "+r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(v)
	}))
	t.Cleanup(srv.Close)
	return &c.Client{BaseURLs: map[string]string{"dataforseo": srv.URL, "ahrefs": srv.URL}}, &calls
}

var ctx = context.Background()

func val(p *float64) float64 {
	if p == nil {
		return -1
	}
	return *p
}

func q(report string) c.SEOQuery {
	out, _ := c.CheckSEOQuery(c.SEOQuery{Report: report, Target: "example.com", Limit: 10})
	return out
}

func dfs(cost float64, result ...map[string]any) map[string]any {
	return map[string]any{"status_code": 20000, "cost": cost, "tasks": []any{map[string]any{"status_code": 20000, "cost": cost, "result": result}}}
}

func TestEveryConnectorIsAnSEOReader(t *testing.T) {
	for _, x := range All() {
		r, ok := x.(c.SEOReader)
		d := x.Definition()
		if !ok || d.Category != c.CategorySEO || !d.Has(c.CapSEO) || len(r.SEOReports()) == 0 {
			t.Fatalf("%s: reader %v, def %+v", d.ID, ok, d)
		}
		for _, report := range r.SEOReports() {
			if e := r.Estimate(q(report)); e.USD <= 0 && e.Units <= 0 {
				t.Fatalf("%s %s: no estimate", d.ID, report)
			}
		}
	}
}

func TestDataForSEO(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"GET /v3/appendix/user_data": dfs(0, map[string]any{"login": "jo@example.com", "money": map[string]any{"balance": 41.5}}),
		"POST /v3/dataforseo_labs/google/domain_rank_overview/live": dfs(0.0101, map[string]any{"items": []any{map[string]any{
			"metrics": map[string]any{"organic": map[string]any{"count": 1200, "etv": 3400.5, "estimated_paid_traffic_cost": 5100, "pos_1": 10, "pos_2_3": 25}},
		}}}),
		"POST /v3/backlinks/summary/live": dfs(0.02, map[string]any{"rank": 41, "backlinks": 9100, "referring_domains": 380}),
		"POST /v3/dataforseo_labs/google/ranked_keywords/live": dfs(0.0103, map[string]any{"total_count": 1200, "items": []any{map[string]any{
			"keyword_data":        map[string]any{"keyword": "cold email tool", "keyword_info": map[string]any{"search_volume": 2400}},
			"ranked_serp_element": map[string]any{"serp_item": map[string]any{"url": "https://example.com/", "rank_group": 3, "etv": 310.2}},
		}}}),
		"POST /v3/backlinks/referring_domains/live": dfs(0.0203, map[string]any{"total_count": 380, "items": []any{map[string]any{"domain": "news.example.org", "rank": 72, "backlinks": 4}}}),
		"POST /v3/dataforseo_labs/google/competitors_domain/live": dfs(0.0102, map[string]any{"items": []any{
			map[string]any{"domain": "example.com", "intersections": 1200},
			map[string]any{"domain": "rival-com.example", "intersections": 300, "avg_position": 12.5, "full_domain_metrics": map[string]any{"organic": map[string]any{"count": 900, "etv": 2000}}},
		}}),
	})
	creds := c.Creds{"login": "jo@example.com", "password": "pw"}
	d := DataForSEO{}
	acct, err := d.Test(ctx, cl, creds)
	if err != nil || acct.Name != "jo@example.com" || acct.Details["balance"] != "$41.5" {
		t.Fatalf("test: %+v %v", acct, err)
	}

	rep, err := d.SEOReport(ctx, cl, creds, q(c.SEOOverview))
	if err != nil || val(rep.Totals.OrganicKeywords) != 1200 || val(rep.Totals.Top3) != 35 || val(rep.Totals.DomainRating) != 41 ||
		val(rep.Totals.ReferringDomains) != 380 || rep.Cost.USD < 0.03 || rep.Cost.USD > 0.031 || rep.Cost.Estimated {
		t.Fatalf("overview: %+v %v", rep, err)
	}
	if !strings.Contains((*calls)[1], `"location_code":2840`) || !strings.Contains((*calls)[1], `"language_code":"en"`) {
		t.Fatalf("location: %s", (*calls)[1])
	}

	rep, err = d.SEOReport(ctx, cl, creds, q(c.SEOKeywords))
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Key != "cold email tool" || val(rep.Rows[0].Position) != 3 || val(rep.Rows[0].Volume) != 2400 || rep.Rows[0].URL != "https://example.com/" {
		t.Fatalf("keywords: %+v %v", rep, err)
	}
	rep, err = d.SEOReport(ctx, cl, creds, q(c.SEOBacklinks))
	if err != nil || len(rep.Rows) != 1 || val(rep.Rows[0].DomainRating) != 72 {
		t.Fatalf("backlinks: %+v %v", rep, err)
	}
	rep, err = d.SEOReport(ctx, cl, creds, q(c.SEOCompetitors))
	if err != nil || len(rep.Rows) != 1 || rep.Rows[0].Key != "rival-com.example" || val(rep.Rows[0].CommonKeywords) != 300 {
		t.Fatalf("competitors: %+v %v", rep, err)
	}
	if _, err := d.SEOReport(ctx, cl, creds, q(c.SEOAudit)); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("audit: %v", err)
	}
	bad := q(c.SEOOverview)
	bad.Country = "ZZ"
	if _, err := d.SEOReport(ctx, cl, creds, bad); c.KindOf(err) != c.ErrInvalid {
		t.Fatalf("country: %v", err)
	}
}

func TestDataForSEOErrors(t *testing.T) {
	cl, _ := fake(t, map[string]any{
		"POST /v3/dataforseo_labs/google/domain_rank_overview/live": dfs(0.0101, map[string]any{"items": []any{}}),
		"POST /v3/backlinks/summary/live":                           map[string]any{"status_code": 20000, "tasks": []any{map[string]any{"status_code": 40204, "status_message": "Access denied"}}},
		"POST /v3/dataforseo_labs/google/ranked_keywords/live":      map[string]any{"status_code": 40200, "status_message": "Payment Required."},
	})
	creds := c.Creds{"login": "a", "password": "b"}
	rep, err := DataForSEO{}.SEOReport(ctx, cl, creds, q(c.SEOOverview))
	if err != nil || len(rep.Notes) != 1 || !strings.Contains(rep.Notes[0], "subscription") {
		t.Fatalf("links without a subscription: %+v %v", rep, err)
	}
	if _, err := (DataForSEO{}).SEOReport(ctx, cl, creds, q(c.SEOKeywords)); c.KindOf(err) != c.ErrQuota {
		t.Fatalf("out of funds: %v", err)
	}
}

func TestAhrefs(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"GET /subscription-info/limits-and-usage": map[string]any{"limits_and_usage": map[string]any{"subscription": "Advanced", "units_limit_api_key": 100000, "units_usage_api_key": 2500}},
		"GET /site-explorer/domain-rating":        map[string]any{"domain_rating": map[string]any{"domain_rating": 54.5, "ahrefs_rank": 120000}},
		"GET /site-explorer/backlinks-stats":      map[string]any{"metrics": map[string]any{"live": 12000, "live_refdomains": 640}},
		"GET /site-explorer/metrics":              map[string]any{"metrics": map[string]any{"org_keywords": 2100, "org_keywords_1_3": 80, "org_traffic": 5300, "org_cost": 712000}},
		"GET /site-explorer/organic-keywords": map[string]any{"keywords": []any{
			map[string]any{"keyword": "cold email", "best_position": 4, "best_position_url": "https://example.com/blog", "volume": 9000, "sum_traffic": 420},
		}},
		"GET /site-explorer/refdomains":          map[string]any{"refdomains": []any{map[string]any{"domain": "big.org", "domain_rating": 88, "links_to_target": 3}}},
		"GET /site-explorer/organic-competitors": map[string]any{"competitors": []any{map[string]any{"competitor_domain": "rival-com.example", "keywords_common": 410, "domain_rating": 60}}},
		"GET /site-audit/projects":               map[string]any{"healthscores": []any{map[string]any{"project_id": "77", "target_url": "https://example.com/", "health_score": 86, "total": 420}}},
		"GET /site-audit/issues": map[string]any{"issues": []any{
			map[string]any{"name": "Missing alt text", "importance": "Notice", "crawled": 50},
			map[string]any{"name": "404 page", "importance": "Error", "crawled": 3},
			map[string]any{"name": "Fixed thing", "importance": "Error", "crawled": 0},
		}},
	})
	creds := c.Creds{"api_key": "k"}
	a := Ahrefs{}
	acct, err := a.Test(ctx, cl, creds)
	if err != nil || acct.Name != "Ahrefs Advanced" || val(acct.Credits) != 97500 {
		t.Fatalf("test: %+v %v", acct, err)
	}
	if _, err := a.Test(ctx, cl, c.Creds{"api_key": "bad"}); c.KindOf(err) != c.ErrAuth {
		t.Fatalf("bad key: %v", err)
	}

	rep, err := a.SEOReport(ctx, cl, creds, q(c.SEOOverview))
	if err != nil || val(rep.Totals.DomainRating) != 54.5 || val(rep.Totals.ReferringDomains) != 640 || val(rep.Totals.TrafficValue) != 7120 ||
		val(rep.Totals.Top3) != 80 || rep.Cost.Units != 150 || !rep.Cost.Estimated {
		t.Fatalf("overview: %+v %v", rep, err)
	}
	rep, err = a.SEOReport(ctx, cl, creds, q(c.SEOKeywords))
	if err != nil || len(rep.Rows) != 1 || val(rep.Rows[0].Position) != 4 || rep.Rows[0].URL != "https://example.com/blog" || rep.Cost.Units != 50 {
		t.Fatalf("keywords: %+v %v", rep, err)
	}
	last := (*calls)[len(*calls)-1]
	if !strings.Contains(last, "country=us") || !strings.Contains(last, "order_by=sum_traffic%3Adesc") || !strings.Contains(last, "limit=10") {
		t.Fatalf("keywords request: %s", last)
	}
	rep, err = a.SEOReport(ctx, cl, creds, q(c.SEOBacklinks))
	if err != nil || len(rep.Rows) != 1 || val(rep.Rows[0].DomainRating) != 88 {
		t.Fatalf("backlinks: %+v %v", rep, err)
	}
	rep, err = a.SEOReport(ctx, cl, creds, q(c.SEOCompetitors))
	if err != nil || len(rep.Rows) != 1 || val(rep.Rows[0].CommonKeywords) != 410 {
		t.Fatalf("competitors: %+v %v", rep, err)
	}
	rep, err = a.SEOReport(ctx, cl, creds, q(c.SEOAudit))
	if err != nil || val(rep.Totals.HealthScore) != 86 || len(rep.Rows) != 2 || rep.Rows[0].Key != "404 page" || rep.Rows[0].Kind != "error" {
		t.Fatalf("audit: %+v %v", rep, err)
	}
}

func TestLinksReports(t *testing.T) {
	cl, calls := fake(t, map[string]any{
		"POST /v3/backlinks/backlinks/live": dfs(0.021, map[string]any{"total_count": 2, "items": []any{
			map[string]any{"url_from": "https://blog.example.org/post", "url_to": "https://example.com/", "anchor": "Example", "domain_from": "Blog.Example.org",
				"dofollow": true, "domain_from_rank": 55, "backlink_spam_score": 3, "first_seen": "2026-09-01 10:00:00 +00:00", "last_seen": "2026-10-20 10:00:00 +00:00",
				"is_lost": false, "page_from_external_links": 12, "page_from_title": "A post", "page_from_rank": 33, "item_type": "image",
				"semantic_location": "article", "page_from_language": "en", "domain_from_platform_type": []any{"blogs", "unknown"},
				"url_to_redirect_target": "https://example.com/home", "url_to_status_code": 301},
			map[string]any{"url_from": "https://spam.example.net/x", "url_to": "https://example.com/pricing", "anchor": "cheap", "domain_from": "spam.example.net",
				"dofollow": false, "attributes": []any{"nofollow", "sponsored"}, "domain_from_rank": 2, "backlink_spam_score": 80, "first_seen": "2026-08-01 00:00:00 +00:00",
				"last_seen": "2026-10-10 00:00:00 +00:00", "is_lost": true},
		}}),
		"GET /site-explorer/all-backlinks": map[string]any{"backlinks": []any{
			map[string]any{"url_from": "https://news.example.org/a", "url_to": "https://example.com/", "anchor": "here", "name_source": "news.example.org",
				"is_nofollow": false, "is_lost": true, "lost_reason": "removedfromhtml", "first_seen_link": "2026-05-01T00:00:00Z", "last_seen": "2026-10-02T00:00:00Z",
				"domain_rating_source": 71, "is_spam": false, "links_external": 4, "title": "News", "url_rating_source": 12.5, "positions": 40,
				"linked_domains_source_page": 9, "link_type": "text", "is_content": true, "page_type_source": "/Article/Listicle, /Listing",
				"page_category_source": "/Computers/Security", "languages": []any{"en"}, "powered_by": []any{"WordPress"}, "source_page_author": "Ana",
				"url_redirect": []any{"https://example.com/b"}, "redirect_kind": []any{301}, "traffic": 120, "traffic_domain": 5000, "refdomains_source": 7},
		}},
	})
	lq := q(c.SEOLinks)
	lq.Since = "2026-08-01"
	rep, err := DataForSEO{}.SEOReport(ctx, cl, c.Creds{"login": "jo", "password": "pw"}, lq)
	if err != nil || len(rep.Rows) != 2 || rep.Cost.USD != 0.021 {
		t.Fatalf("dfs links: %v %+v", err, rep)
	}
	a, b := rep.Rows[0].Link, rep.Rows[1].Link
	if a.SourceDomain != "blog.example.org" || a.Nofollow || a.Lost || a.FirstSeen != "2026-09-01" || a.LastSeen != "" || val(a.Spam) != 3 || val(a.SourceRating) != 55 ||
		!b.Nofollow || !b.Sponsored || !b.Lost || b.LastSeen != "2026-10-10" {
		t.Fatalf("dfs rows: %+v %+v", a, b)
	}
	if last := (*calls)[len(*calls)-1]; !strings.Contains(last, `"backlinks_status_type":"all"`) || !strings.Contains(last, `"2026-08-01 00:00:00 +00:00"`) {
		t.Fatalf("dfs request: %s", last)
	}
	rep, err = Ahrefs{}.SEOReport(ctx, cl, c.Creds{"api_key": "k"}, lq)
	if err != nil || len(rep.Rows) != 1 || rep.Cost.Units != 50 {
		t.Fatalf("ahrefs links: %v %+v", err, rep)
	}
	l := rep.Rows[0].Link
	if !l.Lost || l.LostReason != "removedfromhtml" || l.FirstSeen != "2026-05-01" || l.LastSeen != "2026-10-02" || val(l.Spam) != 0 || val(l.SourceRating) != 71 {
		t.Fatalf("ahrefs row: %+v", l)
	}
	if last := (*calls)[len(*calls)-1]; !strings.Contains(last, "history=since%3A2026-08-01") || strings.Contains(last, "traffic") {
		t.Fatalf("ahrefs request: %s", last)
	}
	if val(l.URLRating) != 12.5 || val(l.PageKeywords) != 40 || val(l.LinkedDomains) != 9 || l.LinkType != "text" || !l.InContent ||
		len(l.PageTypes) != 2 || l.PageTypes[1] != "/Listing" || l.PageCategories[0] != "/Computers/Security" || l.Platforms[0] != "WordPress" ||
		l.Author != "Ana" || len(l.Hops) != 1 || l.Hops[0] != (c.SEOHop{Kind: "301", URL: "https://example.com/b"}) || l.PageTraffic != nil || l.DomainTraffic != nil {
		t.Fatalf("ahrefs details: %+v", l)
	}
	// Traffic is read only when asked for, and costs 26 units a link.
	lq.Traffic = true
	rep, err = Ahrefs{}.SEOReport(ctx, cl, c.Creds{"api_key": "k"}, lq)
	if err != nil || val(rep.Rows[0].Link.PageTraffic) != 120 || val(rep.Rows[0].Link.DomainTraffic) != 5000 || val(rep.Rows[0].Link.PageRefDomains) != 7 {
		t.Fatalf("ahrefs traffic: %v %+v", err, rep.Rows)
	}
	if last := (*calls)[len(*calls)-1]; !strings.Contains(last, "traffic_domain") {
		t.Fatalf("ahrefs traffic request: %s", last)
	}
	if est := (Ahrefs{}).Estimate(c.SEOQuery{Report: c.SEOLinks, Limit: 200, Traffic: true}); est.Units != 5200 {
		t.Fatalf("traffic estimate: %+v", est)
	}
	d := DataForSEO{}
	rep, _ = d.SEOReport(ctx, cl, c.Creds{"login": "jo", "password": "pw"}, lq)
	if a := rep.Rows[0].Link; val(a.URLRating) != 33 || a.LinkType != "image" || !a.InContent || a.Languages[0] != "en" || len(a.Platforms) != 1 ||
		a.LastChecked != "2026-10-20" || len(a.Hops) != 1 || a.Hops[0].Kind != "301" {
		t.Fatalf("dfs details: %+v", a)
	}
}
