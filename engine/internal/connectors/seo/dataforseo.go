package seo

import (
	"context"
	"net/http"
	"strings"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const dataForSEOAPI = "https://api.dataforseo.com"

// DataForSEO reads DataForSEO Labs (rankings, competitors, estimated
// traffic) and the Backlinks API. It is pay as you go: every response says
// what it cost, in US dollars, and that is what gets recorded.
type DataForSEO struct{}

func (DataForSEO) Definition() c.Definition {
	d := def("dataforseo", "DataForSEO", "https://dataforseo.com", "https://docs.dataforseo.com/v3/",
		"https://app.dataforseo.com/api-access",
		"Keyword rankings, rank tracking, estimated search traffic, competitors and backlinks for any domain, paid per call.",
		"Uses DataForSEO Labs for rankings, traffic and competitors, the SERP API to track where your pages rank on Google, and the Backlinks API for links (a separate DataForSEO subscription; without it the overview skips links). "+
			"Every call's cost comes back from DataForSEO and is recorded in Outcomes; a monthly budget on the connection stops calls before they go over.",
		"Pay per call, about $0.01 to $0.05 a report", []c.Field{
			{Name: "login", Label: "API login", Help: "DataForSEO dashboard > API Access. Usually your account email."},
			{Name: "password", Label: "API password", Secret: true, Help: "DataForSEO dashboard > API Access > API password (not your account password)."},
		})
	// The SERP API reads Google's results for rank tracking.
	d.Capabilities = append(d.Capabilities, c.CapSERP)
	return d
}

func (DataForSEO) SEOReports() []string {
	return []string{c.SEOOverview, c.SEOKeywords, c.SEOBacklinks, c.SEOLinks, c.SEOCompetitors}
}

// Estimate follows DataForSEO's price list: Labs live calls are about
// $0.01 a task plus $0.0001 a row, Backlinks about $0.02 a request plus
// $0.00003 a row. The recorded cost is what DataForSEO reports.
func (DataForSEO) Estimate(q c.SEOQuery) c.SEOCost {
	rows := float64(q.Limit)
	switch q.Report {
	case c.SEOOverview:
		return c.SEOCost{USD: 0.0101 + 0.02003, Estimated: true}
	case c.SEOBacklinks, c.SEOLinks:
		return c.SEOCost{USD: 0.02 + 0.00003*rows, Estimated: true}
	default:
		return c.SEOCost{USD: 0.01 + 0.0001*rows, Estimated: true}
	}
}

func (DataForSEO) header(creds c.Creds) http.Header {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(creds["login"], creds["password"])
	return http.Header{"Authorization": {req.Header.Get("Authorization")}}
}

func (d DataForSEO) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var resp dfsResponse[struct {
		Login string `json:"login"`
		Money *struct {
			Balance *float64 `json:"balance"`
		} `json:"money"`
	}]
	if err := get(ctx, cl, "DataForSEO", cl.Base("dataforseo", dataForSEOAPI)+"/v3/appendix/user_data", d.header(creds), &resp); err != nil {
		return c.Account{}, err
	}
	result, err := resp.first("DataForSEO")
	if err != nil {
		return c.Account{}, err
	}
	acct := c.Account{ID: result.Login, Name: result.Login, Details: map[string]string{}}
	if result.Money != nil && result.Money.Balance != nil {
		acct.Details["balance"] = "$" + strings.TrimRight(strings.TrimRight(ftoa(round2(*result.Money.Balance)), "0"), ".")
	}
	return acct, nil
}

// dfsResponse is DataForSEO's envelope: a status for the request, then one
// task per item sent, each with its own status, cost and results.
type dfsResponse[T any] struct {
	StatusCode    int     `json:"status_code"`
	StatusMessage string  `json:"status_message"`
	Cost          float64 `json:"cost"`
	Tasks         []struct {
		StatusCode    int     `json:"status_code"`
		StatusMessage string  `json:"status_message"`
		Cost          float64 `json:"cost"`
		Result        []T     `json:"result"`
	} `json:"tasks"`
}

// dfsError maps DataForSEO's status codes onto error kinds.
func dfsError(code int, msg string) error {
	switch {
	case code == 40100 || code == 40101 || code == 40103:
		return c.Errf(c.ErrAuth, "DataForSEO refused the login and API password: %s", msg)
	case code == 40200 || code == 40201 || code == 40202 || code == 40209 || code == 40210:
		return c.Errf(c.ErrQuota, "DataForSEO says: %s. Add funds or wait for the limit to reset.", msg)
	case code == 40204:
		return c.Errf(c.ErrInvalid, "DataForSEO says: %s. This API needs its own subscription at DataForSEO.", msg)
	case code >= 50000:
		return c.Errf(c.ErrUnavailable, "DataForSEO had an internal error: %s", msg)
	}
	return c.Errf(c.ErrInvalid, "DataForSEO refused the request: %s", msg)
}

func (r dfsResponse[T]) first(name string) (T, error) {
	var zero T
	if r.StatusCode != 0 && r.StatusCode != 20000 {
		return zero, dfsError(r.StatusCode, r.StatusMessage)
	}
	if len(r.Tasks) == 0 {
		return zero, c.Errf(c.ErrUnavailable, "%s returned no task", name)
	}
	t := r.Tasks[0]
	if t.StatusCode != 20000 {
		return zero, dfsError(t.StatusCode, t.StatusMessage)
	}
	if len(t.Result) == 0 {
		return zero, nil
	}
	return t.Result[0], nil
}

func (d DataForSEO) call(ctx context.Context, cl *c.Client, creds c.Creds, path string, task map[string]any, out *dfsResponse[map[string]any], cost *c.SEOCost) error {
	err := post(ctx, cl, "DataForSEO", cl.Base("dataforseo", dataForSEOAPI)+path, d.header(creds), []map[string]any{task}, out)
	cost.USD += out.Cost
	return err
}

// dfsDay keeps the date of a DataForSEO timestamp ("2026-01-31 12:00:00 +00:00").
func dfsDay(v any) string {
	s := str(v)
	if len(s) < 10 {
		return ""
	}
	return s[:10]
}

// locations are DataForSEO's location codes and a language for the
// countries people ask about most.
var locations = map[string]struct {
	code int
	lang string
}{
	"US": {2840, "en"}, "GB": {2826, "en"}, "AU": {2036, "en"}, "CA": {2124, "en"}, "NZ": {2554, "en"},
	"IE": {2372, "en"}, "IN": {2356, "en"}, "SG": {2702, "en"}, "ZA": {2710, "en"}, "DE": {2276, "de"},
	"FR": {2250, "fr"}, "ES": {2724, "es"}, "IT": {2380, "it"}, "NL": {2528, "nl"}, "BR": {2076, "pt"},
	"MX": {2484, "es"}, "SE": {2752, "sv"}, "PL": {2616, "pl"}, "JP": {2392, "ja"},
}

func (d DataForSEO) SEOReport(ctx context.Context, cl *c.Client, creds c.Creds, q c.SEOQuery) (c.SEOReport, error) {
	rep := c.SEOReport{Report: q.Report, Target: q.Target, Country: q.Country, Notes: []string{}}
	loc, ok := locations[q.Country]
	if !ok {
		keys := make([]string, 0, len(locations))
		for k := range locations {
			keys = append(keys, k)
		}
		return rep, c.Errf(c.ErrInvalid, "DataForSEO here covers %s; ask for one of those countries", strings.Join(sorted(keys), ", "))
	}
	labs := map[string]any{"target": q.Target, "location_code": loc.code, "language_code": loc.lang}
	var resp dfsResponse[map[string]any]
	switch q.Report {
	case c.SEOOverview:
		if err := d.call(ctx, cl, creds, "/v3/dataforseo_labs/google/domain_rank_overview/live", labs, &resp, &rep.Cost); err != nil {
			return rep, err
		}
		result, err := resp.first("DataForSEO")
		if err != nil {
			return rep, err
		}
		if item := firstItem(result); item != nil {
			org := obj(obj(item["metrics"])["organic"])
			rep.Totals.OrganicKeywords = num(org["count"])
			rep.Totals.OrganicTraffic = num(org["etv"])
			rep.Totals.TrafficValue = num(org["estimated_paid_traffic_cost"])
			rep.Totals.Top3 = sum(num(org["pos_1"]), num(org["pos_2_3"]))
		}
		var links dfsResponse[map[string]any]
		err = d.call(ctx, cl, creds, "/v3/backlinks/summary/live", map[string]any{"target": q.Target, "rank_scale": "one_hundred", "include_subdomains": true}, &links, &rep.Cost)
		if err == nil {
			var summary map[string]any
			summary, err = links.first("DataForSEO")
			if err == nil && summary != nil {
				rep.Totals.DomainRating = num(summary["rank"])
				rep.Totals.Backlinks = num(summary["backlinks"])
				rep.Totals.ReferringDomains = num(summary["referring_domains"])
			}
		}
		if err != nil {
			// Links are a separate subscription; the rest of the
			// overview still stands.
			if c.KindOf(err) == c.ErrAuth {
				return rep, err
			}
			rep.Notes = append(rep.Notes, "Links are missing: "+err.Error())
		} else {
			rep.Notes = append(rep.Notes, "Domain rating is DataForSEO's rank on a 0 to 100 scale, which is not the same as Ahrefs DR.")
		}
	case c.SEOKeywords:
		labs["limit"] = q.Limit
		labs["order_by"] = []string{"ranked_serp_element.serp_item.etv,desc"}
		if err := d.call(ctx, cl, creds, "/v3/dataforseo_labs/google/ranked_keywords/live", labs, &resp, &rep.Cost); err != nil {
			return rep, err
		}
		result, err := resp.first("DataForSEO")
		if err != nil {
			return rep, err
		}
		rep.Totals.OrganicKeywords = num(result["total_count"])
		for _, it := range items(result) {
			kd := obj(it["keyword_data"])
			serp := obj(obj(it["ranked_serp_element"])["serp_item"])
			rep.Rows = append(rep.Rows, c.SEORow{Key: str(kd["keyword"]), URL: str(serp["url"]), SEOMetrics: c.SEOMetrics{
				Position: num(serp["rank_group"]), Volume: num(obj(kd["keyword_info"])["search_volume"]), Traffic: num(serp["etv"]),
			}})
		}
		rep.Notes = append(rep.Notes, "Traffic is DataForSEO's estimate of monthly visits from each keyword.")
	case c.SEOBacklinks:
		task := map[string]any{"target": q.Target, "limit": q.Limit, "order_by": []string{"rank,desc"}, "rank_scale": "one_hundred", "include_subdomains": true}
		if err := d.call(ctx, cl, creds, "/v3/backlinks/referring_domains/live", task, &resp, &rep.Cost); err != nil {
			return rep, err
		}
		result, err := resp.first("DataForSEO")
		if err != nil {
			return rep, err
		}
		rep.Totals.ReferringDomains = num(result["total_count"])
		for _, it := range items(result) {
			rep.Rows = append(rep.Rows, c.SEORow{Key: str(it["domain"]), SEOMetrics: c.SEOMetrics{
				DomainRating: num(it["rank"]), Backlinks: num(it["backlinks"]),
			}})
		}
	case c.SEOLinks:
		task := map[string]any{"target": q.Target, "mode": "as_is", "limit": q.Limit, "order_by": []string{"domain_from_rank,desc"},
			"rank_scale": "one_hundred", "include_subdomains": true, "backlinks_status_type": "live"}
		if q.Since != "" {
			task["backlinks_status_type"] = "all"
			task["filters"] = []any{"last_seen", ">=", q.Since + " 00:00:00 +00:00"}
		}
		if err := d.call(ctx, cl, creds, "/v3/backlinks/backlinks/live", task, &resp, &rep.Cost); err != nil {
			return rep, err
		}
		result, err := resp.first("DataForSEO")
		if err != nil {
			return rep, err
		}
		rep.Totals.Backlinks = num(result["total_count"])
		for _, it := range items(result) {
			l := &c.SEOLink{From: str(it["url_from"]), To: str(it["url_to"]), Anchor: str(it["anchor"]),
				SourceDomain: strings.ToLower(str(it["domain_from"])), Lost: it["is_lost"] == true,
				FirstSeen: dfsDay(it["first_seen"]), SourceRating: num(it["domain_from_rank"]), Spam: num(it["backlink_spam_score"]),
				ExternalLinks: num(it["page_from_external_links"]), Title: str(it["page_from_title"])}
			if dofollow, ok := it["dofollow"].(bool); ok {
				l.Nofollow = !dofollow
			}
			attrs, _ := it["attributes"].([]any)
			for _, a := range attrs {
				switch str(a) {
				case "sponsored":
					l.Sponsored = true
				case "ugc":
					l.UGC = true
				}
			}
			if l.Lost {
				l.LastSeen = dfsDay(it["last_seen"])
			}
			rep.Rows = append(rep.Rows, c.SEORow{Key: l.From, URL: l.To, Link: l, SEOMetrics: c.SEOMetrics{DomainRating: l.SourceRating}})
		}
	case c.SEOCompetitors:
		labs["limit"] = q.Limit
		labs["exclude_top_domains"] = true
		if err := d.call(ctx, cl, creds, "/v3/dataforseo_labs/google/competitors_domain/live", labs, &resp, &rep.Cost); err != nil {
			return rep, err
		}
		result, err := resp.first("DataForSEO")
		if err != nil {
			return rep, err
		}
		for _, it := range items(result) {
			domain := str(it["domain"])
			if domain == q.Target {
				continue
			}
			org := obj(obj(it["full_domain_metrics"])["organic"])
			rep.Rows = append(rep.Rows, c.SEORow{Key: domain, SEOMetrics: c.SEOMetrics{
				CommonKeywords: num(it["intersections"]), Position: num(it["avg_position"]),
				OrganicKeywords: num(org["count"]), OrganicTraffic: num(org["etv"]),
			}})
		}
	default:
		return rep, c.Errf(c.ErrInvalid, "DataForSEO has no %s report here; Ahrefs reads site audits", q.Report)
	}
	return rep, nil
}

// SERPEngines: DataForSEO reads Google.
func (DataForSEO) SERPEngines() []string { return []string{"google"} }

// EstimateSERP follows DataForSEO's price list for the live SERP API:
// about $0.002 for each 10 results read.
func (DataForSEO) EstimateSERP(q c.SERPQuery) c.SEOCost {
	pages := (max(q.Depth, 10) + 9) / 10
	return c.SEOCost{USD: 0.002 * float64(pages), Estimated: true}
}

// SERP reads Google's organic results with the live SERP API.
func (d DataForSEO) SERP(ctx context.Context, cl *c.Client, creds c.Creds, engine string, q c.SERPQuery) (c.SERPResult, error) {
	out := c.SERPResult{Engine: "google", Items: []c.SERPItem{}}
	if engine != "" && engine != "google" {
		return out, c.Errf(c.ErrInvalid, "DataForSEO here reads Google only")
	}
	loc, ok := locations[q.Country]
	if !ok {
		keys := make([]string, 0, len(locations))
		for k := range locations {
			keys = append(keys, k)
		}
		return out, c.Errf(c.ErrInvalid, "DataForSEO here covers %s; ask for one of those countries", strings.Join(sorted(keys), ", "))
	}
	device := q.Device
	if device == "" {
		device = "desktop"
	}
	task := map[string]any{"keyword": q.Keyword, "location_code": loc.code, "language_code": loc.lang, "device": device, "depth": max(q.Depth, 10)}
	var resp dfsResponse[map[string]any]
	if err := d.call(ctx, cl, creds, "/v3/serp/google/organic/live/regular", task, &resp, &out.Cost); err != nil {
		return out, err
	}
	result, err := resp.first("DataForSEO")
	if err != nil {
		return out, err
	}
	for _, it := range items(result) {
		if str(it["type"]) != "organic" {
			continue
		}
		pos := num(it["rank_group"])
		if pos == nil {
			continue
		}
		out.Items = append(out.Items, c.SERPItem{Position: int(*pos), URL: str(it["url"]), Domain: str(it["domain"]), Title: str(it["title"])})
	}
	return out, nil
}
