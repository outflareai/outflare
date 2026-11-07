package api

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/metrics"
)

// Page speed from real visits. The site's tag measures each page load in
// the visitor's browser (loading, response to clicks, layout jumps, first
// paint, server response) and sends one "vitals" message when the page is
// left. It carries no visitor id: a reading is the page, the kind of device
// and the country, nothing about who loaded it, so it needs no consent and
// joins no session or record. Readings are kept 90 days (PrunePageVitals).

func (s *Server) routesSiteVitals(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/analytics/page-speed", s.onBrand(auth.ScopeAnalyticsRead, s.pageSpeed))
}

// vitalsRetention is how long readings are kept.
const vitalsRetention = 90 * 24 * time.Hour

// siteVitals is one page load's reading, cleaned.
type siteVitals struct {
	host, path          string
	lcp, inp, fcp, ttfb *int
	cls                 *float64
}

// vitalsLimits are the most each reading may be; anything over is a
// broken measurement (a tab left in the background) and is dropped.
var vitalsLimits = map[string]float64{"lcp": 120000, "inp": 60000, "fcp": 120000, "ttfb": 120000, "cls": 100}

// isVitals says whether a browser message is a page speed reading.
func isVitals(raw json.RawMessage) bool {
	var m struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(raw, &m) == nil && strings.EqualFold(m.Type, "vitals")
}

// siteVitalsOf reads a vitals message: {"type": "vitals", "properties":
// {"lcp", "inp", "cls", "fcp", "ttfb"}, "context": {"page": {"url"}}}. The
// page's address keeps only its host and path.
func siteVitalsOf(raw json.RawMessage) (siteVitals, error) {
	var m struct {
		Properties map[string]any `json:"properties"`
		Context    struct {
			Page struct {
				URL  string `json:"url"`
				Path string `json:"path"`
			} `json:"page"`
		} `json:"context"`
	}
	var v siteVitals
	if err := json.Unmarshal(raw, &m); err != nil {
		return v, errInvalid("a vitals message is an object")
	}
	num := func(k string) (float64, bool) {
		f, ok := m.Properties[k].(float64)
		if !ok || math.IsNaN(f) || f < 0 || f > vitalsLimits[k] {
			return 0, false
		}
		return f, true
	}
	ms := func(k string) *int {
		if f, ok := num(k); ok {
			n := int(math.Round(f))
			return &n
		}
		return nil
	}
	v.lcp, v.inp, v.fcp, v.ttfb = ms("lcp"), ms("inp"), ms("fcp"), ms("ttfb")
	if f, ok := num("cls"); ok {
		f = math.Round(f*10000) / 10000
		v.cls = &f
	}
	if v.lcp == nil && v.inp == nil && v.cls == nil && v.fcp == nil && v.ttfb == nil {
		return v, errInvalid("a vitals message needs at least one reading")
	}
	if u, err := url.Parse(m.Context.Page.URL); err == nil && u.Host != "" {
		v.host = strings.ToLower(u.Hostname())
		v.path = u.EscapedPath()
	} else {
		v.path = m.Context.Page.Path
	}
	if v.path == "" {
		v.path = "/"
	}
	if !strings.HasPrefix(v.path, "/") {
		return v, errInvalid("a vitals message's page path starts with /")
	}
	v.host, v.path = cutUTF8(v.host, 253), cutUTF8(v.path, 1000)
	return v, nil
}

// storeVitals keeps one reading for the site key's brand. A key with no
// brand has nowhere to show it, so its readings are refused.
func storeVitals(ctx context.Context, tx pgx.Tx, k siteKeyRow, device string, v siteVitals, now time.Time) error {
	if k.brandID == nil || *k.brandID == "" {
		return errInvalid("this site key has no brand to keep page speed for")
	}
	country := k.country
	if len(country) != 2 {
		country = ""
	}
	_, err := tx.Exec(ctx, `INSERT INTO page_vitals (workspace_id, brand_id, key_id, host, path, device, country, lcp_ms, inp_ms, cls, fcp_ms, ttfb_ms, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		k.workspaceID, *k.brandID, k.id, v.host, v.path, cutUTF8(device, 20), strings.ToUpper(country), v.lcp, v.inp, v.cls, v.fcp, v.ttfb, now)
	return err
}

// PrunePageVitals deletes readings older than 90 days, a batch at a time.
func (s *Server) PrunePageVitals(ctx context.Context) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `DELETE FROM page_vitals WHERE id IN (
			SELECT id FROM page_vitals WHERE occurred_at < $1 LIMIT 5000)`, time.Now().Add(-vitalsRetention))
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		metrics.PageVitals("pruned", int(tag.RowsAffected()))
		if tag.RowsAffected() < 5000 {
			return total, nil
		}
	}
}

// Google's thresholds: at or under good is good, over poor is poor.
var vitalsGrades = map[string][2]float64{
	"lcp": {2500, 4000}, "inp": {200, 500}, "cls": {0.1, 0.25}, "fcp": {1800, 3000}, "ttfb": {800, 1800},
}

// vitalsColumns are the metrics in the order they are shown.
var vitalsColumns = []struct{ key, col string }{
	{"lcp", "lcp_ms"}, {"inp", "inp_ms"}, {"cls", "cls"}, {"fcp", "fcp_ms"}, {"ttfb", "ttfb_ms"},
}

// vitalsMetric is one metric over a set of page loads.
type vitalsMetric struct {
	// The 75th percentile, the value Google judges a page by; null with no
	// readings.
	P75 *float64 `json:"p75"`
	// good, needs_improvement or poor for p75; "" with no readings.
	Grade string `json:"grade"`
	// How many loads measured it, and how many of those were good, needed
	// improvement and were poor.
	Measured int `json:"measured"`
	Good     int `json:"good"`
	Needs    int `json:"needs_improvement"`
	Poor     int `json:"poor"`
}

type pageSpeedPage struct {
	Website string                  `json:"website"`
	Path    string                  `json:"path"`
	Loads   int                     `json:"loads"`
	Metrics map[string]vitalsMetric `json:"metrics"`
	// The same page's p75 loading time in the period before, for the
	// change; null when it had too few loads then.
	PreviousLCP *float64 `json:"previous_lcp"`
}

// pageSpeedSlower is a page that got slower than in the period before.
type pageSpeedSlower struct {
	Website string  `json:"website"`
	Path    string  `json:"path"`
	Metric  string  `json:"metric"`
	Before  float64 `json:"before"`
	After   float64 `json:"after"`
	Loads   int     `json:"loads"`
}

type pageSpeedReport struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Loads int    `json:"loads"`
	// Loads where every measured metric was good.
	FastShare *float64                `json:"fast_share"`
	Metrics   map[string]vitalsMetric `json:"metrics"`
	// The same over the period before, for the change.
	PreviousLoads     int                     `json:"previous_loads"`
	PreviousFastShare *float64                `json:"previous_fast_share"`
	PreviousMetrics   map[string]vitalsMetric `json:"previous_metrics"`
	// Pages, most loads first, at most 100.
	Pages  []pageSpeedPage   `json:"pages"`
	Slower []pageSpeedSlower `json:"slower"`
	// The websites seen, for the picker.
	Websites []string `json:"websites"`
}

// vitalsMinLoads is how many loads a page needs in both periods before a
// change is reported, so one slow visit doesn't raise a warning.
const vitalsMinLoads = 30

func (s *Server) pageSpeed(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	q := r.URL.Query()
	to := time.Now().UTC()
	out := pageSpeedReport{To: to.Format(time.DateOnly), From: to.AddDate(0, 0, -6).Format(time.DateOnly), Pages: []pageSpeedPage{}, Slower: []pageSpeedSlower{}, Websites: []string{}}
	for name, dst := range map[string]*string{"from": &out.From, "to": &out.To} {
		if v := q.Get(name); v != "" {
			if dateOnly(v) == "" {
				writeError(w, r, http.StatusBadRequest, "invalid_request", name+" is a date, YYYY-MM-DD")
				return
			}
			*dst = dateOnly(v)
		}
	}
	from, _ := time.Parse(time.DateOnly, out.From)
	end, _ := time.Parse(time.DateOnly, out.To)
	if from.After(end) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "from is after to")
		return
	}
	if end.Sub(from) > 90*24*time.Hour {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "page speed is kept 90 days: the range is at most 90 days")
		return
	}
	end = end.AddDate(0, 0, 1)
	device := strings.ToLower(q.Get("device"))
	if device != "" && device != "desktop" && device != "mobile" && device != "tablet" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "device is desktop, mobile or tablet")
		return
	}
	website := strings.ToLower(strings.TrimSpace(q.Get("website")))
	ctx := r.Context()
	prevFrom := from.Add(-end.Sub(from))
	cur, err := s.vitalsGroups(ctx, a.WorkspaceID, a.Brand, from, end, website, device)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	prev, err := s.vitalsGroups(ctx, a.WorkspaceID, a.Brand, prevFrom, from, website, device)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out.Loads, out.FastShare, out.Metrics = cur.total.loads, cur.total.fastShare(), cur.total.metrics
	out.PreviousLoads, out.PreviousFastShare, out.PreviousMetrics = prev.total.loads, prev.total.fastShare(), prev.total.metrics
	before := map[string]vitalsGroup{}
	for _, g := range prev.pages {
		before[g.host+" "+g.path] = g
	}
	for _, g := range cur.pages {
		p := pageSpeedPage{Website: g.host, Path: g.path, Loads: g.loads, Metrics: g.metrics}
		if b, ok := before[g.host+" "+g.path]; ok && b.loads >= vitalsMinLoads {
			p.PreviousLCP = b.metrics["lcp"].P75
		}
		if len(out.Pages) < 100 {
			out.Pages = append(out.Pages, p)
		}
		if b, ok := before[g.host+" "+g.path]; ok {
			out.Slower = append(out.Slower, slowerPage(g, b)...)
		}
	}
	sort.SliceStable(out.Slower, func(i, j int) bool {
		return out.Slower[i].Loads*int(out.Slower[i].After-out.Slower[i].Before) > out.Slower[j].Loads*int(out.Slower[j].After-out.Slower[j].Before)
	})
	if len(out.Slower) > 10 {
		out.Slower = out.Slower[:10]
	}
	if out.Websites, err = s.vitalsWebsites(ctx, a.WorkspaceID, a.Brand, prevFrom, end); err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// slowerPage says which of a page's loading and click response got
// clearly slower: past good now, worse by a real amount and a real share,
// with enough loads in both periods.
func slowerPage(cur, prev vitalsGroup) []pageSpeedSlower {
	if cur.loads < vitalsMinLoads || prev.loads < vitalsMinLoads {
		return nil
	}
	var out []pageSpeedSlower
	for _, c := range []struct {
		key        string
		least, add float64
	}{{"lcp", 500, 0.2}, {"inp", 100, 0.25}} {
		now, then := cur.metrics[c.key].P75, prev.metrics[c.key].P75
		if now == nil || then == nil || *now <= vitalsGrades[c.key][0] {
			continue
		}
		if *now-*then >= c.least && *now >= *then*(1+c.add) {
			out = append(out, pageSpeedSlower{Website: cur.host, Path: cur.path, Metric: c.key, Before: *then, After: *now, Loads: cur.loads})
		}
	}
	return out
}

// vitalsGroup is a set of loads: the whole brand, or one page.
type vitalsGroup struct {
	host, path string
	loads      int
	fast       int
	fastOf     int
	metrics    map[string]vitalsMetric
}

func (g vitalsGroup) fastShare() *float64 {
	if g.fastOf == 0 {
		return nil
	}
	f := float64(g.fast) / float64(g.fastOf)
	return &f
}

type vitalsGroups struct {
	total vitalsGroup
	pages []vitalsGroup
}

// vitalsGroups reads the brand's loads in [from, end) as a whole and per
// page, most loads first.
func (s *Server) vitalsGroups(ctx context.Context, ws, brand string, from, end time.Time, website, device string) (vitalsGroups, error) {
	var sel strings.Builder
	sel.WriteString(`count(*)`)
	// A load is fast when every metric it measured was good.
	sel.WriteString(`, count(*) FILTER (WHERE COALESCE(lcp_ms <= 2500, true) AND COALESCE(inp_ms <= 200, true) AND COALESCE(cls <= 0.1, true) AND (lcp_ms IS NOT NULL OR inp_ms IS NOT NULL OR cls IS NOT NULL))`)
	sel.WriteString(`, count(*) FILTER (WHERE lcp_ms IS NOT NULL OR inp_ms IS NOT NULL OR cls IS NOT NULL)`)
	for _, c := range vitalsColumns {
		g := vitalsGrades[c.key]
		sel.WriteString(`, percentile_cont(0.75) WITHIN GROUP (ORDER BY ` + c.col + `), count(` + c.col + `)`)
		sel.WriteString(`, count(*) FILTER (WHERE ` + c.col + ` <= ` + ftoa(g[0]) + `), count(*) FILTER (WHERE ` + c.col + ` > ` + ftoa(g[1]) + `)`)
	}
	where := `WHERE workspace_id = $1 AND brand_id = $2 AND occurred_at >= $3 AND occurred_at < $4
		AND ($5 = '' OR host = $5) AND ($6 = '' OR device = $6)`
	args := []any{ws, brand, from, end, website, device}
	scan := func(row pgx.Row, g *vitalsGroup, lead ...any) error {
		dst := append(lead, &g.loads, &g.fast, &g.fastOf)
		type m struct {
			p75                  *float64
			measured, good, poor int
		}
		ms := make([]m, len(vitalsColumns))
		for i := range ms {
			dst = append(dst, &ms[i].p75, &ms[i].measured, &ms[i].good, &ms[i].poor)
		}
		if err := row.Scan(dst...); err != nil {
			return err
		}
		g.metrics = map[string]vitalsMetric{}
		for i, c := range vitalsColumns {
			v := vitalsMetric{P75: ms[i].p75, Measured: ms[i].measured, Good: ms[i].good, Poor: ms[i].poor, Needs: ms[i].measured - ms[i].good - ms[i].poor}
			if v.P75 != nil {
				p := *v.P75
				if c.key == "cls" {
					p = math.Round(p*1000) / 1000
				} else {
					p = math.Round(p)
				}
				v.P75 = &p
				v.Grade = vitalsGrade(c.key, p)
			}
			g.metrics[c.key] = v
		}
		return nil
	}
	var out vitalsGroups
	if err := scan(s.pool.QueryRow(ctx, `SELECT `+sel.String()+` FROM page_vitals `+where, args...), &out.total); err != nil {
		return out, err
	}
	rows, err := s.pool.Query(ctx, `SELECT host, path, `+sel.String()+` FROM page_vitals `+where+`
		GROUP BY host, path ORDER BY count(*) DESC, host, path LIMIT 500`, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var g vitalsGroup
		if err := scan(rows, &g, &g.host, &g.path); err != nil {
			return out, err
		}
		out.pages = append(out.pages, g)
	}
	return out, rows.Err()
}

// vitalsWebsites are the hosts with readings, for the picker.
func (s *Server) vitalsWebsites(ctx context.Context, ws, brand string, from, end time.Time) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT host FROM page_vitals WHERE workspace_id = $1 AND brand_id = $2 AND occurred_at >= $3 AND occurred_at < $4 AND host <> ''
		GROUP BY host ORDER BY count(*) DESC LIMIT 20`, ws, brand, from, end)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func vitalsGrade(key string, v float64) string {
	g := vitalsGrades[key]
	switch {
	case v <= g[0]:
		return "good"
	case v > g[1]:
		return "poor"
	}
	return "needs_improvement"
}

func ftoa(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
