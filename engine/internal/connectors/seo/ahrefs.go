package seo

import (
	"context"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

const ahrefsAPI = "https://api.ahrefs.com/v3"

// Ahrefs reads Ahrefs API v3: Site Explorer for domain rating, backlinks,
// organic keywords and competitors, and Site Audit for the projects the
// account already crawls. The API is part of an Ahrefs plan and spends API
// units; Ahrefs does not say per response how many, so the recorded units
// are worked out from its price list (50 units at least per request, more
// per row for volume and traffic columns).
type Ahrefs struct{}

func (Ahrefs) Definition() c.Definition {
	d := def("ahrefs", "Ahrefs", "https://ahrefs.com", "https://docs.ahrefs.com/docs/api/reference/introduction",
		"https://app.ahrefs.com/account/api-keys",
		"Domain Rating, backlinks, organic keywords, competitors and Site Audit health for your domains.",
		"Needs an Ahrefs plan with API access and an API key. Reads Site Explorer for any domain and Site Audit for projects your account already crawls. "+
			"Calls spend Ahrefs API units; Outflare records an estimate of each call in Outcomes and stops at the connection's monthly budget.",
		"API units from your Ahrefs plan", []c.Field{
			{Name: "api_key", Label: "API key", Secret: true, Help: "Ahrefs > Account settings > API keys. Give it a monthly unit limit there too if you like."},
		})
	// Ahrefs Connect: the person signs in and calls spend their units.
	// Tokens last a year and do not refresh.
	d.Auth, d.AuthMethods = c.AuthOAuth2, c.Both
	d.OAuth = &c.OAuth{
		AuthURL: "https://app.ahrefs.com/web/oauth/authorize", TokenURL: "https://ahrefs.com/oauth/token",
		Scopes: []string{"apiv3-integration-apps"},
	}
	return d
}

func (Ahrefs) SEOReports() []string {
	return []string{c.SEOOverview, c.SEOKeywords, c.SEOBacklinks, c.SEOLinks, c.SEOCompetitors, c.SEOAudit}
}

// rowUnits is what one row costs: 1, plus 10 for each volume or traffic
// column the report asks for.
var rowUnits = map[string]float64{c.SEOKeywords: 21, c.SEOBacklinks: 11, c.SEOCompetitors: 11, c.SEOLinks: 1}

func (Ahrefs) Estimate(q c.SEOQuery) c.SEOCost {
	switch q.Report {
	case c.SEOOverview:
		return c.SEOCost{Units: 150, Estimated: true}
	case c.SEOAudit:
		return c.SEOCost{Units: 100, Estimated: true}
	}
	return c.SEOCost{Units: math.Max(50, float64(q.Limit)*rowUnits[q.Report]), Estimated: true}
}

func (Ahrefs) get(ctx context.Context, cl *c.Client, creds c.Creds, path string, q url.Values, out any) error {
	return get(ctx, cl, "Ahrefs", cl.Base("ahrefs", ahrefsAPI)+path+"?"+q.Encode(),
		http.Header{"Authorization": {"Bearer " + c.Token(creds)}}, out)
}

func (a Ahrefs) Test(ctx context.Context, cl *c.Client, creds c.Creds) (c.Account, error) {
	var resp struct {
		L struct {
			Subscription string   `json:"subscription"`
			Limit        *float64 `json:"units_limit_api_key"`
			Used         float64  `json:"units_usage_api_key"`
			Workspace    *float64 `json:"units_limit_workspace"`
			WorkspaceUse *float64 `json:"units_usage_workspace"`
		} `json:"limits_and_usage"`
	}
	if err := a.get(ctx, cl, creds, "/subscription-info/limits-and-usage", url.Values{}, &resp); err != nil {
		return c.Account{}, err
	}
	acct := c.Account{Name: "Ahrefs " + resp.L.Subscription, Details: map[string]string{"units_used_this_month": itoa(int(resp.L.Used))}}
	limit := resp.L.Limit
	if limit == nil {
		limit = resp.L.Workspace
	}
	if limit != nil {
		left := *limit - resp.L.Used
		if resp.L.WorkspaceUse != nil && resp.L.Limit == nil {
			left = *limit - *resp.L.WorkspaceUse
		}
		acct.Credits = c.Num(left)
	}
	return acct, nil
}

func (a Ahrefs) SEOReport(ctx context.Context, cl *c.Client, creds c.Creds, q c.SEOQuery) (c.SEOReport, error) {
	rep := c.SEOReport{Report: q.Report, Target: q.Target, Country: q.Country, Notes: []string{}}
	today := time.Now().UTC().Format(time.DateOnly)
	base := url.Values{"target": {q.Target}, "date": {today}, "mode": {"subdomains"}, "output": {"json"}}
	country := strings.ToLower(q.Country)
	rep.Cost = a.Estimate(q)
	switch q.Report {
	case c.SEOOverview:
		var dr struct {
			DR struct {
				DomainRating *float64 `json:"domain_rating"`
			} `json:"domain_rating"`
		}
		var links struct {
			M struct {
				Live           *float64 `json:"live"`
				LiveRefdomains *float64 `json:"live_refdomains"`
			} `json:"metrics"`
		}
		var metrics struct {
			M struct {
				OrgKeywords   *float64 `json:"org_keywords"`
				OrgKeywords13 *float64 `json:"org_keywords_1_3"`
				OrgTraffic    *float64 `json:"org_traffic"`
				OrgCost       *float64 `json:"org_cost"`
			} `json:"metrics"`
		}
		if err := a.get(ctx, cl, creds, "/site-explorer/domain-rating", url.Values{"target": {q.Target}, "date": {today}, "output": {"json"}}, &dr); err != nil {
			return rep, err
		}
		if err := a.get(ctx, cl, creds, "/site-explorer/backlinks-stats", base, &links); err != nil {
			return rep, err
		}
		mq := cloneValues(base)
		mq.Set("country", country)
		if err := a.get(ctx, cl, creds, "/site-explorer/metrics", mq, &metrics); err != nil {
			return rep, err
		}
		rep.Totals = c.SEOMetrics{
			DomainRating: n(dr.DR.DomainRating), Backlinks: n(links.M.Live), ReferringDomains: n(links.M.LiveRefdomains),
			OrganicKeywords: n(metrics.M.OrgKeywords), Top3: n(metrics.M.OrgKeywords13), OrganicTraffic: n(metrics.M.OrgTraffic),
		}
		if metrics.M.OrgCost != nil {
			rep.Totals.TrafficValue = c.Num(*metrics.M.OrgCost / 100)
		}
		rep.Notes = append(rep.Notes, "Keywords and traffic are for "+q.Country+"; links are worldwide.")
	case c.SEOKeywords:
		v := cloneValues(base)
		v.Set("country", country)
		v.Set("select", "keyword,best_position,best_position_url,volume,sum_traffic")
		v.Set("order_by", "sum_traffic:desc")
		v.Set("limit", itoa(q.Limit))
		var resp struct {
			Keywords []struct {
				Keyword  string   `json:"keyword"`
				Position *float64 `json:"best_position"`
				URL      *string  `json:"best_position_url"`
				Volume   *float64 `json:"volume"`
				Traffic  *float64 `json:"sum_traffic"`
			} `json:"keywords"`
		}
		if err := a.get(ctx, cl, creds, "/site-explorer/organic-keywords", v, &resp); err != nil {
			return rep, err
		}
		for _, k := range resp.Keywords {
			row := c.SEORow{Key: k.Keyword, SEOMetrics: c.SEOMetrics{Position: n(k.Position), Volume: n(k.Volume), Traffic: n(k.Traffic)}}
			if k.URL != nil {
				row.URL = *k.URL
			}
			rep.Rows = append(rep.Rows, row)
		}
		rep.Cost.Units = math.Max(50, float64(len(resp.Keywords))*rowUnits[q.Report])
	case c.SEOBacklinks:
		v := cloneValues(base)
		v.Del("date")
		v.Set("history", "live")
		v.Set("select", "domain,domain_rating,links_to_target,traffic_domain")
		v.Set("order_by", "domain_rating:desc")
		v.Set("limit", itoa(q.Limit))
		var resp struct {
			Refdomains []struct {
				Domain  string   `json:"domain"`
				DR      *float64 `json:"domain_rating"`
				Links   *float64 `json:"links_to_target"`
				Traffic *float64 `json:"traffic_domain"`
			} `json:"refdomains"`
		}
		if err := a.get(ctx, cl, creds, "/site-explorer/refdomains", v, &resp); err != nil {
			return rep, err
		}
		for _, d := range resp.Refdomains {
			rep.Rows = append(rep.Rows, c.SEORow{Key: d.Domain, SEOMetrics: c.SEOMetrics{DomainRating: n(d.DR), Backlinks: n(d.Links), OrganicTraffic: n(d.Traffic)}})
		}
		rep.Cost.Units = math.Max(50, float64(len(resp.Refdomains))*rowUnits[q.Report])
	case c.SEOLinks:
		v := url.Values{"target": {q.Target}, "mode": {"subdomains"}, "output": {"json"}, "aggregation": {"similar_links"},
			"select":   {"url_from,url_to,anchor,name_source,is_nofollow,is_sponsored,is_ugc,is_lost,lost_reason,first_seen_link,last_seen,domain_rating_source,is_spam,links_external,title"},
			"order_by": {"domain_rating_source:desc"}, "limit": {itoa(q.Limit)}, "history": {"live"}}
		if q.Since != "" {
			v.Set("history", "since:"+q.Since)
		}
		var resp struct {
			Backlinks []struct {
				From       string   `json:"url_from"`
				To         string   `json:"url_to"`
				Anchor     string   `json:"anchor"`
				Source     string   `json:"name_source"`
				Nofollow   bool     `json:"is_nofollow"`
				Sponsored  bool     `json:"is_sponsored"`
				UGC        bool     `json:"is_ugc"`
				Lost       bool     `json:"is_lost"`
				LostReason *string  `json:"lost_reason"`
				FirstSeen  *string  `json:"first_seen_link"`
				LastSeen   *string  `json:"last_seen"`
				DR         *float64 `json:"domain_rating_source"`
				Spam       bool     `json:"is_spam"`
				External   *float64 `json:"links_external"`
				Title      string   `json:"title"`
			} `json:"backlinks"`
		}
		if err := a.get(ctx, cl, creds, "/site-explorer/all-backlinks", v, &resp); err != nil {
			return rep, err
		}
		for _, b := range resp.Backlinks {
			l := &c.SEOLink{From: b.From, To: b.To, Anchor: b.Anchor, SourceDomain: strings.ToLower(b.Source), Nofollow: b.Nofollow,
				Sponsored: b.Sponsored, UGC: b.UGC, Lost: b.Lost, FirstSeen: day(b.FirstSeen), SourceRating: n(b.DR),
				ExternalLinks: n(b.External), Title: b.Title, Spam: c.Num(0)}
			if b.Spam {
				l.Spam = c.Num(100)
			}
			if b.Lost {
				l.LastSeen = day(b.LastSeen)
				if b.LostReason != nil {
					l.LostReason = *b.LostReason
				}
			}
			rep.Rows = append(rep.Rows, c.SEORow{Key: b.From, URL: b.To, Link: l, SEOMetrics: c.SEOMetrics{DomainRating: n(b.DR)}})
		}
		rep.Cost.Units = math.Max(50, float64(len(resp.Backlinks))*rowUnits[q.Report])
	case c.SEOCompetitors:
		v := cloneValues(base)
		v.Set("country", country)
		v.Set("select", "competitor_domain,keywords_common,domain_rating,traffic")
		v.Set("order_by", "keywords_common:desc")
		v.Set("limit", itoa(q.Limit))
		var resp struct {
			Competitors []struct {
				Domain  *string  `json:"competitor_domain"`
				Common  *float64 `json:"keywords_common"`
				DR      *float64 `json:"domain_rating"`
				Traffic *float64 `json:"traffic"`
			} `json:"competitors"`
		}
		if err := a.get(ctx, cl, creds, "/site-explorer/organic-competitors", v, &resp); err != nil {
			return rep, err
		}
		for _, x := range resp.Competitors {
			if x.Domain == nil {
				continue
			}
			rep.Rows = append(rep.Rows, c.SEORow{Key: *x.Domain, SEOMetrics: c.SEOMetrics{CommonKeywords: n(x.Common), DomainRating: n(x.DR), OrganicTraffic: n(x.Traffic)}})
		}
		rep.Cost.Units = math.Max(50, float64(len(resp.Competitors))*rowUnits[q.Report])
	case c.SEOAudit:
		var projects struct {
			Healthscores []struct {
				ProjectID   string   `json:"project_id"`
				TargetURL   string   `json:"target_url"`
				HealthScore *float64 `json:"health_score"`
				Total       *float64 `json:"total"`
				Errors      *float64 `json:"urls_with_errors"`
			} `json:"healthscores"`
		}
		if err := a.get(ctx, cl, creds, "/site-audit/projects", url.Values{"project_url": {q.Target}, "output": {"json"}}, &projects); err != nil {
			return rep, err
		}
		if len(projects.Healthscores) == 0 {
			rep.Notes = append(rep.Notes, "Ahrefs has no Site Audit project for "+q.Target+". Create one in Ahrefs Site Audit and it shows here after its first crawl.")
			return rep, nil
		}
		p := projects.Healthscores[0]
		rep.Totals = c.SEOMetrics{HealthScore: n(p.HealthScore), Pages: n(p.Total)}
		var issues struct {
			Issues []struct {
				Name       string   `json:"name"`
				Importance string   `json:"importance"`
				Crawled    *float64 `json:"crawled"`
			} `json:"issues"`
		}
		if err := a.get(ctx, cl, creds, "/site-audit/issues", url.Values{"project_id": {p.ProjectID}, "output": {"json"}}, &issues); err != nil {
			return rep, err
		}
		rank := map[string]int{"error": 0, "warning": 1, "notice": 2}
		for _, is := range issues.Issues {
			if is.Crawled == nil || *is.Crawled == 0 {
				continue
			}
			rep.Rows = append(rep.Rows, c.SEORow{Key: is.Name, Kind: strings.ToLower(is.Importance), SEOMetrics: c.SEOMetrics{Pages: n(is.Crawled)}})
		}
		sortRows(rep.Rows, func(a, b c.SEORow) bool {
			if rank[a.Kind] != rank[b.Kind] {
				return rank[a.Kind] < rank[b.Kind]
			}
			return *a.Pages > *b.Pages
		})
		if len(rep.Rows) > q.Limit {
			rep.Rows = rep.Rows[:q.Limit]
		}
	default:
		return rep, c.Errf(c.ErrInvalid, "Ahrefs has no %s report", q.Report)
	}
	return rep, nil
}

// day keeps the date of a provider's timestamp.
func day(ts *string) string {
	if ts == nil || len(*ts) < 10 {
		return ""
	}
	return (*ts)[:10]
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, x := range v {
		out[k] = append([]string(nil), x...)
	}
	return out
}
