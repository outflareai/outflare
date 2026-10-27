package connectors

import (
	"context"
	"fmt"
	"time"
)

// Web analytics: connectors that read how a website is doing (visitors,
// pages, where people came from, what they searched for, which events and
// conversions happened). Every provider answers the same five reports in
// the same shape, so a person or an agent asks "how is the site doing"
// without caring which tool sits behind it. They only read.

// CategoryAnalytics groups web and product analytics and search consoles.
const CategoryAnalytics = "analytics"

// CapAnalytics marks a connector that implements AnalyticsReader.
const CapAnalytics = "analytics.read"

// CapBotLogs marks a connector that implements BotLogReader.
const CapBotLogs = "bot_logs.read"

// Reports an AnalyticsReader can answer.
const (
	ReportTraffic       = "traffic"        // totals plus one point per day
	ReportPages         = "pages"          // top pages by views
	ReportSources       = "sources"        // top referrers and sources
	ReportSearchQueries = "search_queries" // what people searched for before clicking through
	ReportEvents        = "events"         // events and conversions by name
)

// AllReports lists every report, in display order.
var AllReports = []string{ReportTraffic, ReportPages, ReportSources, ReportSearchQueries, ReportEvents}

// AnalyticsSite is one property, site or project at the provider.
type AnalyticsSite struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url,omitempty"`
}

// AnalyticsQuery asks for one report. From and To are inclusive dates
// (YYYY-MM-DD, UTC unless the provider's own reporting time zone applies).
type AnalyticsQuery struct {
	Report string `json:"report"`
	Site   string `json:"site"`
	From   string `json:"from"`
	To     string `json:"to"`
	// Limit caps the rows of a breakdown report.
	Limit int `json:"limit"`
}

// Days returns the query's dates, From first.
func (q AnalyticsQuery) Days() []string {
	from, err1 := time.Parse(time.DateOnly, q.From)
	to, err2 := time.Parse(time.DateOnly, q.To)
	if err1 != nil || err2 != nil || to.Before(from) {
		return nil
	}
	var out []string
	for d := from; !d.After(to) && len(out) < 3700; d = d.AddDate(0, 0, 1) {
		out = append(out, d.Format(time.DateOnly))
	}
	return out
}

// Metrics are the numbers a report row can carry. A nil field means the
// provider does not measure it, which is different from zero.
type Metrics struct {
	Visitors  *float64 `json:"visitors,omitempty"`  // unique visitors or users
	Pageviews *float64 `json:"pageviews,omitempty"` // page or screen views
	Sessions  *float64 `json:"sessions,omitempty"`  // visits
	// BounceRate is a fraction from 0 to 1.
	BounceRate *float64 `json:"bounce_rate,omitempty"`
	// AvgDuration is the average visit length in seconds.
	AvgDuration *float64 `json:"avg_duration,omitempty"`
	// Search: clicks and impressions in search results, the click-through
	// rate (0 to 1) and the average position (1 is the top).
	Clicks      *float64 `json:"clicks,omitempty"`
	Impressions *float64 `json:"impressions,omitempty"`
	CTR         *float64 `json:"ctr,omitempty"`
	Position    *float64 `json:"position,omitempty"`
	// Events counts events, Conversions those that count as conversions
	// (GA4 key events, goals).
	Events      *float64 `json:"events,omitempty"`
	Conversions *float64 `json:"conversions,omitempty"`
}

// Num makes a metric value.
func Num(v float64) *float64 { return &v }

// Add sums m and n where both are counts; rates and averages are left to
// the caller.
func (m Metrics) Add(n Metrics) Metrics {
	add := func(a, b *float64) *float64 {
		switch {
		case a == nil && b == nil:
			return nil
		case a == nil:
			return Num(*b)
		case b == nil:
			return Num(*a)
		}
		return Num(*a + *b)
	}
	m.Visitors, m.Pageviews, m.Sessions = add(m.Visitors, n.Visitors), add(m.Pageviews, n.Pageviews), add(m.Sessions, n.Sessions)
	m.Clicks, m.Impressions = add(m.Clicks, n.Clicks), add(m.Impressions, n.Impressions)
	m.Events, m.Conversions = add(m.Events, n.Events), add(m.Conversions, n.Conversions)
	return m
}

// AnalyticsRow is one line of a breakdown: a page path, a source, a search
// query or an event name.
type AnalyticsRow struct {
	Key string `json:"key"`
	Metrics
}

// AnalyticsPoint is one day of the traffic report.
type AnalyticsPoint struct {
	Date string `json:"date"`
	Metrics
}

// AnalyticsReport is a provider's answer, in provider-neutral terms.
type AnalyticsReport struct {
	Report string `json:"report"`
	Site   string `json:"site"`
	From   string `json:"from"`
	To     string `json:"to"`
	// Totals cover the whole site over the range. Breakdown reports leave
	// them empty when the provider does not give a total.
	Totals Metrics          `json:"totals"`
	Series []AnalyticsPoint `json:"series,omitempty"`
	Rows   []AnalyticsRow   `json:"rows,omitempty"`
	// Notes explain caveats in words, e.g. "Search Console data is two to
	// three days behind".
	Notes []string `json:"notes"`
}

// BotHits is how many requests one crawler, by its user agent, made to one
// host on one day, from a CDN's logs.
type BotHits struct {
	Host      string `json:"host"`
	Day       string `json:"day"` // YYYY-MM-DD, UTC
	UserAgent string `json:"user_agent"`
	Requests  int64  `json:"requests"`
}

// BotLogReader reads requests by named crawlers from a CDN's logs, such
// as Cloudflare's HTTP request analytics. Agents are user-agent tokens
// (GPTBot); only requests whose user agent contains one come back.
type BotLogReader interface {
	BotHits(ctx context.Context, c *Client, creds Creds, agents []string, day string) ([]BotHits, error)
}

// AnalyticsReader reads a website's analytics. It must not change anything
// at the provider.
type AnalyticsReader interface {
	Connector
	// Reports returns the reports the provider can answer.
	Reports() []string
	// Sites lists the properties, sites or projects the credentials can read.
	Sites(ctx context.Context, c *Client, creds Creds) ([]AnalyticsSite, error)
	// Report answers one query. The caller has checked the report is in
	// Reports, the dates are valid and Site is set.
	Report(ctx context.Context, c *Client, creds Creds, q AnalyticsQuery) (AnalyticsReport, error)
}

// Supports reports whether r answers report.
func Supports(r AnalyticsReader, report string) bool {
	for _, x := range r.Reports() {
		if x == report {
			return true
		}
	}
	return false
}

// CheckAnalyticsQuery validates the dates and fills in the defaults: the
// last 28 days and 20 rows.
func CheckAnalyticsQuery(q AnalyticsQuery, now time.Time) (AnalyticsQuery, error) {
	if q.To == "" {
		q.To = now.UTC().Format(time.DateOnly)
	}
	to, err := time.Parse(time.DateOnly, q.To)
	if err != nil {
		return q, Errf(ErrInvalid, "to should be a date like 2026-10-24")
	}
	if q.From == "" {
		q.From = to.AddDate(0, 0, -27).Format(time.DateOnly)
	}
	from, err := time.Parse(time.DateOnly, q.From)
	if err != nil {
		return q, Errf(ErrInvalid, "from should be a date like 2026-09-27")
	}
	if to.Before(from) {
		return q, Errf(ErrInvalid, "from must be on or before to")
	}
	if to.Sub(from) > 5*366*24*time.Hour {
		return q, Errf(ErrInvalid, "ask for at most five years at a time")
	}
	switch {
	case q.Limit <= 0:
		q.Limit = 20
	case q.Limit > 1000:
		q.Limit = 1000
	}
	return q, nil
}

// NotSupported is the error for a report a provider cannot answer.
func NotSupported(name, report string) error {
	return Errf(ErrInvalid, "%s has no %s report", name, report)
}

// ParseNum reads a number a provider sent as a string; empty is nil.
func ParseNum(s string) *float64 {
	var v float64
	if s == "" {
		return nil
	}
	if _, err := fmt.Sscan(s, &v); err != nil {
		return nil
	}
	return &v
}

// AnalyticsCacher is an AnalyticsReader that wants its answers kept longer
// than the default, because the provider allows few calls.
type AnalyticsCacher interface {
	CacheFor() time.Duration
}
