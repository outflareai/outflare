// Package analytics connects web analytics, product analytics and search
// consoles. Each provider maps its own reports onto the five shared ones in
// connectors (traffic, pages, sources, search queries, events), so a person
// or an agent reads every tool the same way. Nothing here writes to the
// provider.
//
// Request shapes follow each provider's public API reference. Base URLs can
// be pointed at a fake in tests through connectors.Client.BaseURLs.
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	c "github.com/outflareai/outflare/engine/internal/connectors"
)

// All returns every available analytics connector.
func All() []c.Connector {
	return []c.Connector{
		GA4{}, SearchConsole{}, Bing{}, PostHog{}, Plausible{}, Fathom{}, Matomo{}, Umami{},
		Mixpanel{}, Amplitude{}, Cloudflare{}, SimpleAnalytics{}, Clarity{}, Vercel{},
	}
}

// Planned lists analytics tools people asked for that have no public read
// API yet, so the catalog can say why they cannot be connected.
func Planned() []c.Definition {
	p := func(id, name, site, summary string) c.Definition {
		return c.Definition{ID: id, Name: name, Category: c.CategoryAnalytics, Auth: c.AuthAPIKey, Website: site, Summary: summary, Capabilities: []string{}}
	}
	return []c.Definition{
		p("hotjar", "Hotjar", "https://www.hotjar.com", "Hotjar's API covers surveys and user lookups, not traffic, so there is nothing to report yet."),
		p("segment", "Segment", "https://segment.com", "Segment forwards events but does not count visits. Planned as a source of events, not a report."),
	}
}

func def(id, name, site, docs, keyURL, summary, description, pricing string, fields []c.Field) c.Definition {
	return c.Definition{
		ID: id, Name: name, Category: c.CategoryAnalytics, Website: site, DocsURL: docs, KeyURL: keyURL,
		Summary: summary, Description: description, Auth: c.AuthAPIKey, Fields: fields,
		Capabilities: []string{c.CapAnalytics}, Pricing: pricing,
	}
}

// Google scopes for the analytics connectors. Both connectors ask for both,
// so one Google sign-in connects Analytics and Search Console together.
const (
	scopeGA  = "https://www.googleapis.com/auth/analytics.readonly"
	scopeGSC = "https://www.googleapis.com/auth/webmasters.readonly"
)

func googleOAuth() *c.OAuth {
	return &c.OAuth{
		AuthURL:  "https://accounts.google.com/o/oauth2/v2/auth",
		TokenURL: "https://oauth2.googleapis.com/token",
		Scopes:   []string{scopeGA, scopeGSC, "openid", "email"},
		Params:   map[string]string{"access_type": "offline", "prompt": "consent"},
	}
}

// ---- HTTP ----

type call struct {
	cl     *c.Client
	name   string
	header http.Header
}

func newCall(cl *c.Client, name string) *call {
	return &call{cl: cl, name: name, header: http.Header{}}
}

func (k *call) bearer(token string) *call {
	k.header.Set("Authorization", "Bearer "+token)
	return k
}

func (k *call) basic(user, pass string) *call {
	req, _ := http.NewRequest(http.MethodGet, "http://x", nil)
	req.SetBasicAuth(user, pass)
	k.header.Set("Authorization", req.Header.Get("Authorization"))
	return k
}

func (k *call) set(key, value string) *call {
	k.header.Set(key, value)
	return k
}

func (k *call) do(ctx context.Context, method, u string, body, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		switch b := body.(type) {
		case url.Values:
			rd = strings.NewReader(b.Encode())
		default:
			raw, _ := json.Marshal(b)
			rd = bytes.NewReader(raw)
		}
	}
	req, err := http.NewRequest(method, u, rd)
	if err != nil {
		return 0, c.Errf(c.ErrInvalid, "bad %s address: %v", k.name, err)
	}
	for h, v := range k.header {
		req.Header[h] = v
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		if _, ok := body.(url.Values); ok {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		} else {
			req.Header.Set("Content-Type", "application/json")
		}
	}
	return c.DoJSON(ctx, k.cl, k.name, req, out)
}

func (k *call) get(ctx context.Context, u string, out any) error {
	_, err := k.do(ctx, http.MethodGet, u, nil, out)
	return err
}

func (k *call) post(ctx context.Context, u string, body, out any) error {
	_, err := k.do(ctx, http.MethodPost, u, body, out)
	return err
}

// hostURL reads an optional self-hosted address, falling back to the
// provider's cloud.
func hostURL(cl *c.Client, id, raw, cloud string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return cl.Base(id, cloud), nil
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", c.Errf(c.ErrInvalid, "the address should look like https://analytics.example.com")
	}
	return cl.Base(id, raw), nil
}

// ---- Values ----

// num reads a provider number that may come as a JSON number or a string,
// with or without a percent sign.
func num(v any) *float64 {
	switch x := v.(type) {
	case float64:
		return c.Num(x)
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil
		}
		return c.Num(f)
	case int:
		return c.Num(float64(x))
	case int64:
		return c.Num(float64(x))
	case string:
		s := strings.TrimSpace(x)
		pct := strings.HasSuffix(s, "%")
		s = strings.TrimSuffix(s, "%")
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return nil
		}
		if pct {
			f /= 100
		}
		return c.Num(f)
	}
	return nil
}

func str(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case json.Number:
		return x.String()
	}
	return fmt.Sprint(v)
}

func ratio(a, b *float64) *float64 {
	if a == nil || b == nil || *b == 0 {
		return nil
	}
	return c.Num(*a / *b)
}

func pct(v *float64) *float64 {
	if v == nil {
		return nil
	}
	return c.Num(*v / 100)
}

// dayAfter returns the day after a YYYY-MM-DD date.
func dayAfter(d string) string {
	t, _ := time.Parse(time.DateOnly, d)
	return t.AddDate(0, 0, 1).Format(time.DateOnly)
}

// millis is the start of a day in Unix milliseconds, UTC.
func millis(d string) int64 {
	t, _ := time.Parse(time.DateOnly, d)
	return t.UnixMilli()
}

// compact turns 2026-10-24 into 20261024 and back.
func compact(d string) string { return strings.ReplaceAll(d, "-", "") }

func expand(d string) string {
	if len(d) == 8 {
		return d[:4] + "-" + d[4:6] + "-" + d[6:]
	}
	if len(d) >= 10 {
		return d[:10]
	}
	return d
}

// fillDays returns one point per day of q. Days the provider skipped
// count as zero for the counts it reports on other days.
func fillDays(q c.AnalyticsQuery, got map[string]c.Metrics) []c.AnalyticsPoint {
	var zero c.Metrics
	for _, m := range got {
		zero = zero.Add(zeroed(m))
	}
	out := []c.AnalyticsPoint{}
	for _, d := range q.Days() {
		m, ok := got[d]
		if !ok {
			m = zero
		}
		out = append(out, c.AnalyticsPoint{Date: d, Metrics: m})
	}
	return out
}

// zeroed keeps which counts m has, set to zero.
func zeroed(m c.Metrics) c.Metrics {
	z := func(v *float64) *float64 {
		if v == nil {
			return nil
		}
		return c.Num(0)
	}
	return c.Metrics{Visitors: z(m.Visitors), Pageviews: z(m.Pageviews), Sessions: z(m.Sessions), Clicks: z(m.Clicks),
		Impressions: z(m.Impressions), Events: z(m.Events), Conversions: z(m.Conversions)}
}

// sortRows orders rows by the first metric present, biggest first, and
// keeps at most limit.
func sortRows(rows []c.AnalyticsRow, limit int, by func(c.Metrics) *float64) []c.AnalyticsRow {
	val := func(m c.Metrics) float64 {
		if v := by(m); v != nil {
			return *v
		}
		return -1
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if a, b := val(rows[i].Metrics), val(rows[j].Metrics); a != b {
			return a > b
		}
		return rows[i].Key < rows[j].Key
	})
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	if rows == nil {
		rows = []c.AnalyticsRow{}
	}
	return rows
}

func byPageviews(m c.Metrics) *float64 { return first(m.Pageviews, m.Sessions, m.Visitors, m.Events) }
func bySessions(m c.Metrics) *float64  { return first(m.Sessions, m.Visitors, m.Pageviews, m.Events) }
func byClicks(m c.Metrics) *float64    { return first(m.Clicks, m.Impressions) }
func byEvents(m c.Metrics) *float64    { return first(m.Events, m.Conversions, m.Visitors) }

func first(vs ...*float64) *float64 {
	for _, v := range vs {
		if v != nil {
			return v
		}
	}
	return nil
}

// sumRows totals count metrics over rows.
func sumRows(rows []c.AnalyticsRow) c.Metrics {
	var m c.Metrics
	for _, r := range rows {
		m = m.Add(r.Metrics)
	}
	return m
}

func sumPoints(ps []c.AnalyticsPoint) c.Metrics {
	var m c.Metrics
	for _, p := range ps {
		m = m.Add(p.Metrics)
	}
	return m
}

// search fills in click-through rate on search metrics.
func search(m c.Metrics) c.Metrics {
	if m.CTR == nil {
		m.CTR = ratio(m.Clicks, m.Impressions)
	}
	return m
}

var (
	safeID   = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)
	digits   = regexp.MustCompile(`^[0-9]{1,20}$`)
	hostname = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
)

func checkSite(name, site string, re *regexp.Regexp) error {
	if !re.MatchString(site) {
		return c.Errf(c.ErrInvalid, "%q is not a %s site id; list the sites to pick one", site, name)
	}
	return nil
}

func single(id, name string) []c.AnalyticsSite {
	return []c.AnalyticsSite{{ID: id, Name: name}}
}
