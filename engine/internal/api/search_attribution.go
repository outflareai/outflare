package api

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/outflareai/outflare/engine/internal/seointel"
	"math"
	"net/url"
	"sort"
	"strings"
)

func (s *Server) insightAttribution(ctx context.Context, ws, brand string, site *seoSite, win searchWindow, report, integration string, out *seointel.InsightReport) error {
	// Pick one source to avoid adding the same visits from two GA4 properties.
	var id, property string
	e := s.pool.QueryRow(ctx, `SELECT integration_id::text,site FROM search_ga4_outcomes o WHERE workspace_id=$1 AND brand_id=$2 AND EXISTS(SELECT 1 FROM integrations i WHERE i.workspace_id=$1 AND i.id=o.integration_id AND integration_serves(i,$2::uuid)) AND lower(host)=ANY($3) AND day BETWEEN $4 AND $5 AND ($6='' OR integration_id::text=$6) GROUP BY integration_id,site ORDER BY sum(sessions) DESC,integration_id LIMIT 1`, ws, brand, seointel.PageHosts(site.Host), win.from, win.to, integration).Scan(&id, &property)
	if e == pgx.ErrNoRows {
		out.Status = "not_connected"
		out.Notes = append(out.Notes, "Connect GA4, assign its property to this brand, and sync landing-page outcomes.")
		return nil
	}
	if e != nil {
		return e
	}
	rows, e := s.pool.Query(ctx, `SELECT path,source,currency,sum(sessions)::float8,sum(conversions)::float8,sum(revenue_micros)::bigint FROM search_ga4_outcomes WHERE workspace_id=$1 AND brand_id=$2 AND lower(host)=ANY($3) AND integration_id=$4 AND site=$5 AND day BETWEEN $6 AND $7 GROUP BY path,source,currency ORDER BY sum(sessions) DESC LIMIT 100000`, ws, brand, seointel.PageHosts(site.Host), id, property, win.from, win.to)
	if e != nil {
		return e
	}
	data, e := pgx.CollectRows(rows, func(r pgx.CollectableRow) (seointel.InsightRow, error) {
		var x seointel.InsightRow
		var currency string
		e := r.Scan(&x.Page, &x.Source, &currency, &x.Sessions, &x.Conversions, &x.RevenueMicros)
		x.Key = x.Page + " · " + x.Source
		x.Currency = currency
		x.Status = "measured"
		return x, e
	})
	if e != nil {
		return e
	}
	total := 0.0
	for _, x := range data {
		total += x.Sessions
	}
	out.Summary["all_sessions"] = total
	if report == "ai-referrals" {
		for _, x := range data {
			if assistantSource(x.Source) {
				if total > 0 {
					x.Share = x.Sessions / total
				}
				out.Data = append(out.Data, x)
				out.Summary["ai_sessions"] += x.Sessions
			}
		}
		if total > 0 {
			out.Summary["ai_share"] = out.Summary["ai_sessions"] / total
		}
		out.Notes = append(out.Notes, "Measured GA4 referrers from known assistant domains; visits without referrers are not identifiable as AI traffic.")
		return nil
	}
	// Show measured landing pages alongside an estimated allocation to queries.
	pages := map[string]seointel.InsightRow{}
	googlePages := map[string]seointel.InsightRow{}
	for _, x := range data {
		key := x.Page + " · " + x.Currency
		p := pages[key]
		p.Key = key
		p.Page = x.Page
		p.Sessions += x.Sessions
		p.Conversions += x.Conversions
		p.RevenueMicros += x.RevenueMicros
		p.Currency = x.Currency
		p.Status = "measured_landing_page"
		pages[key] = p
		if strings.ToLower(x.Source) == "google / organic" {
			g := googlePages[key]
			g.Sessions += x.Sessions
			g.Conversions += x.Conversions
			g.RevenueMicros += x.RevenueMicros
			g.Currency = x.Currency
			g.Page = x.Page
			googlePages[key] = g
		}

	}
	if out.Source != nil {
		search, e := s.insightRows(ctx, ws, site.Host, out.Source, win)
		if e != nil {
			return e
		}
		clicks := map[string]float64{}
		for _, x := range search {
			clicks[insightPath(x.Page)] += x.Clicks
		}
		for _, x := range search {
			path := insightPath(x.Page)
			if clicks[path] <= 0 {
				continue
			}
			for _, p := range googlePages {
				if p.Page != path {
					continue
				}
				v := x
				share := x.Clicks / clicks[path]
				v.Key = x.Key + " · " + p.Currency
				v.Sessions = p.Sessions * share
				v.Conversions = p.Conversions * share
				v.RevenueMicros = int64(math.Round(float64(p.RevenueMicros) * share))
				v.Share = share
				v.Currency = p.Currency
				v.Status = "estimated_query_allocation"
				out.Data = append(out.Data, v)
			}
		}
	}
	for _, x := range pages {
		out.Data = append(out.Data, x)
	}
	sort.Slice(out.Data, func(i, j int) bool {
		if out.Data[i].Sessions == out.Data[j].Sessions {
			return out.Data[i].Key < out.Data[j].Key
		}
		return out.Data[i].Sessions > out.Data[j].Sessions
	})
	out.Notes = append(out.Notes, "Landing pages are measured in GA4. Query outcomes are estimates allocated from Google organic outcomes by each query's share of visible GSC clicks, not observed user-level attribution. Do not add page and query rows together. Revenue is an integer number of millionths in the accompanying currency.")
	return nil
}
func insightPath(raw string) string {
	u, e := url.Parse(raw)
	if e != nil {
		return raw
	}
	return u.EscapedPath()
}

func assistantSource(source string) bool {
	host := strings.ToLower(strings.TrimSpace(strings.SplitN(source, " / ", 2)[0]))
	if u, e := url.Parse(host); e == nil && u.Hostname() != "" {
		host = u.Hostname()
	}
	for _, domain := range []string{"chatgpt.com", "chat.openai.com", "perplexity.ai", "claude.ai", "gemini.google.com", "copilot.microsoft.com", "you.com", "poe.com", "grok.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}
