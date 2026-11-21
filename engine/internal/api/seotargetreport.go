package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/seointel"
)

// The target report is the one place to see what every page of a brand
// is meant to rank for and how it does: each page's target keyword and
// its secondary ones, with their Search Console position and clicks on
// that page over the 28 days to the newest day stored, the change from
// the 28 days before and the 7 days before, a weekly position line, the
// tracked rank by country from the rank tracker, the other pages of the
// site that Google shows for the same keyword, and whether the keyword is
// in the page's title, H1 and description. It reads only what is stored,
// so it calls and costs nothing.

type targetWindow struct {
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	Position    *float64 `json:"position"`
}

type targetKeywordReport struct {
	Keyword string `json:"keyword"`
	// The 28 days to the newest Search Console day, the 28 before, the
	// last 7 and the 7 before.
	Current  targetWindow `json:"current"`
	Previous targetWindow `json:"previous"`
	Week     targetWindow `json:"week"`
	LastWeek targetWindow `json:"last_week"`
	// Impression-weighted position by week, oldest first; null for a week
	// with no impressions.
	Weekly []*float64 `json:"weekly"`
}

type targetTrackedRank struct {
	Country          string  `json:"country"`
	Device           string  `json:"device"`
	Position         *int    `json:"position"`
	PreviousPosition *int    `json:"previous_position"`
	URL              string  `json:"url"`
	Day              *string `json:"day"`
	PreviousDay      *string `json:"previous_day"`
	// The ranking URL is the target page (www. and a trailing slash aside).
	OnTarget bool `json:"on_target"`
}

type targetCompetingPage struct {
	URL         string   `json:"url"`
	Clicks      float64  `json:"clicks"`
	Impressions float64  `json:"impressions"`
	Position    *float64 `json:"position"`
}

type targetReportRow struct {
	ID        string                `json:"id"`
	SiteID    string                `json:"site_id"`
	Host      string                `json:"host"`
	URL       string                `json:"url"`
	Primary   targetKeywordReport   `json:"primary"`
	Secondary []targetKeywordReport `json:"secondary"`
	Tracked   []targetTrackedRank   `json:"tracked"`
	// Other pages of the site with impressions for the primary keyword in
	// the 28 days, most impressions first.
	Competing []targetCompetingPage `json:"competing"`
	Checks    seointel.Checks       `json:"checks"`
	Intent    string                `json:"intent"`
	PageType  string                `json:"page_type"`
	// The summary itself comes with one target.
	SummaryLength int `json:"summary_length"`
	// on_target, cannibalized, wrong_intent, missing_content or no_data:
	// the first that applies, in that order after no_data.
	Status string `json:"status"`
	// The page that ranks for the keyword instead, when cannibalized.
	Owner       *string            `json:"owner"`
	Suggestions []targetSuggestion `json:"suggestions"`
	// The band of the best position known: the newest tracked rank, else
	// Search Console's. top3, top10, striking (11 to 20), beyond, or none.
	Band string `json:"band"`
	// improved, declined or steady: Search Console's 28-day position
	// against the 28 days before, a change of 1 or more; unknown without
	// both.
	Trend     string    `json:"trend"`
	UpdatedAt time.Time `json:"updated_at"`
}

type targetSuggestion struct {
	// title, h1, description, internal_link, retarget, intent, cover,
	// summary or striking.
	Kind string `json:"kind"`
	Text string `json:"text"`
	// The other page it is about, when there is one.
	URL string `json:"url,omitempty"`
}

type targetReportSite struct {
	ID     string           `json:"id"`
	Host   string           `json:"host"`
	Source *seointel.Source `json:"source"`
}

const (
	targetReportWeeks = 12
	// A competing page needs this many impressions in 28 days.
	competingFloor = 10
)

func (s *Server) seoTargetReport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	rows, err := s.pool.Query(ctx, `SELECT id::text, host FROM seo_sites WHERE workspace_id = $1 AND brand_id = $2 ORDER BY host`, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	var sites []targetReportSite
	for rows.Next() {
		var st targetReportSite
		if err := rows.Scan(&st.ID, &st.Host); err != nil {
			rows.Close()
			s.internal(w, r, err)
			return
		}
		sites = append(sites, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internal(w, r, err)
		return
	}

	out := []targetReportRow{}
	for i := range sites {
		src, err := seointel.SourceForBrand(ctx, s.pool, ws, brand, sites[i].Host)
		if err != nil {
			s.internal(w, r, err)
			return
		}
		sites[i].Source = src
		got, err := s.siteTargetReport(ctx, ws, brand, sites[i])
		if err != nil {
			s.internal(w, r, err)
			return
		}
		out = append(out, got...)
	}
	if sites == nil {
		sites = []targetReportSite{}
	}

	summary := map[string]int{"targets": len(out), "top3": 0, "top10": 0, "striking": 0, "beyond": 0, "none": 0,
		"improved": 0, "declined": 0, "competing": 0, "missing_on_page": 0, "untracked": 0,
		"on_target": 0, "cannibalized": 0, "wrong_intent": 0, "missing_content": 0, "no_data": 0, "no_summary": 0}
	for _, t := range out {
		summary[t.Band]++
		if t.Trend == "improved" || t.Trend == "declined" {
			summary[t.Trend]++
		}
		if len(t.Competing) > 0 {
			summary["competing"]++
		}
		if c := t.Checks; c.InTitle != nil && (!*c.InTitle || !*c.InH1) {
			summary["missing_on_page"]++
		}
		if len(t.Tracked) == 0 {
			summary["untracked"]++
		}
		summary[t.Status]++
		if t.SummaryLength == 0 {
			summary["no_summary"]++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"sites":   sites,
		"targets": out,
		"summary": summary,
		"notes": []string{
			"Search Console numbers are for the target page and the exact keyword: clicks, impressions and impression-weighted position over the 28 days to the newest day stored, against the 28 days before.",
			"Tracked ranks come from the rank tracker, for the keywords it tracks, by country and device; Track every target keyword on Keywords, Rankings adds the missing ones.",
			"A competing page is another page of the site with at least 10 impressions for the target keyword in the same 28 days.",
			"The band uses the newest tracked rank when there is one, else Search Console's position. Striking distance is 11 to 20.",
			"Status: cannibalized when another page of the site gets more impressions for the keyword or the rank tracker finds another page; wrong intent when the page type rarely answers the intent (informational on a product or pricing page, transactional on a blog or docs page, navigational on a blog or comparison page); missing content when the keyword is not in the title or H1, or the summary leaves out a secondary keyword.",
		},
	})
}

// siteTargetReport reads one site's set targets with their numbers.
func (s *Server) siteTargetReport(ctx context.Context, ws, brand string, site targetReportSite) ([]targetReportRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, url, keyword, secondary, updated_at, intent, page_type, summary FROM page_targets
		WHERE workspace_id = $1 AND site_id = $2 AND status = 'set' AND keyword <> ''
		ORDER BY url LIMIT $3`, ws, site.ID, maxTargets)
	if err != nil {
		return nil, err
	}
	var out []targetReportRow
	summaries := map[string]string{}
	for rows.Next() {
		var t targetReportRow
		var secondary []string
		var summary string
		if err := rows.Scan(&t.ID, &t.URL, &t.Primary.Keyword, &secondary, &t.UpdatedAt, &t.Intent, &t.PageType, &summary); err != nil {
			rows.Close()
			return nil, err
		}
		t.SiteID, t.Host = site.ID, site.Host
		t.Secondary = []targetKeywordReport{}
		for _, k := range secondary {
			if k = strings.TrimSpace(k); k != "" && k != t.Primary.Keyword {
				t.Secondary = append(t.Secondary, targetKeywordReport{Keyword: k})
			}
		}
		t.Tracked, t.Competing = []targetTrackedRank{}, []targetCompetingPage{}
		t.SummaryLength = len(summary)
		summaries[t.ID] = summary
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return out, err
	}

	if err := s.fillTargetSearch(ctx, ws, brand, site.Source, out); err != nil {
		return nil, err
	}
	if err := s.targetTracked(ctx, ws, site.ID, out); err != nil {
		return nil, err
	}

	urls := make([]string, len(out))
	for i := range out {
		urls[i] = out[i].URL
	}
	crawlID, err := seointel.LatestCrawl(ctx, s.pool, ws, site.ID)
	if err != nil {
		return nil, err
	}
	pages, err := seointel.CrawledPages(ctx, s.pool, ws, crawlID, urls)
	if err != nil {
		return nil, err
	}
	for i := range out {
		t := &out[i]
		t.Checks = seointel.Check(pages[t.URL], t.Primary.Keyword)
		t.Band = rankBandOf(t)
		t.Trend = targetTrend(t.Primary)
		judgeTarget(t, summaries[t.ID])
	}
	return out, nil
}

// fillTargetSearch fills each target's Search Console numbers, for its
// primary and secondary keywords on its page by week over 12 weeks, and
// the other pages shown for its primary keyword.
func (s *Server) fillTargetSearch(ctx context.Context, ws, brand string, src *seointel.Source, out []targetReportRow) error {
	for i := range out {
		out[i].Primary.Weekly = make([]*float64, targetReportWeeks)
		for j := range out[i].Secondary {
			out[i].Secondary[j].Weekly = make([]*float64, targetReportWeeks)
		}
	}
	if src == nil || src.NewestDay == nil {
		return nil
	}
	type pair struct{ page, kw string }
	at := map[pair][]*targetKeywordReport{}
	var pages, kws, primaries []string
	add := func(page string, k *targetKeywordReport) {
		p := pair{page, k.Keyword}
		if at[p] == nil {
			pages, kws = append(pages, page), append(kws, k.Keyword)
		}
		at[p] = append(at[p], k)
	}
	for i := range out {
		add(out[i].URL, &out[i].Primary)
		for j := range out[i].Secondary {
			add(out[i].URL, &out[i].Secondary[j])
		}
		primaries = append(primaries, out[i].Primary.Keyword)
	}

	type acc struct{ c, i, pw float64 }
	weeks := map[pair][]acc{}
	rows, err := s.pool.Query(ctx, `
		WITH t AS (SELECT * FROM unnest($5::text[], $6::text[]) AS t(page, kw))
		SELECT d.page, d.query, ($4::date - d.day) / 7 AS wk,
			sum(d.clicks)::float8, sum(d.impressions)::float8, sum(d.position * d.impressions)::float8
		FROM search_daily d JOIN t ON d.page = t.page AND d.query = t.kw
		WHERE d.workspace_id = $1 AND d.brand_id = $7 AND d.integration_id = $2::uuid AND d.site = $3
		  AND d.day > $4::date - $8::int * 7 AND d.day <= $4::date
		GROUP BY 1, 2, 3`, ws, src.IntegrationID, src.Property, *src.NewestDay, pages, kws, brand, targetReportWeeks)
	if err != nil {
		return err
	}
	for rows.Next() {
		var page, kw string
		var wk int
		var a acc
		if err := rows.Scan(&page, &kw, &wk, &a.c, &a.i, &a.pw); err != nil {
			rows.Close()
			return err
		}
		if wk < 0 || wk >= targetReportWeeks {
			continue
		}
		p := pair{page, kw}
		if weeks[p] == nil {
			weeks[p] = make([]acc, targetReportWeeks)
		}
		weeks[p][wk] = a
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	window := func(wk []acc, from, to int) targetWindow {
		var a acc
		for k := from; k <= to && k < len(wk); k++ {
			a.c, a.i, a.pw = a.c+wk[k].c, a.i+wk[k].i, a.pw+wk[k].pw
		}
		w := targetWindow{Clicks: a.c, Impressions: a.i}
		if a.i > 0 {
			p := a.pw / a.i
			w.Position = &p
		}
		return w
	}
	for p, ks := range at {
		got := weeks[p]
		if got == nil {
			continue
		}
		for _, k := range ks {
			k.Current, k.Previous = window(got, 0, 3), window(got, 4, 7)
			k.Week, k.LastWeek = window(got, 0, 0), window(got, 1, 1)
			for wk := 0; wk < targetReportWeeks; wk++ {
				// Oldest first.
				k.Weekly[targetReportWeeks-1-wk] = window(got, wk, wk).Position
			}
		}
	}

	// Other pages shown for each primary keyword.
	rows, err = s.pool.Query(ctx, `
		SELECT d.query, d.page, sum(d.clicks)::float8, sum(d.impressions)::float8,
			sum(d.position * d.impressions)::float8 / NULLIF(sum(d.impressions), 0)
		FROM search_daily d
		WHERE d.workspace_id = $1 AND d.brand_id = $6 AND d.integration_id = $2::uuid AND d.site = $3
		  AND d.query = ANY($5::text[]) AND d.day > $4::date - 28 AND d.day <= $4::date
		GROUP BY 1, 2 HAVING sum(d.impressions) >= $7
		ORDER BY 4 DESC`, ws, src.IntegrationID, src.Property, *src.NewestDay, primaries, brand, competingFloor)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kw string
		var c targetCompetingPage
		if err := rows.Scan(&kw, &c.URL, &c.Clicks, &c.Impressions, &c.Position); err != nil {
			return err
		}
		for i := range out {
			if out[i].Primary.Keyword == kw && !samePage(out[i].URL, c.URL) {
				out[i].Competing = append(out[i].Competing, c)
			}
		}
	}
	return rows.Err()
}

// targetTracked fills each target's newest and previous tracked rank for
// its primary keyword, one per country and device the tracker checks.
func (s *Server) targetTracked(ctx context.Context, ws, site string, out []targetReportRow) error {
	kws := make([]string, len(out))
	for i := range out {
		kws[i] = out[i].Primary.Keyword
	}
	rows, err := s.pool.Query(ctx, `
		SELECT k.keyword, k.country, k.device, o.position, o.url, to_char(o.day, 'YYYY-MM-DD'), p.position, to_char(p.day, 'YYYY-MM-DD')
		FROM tracked_keywords k
		LEFT JOIN LATERAL (
			SELECT position, url, day FROM rank_observations WHERE workspace_id = $1 AND tracked_id = k.id ORDER BY day DESC LIMIT 1
		) o ON true
		LEFT JOIN LATERAL (
			SELECT position, day FROM rank_observations WHERE workspace_id = $1 AND tracked_id = k.id AND day < o.day ORDER BY day DESC LIMIT 1
		) p ON true
		WHERE k.workspace_id = $1 AND k.site_id = $2 AND k.keyword = ANY($3::text[]) AND k.status = 'active'
		ORDER BY k.keyword, k.country, k.device`, ws, site, kws)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kw string
		var t targetTrackedRank
		var url *string
		if err := rows.Scan(&kw, &t.Country, &t.Device, &t.Position, &url, &t.Day, &t.PreviousPosition, &t.PreviousDay); err != nil {
			return err
		}
		if url != nil {
			t.URL = *url
		}
		for i := range out {
			if out[i].Primary.Keyword == kw {
				r := t
				r.OnTarget = t.URL != "" && samePage(out[i].URL, t.URL)
				out[i].Tracked = append(out[i].Tracked, r)
			}
		}
	}
	return rows.Err()
}

// samePage compares two URLs without the scheme, www., a trailing slash
// and a fragment.
func samePage(a, b string) bool {
	norm := func(u string) string {
		u = strings.ToLower(u)
		if i := strings.IndexByte(u, '#'); i >= 0 {
			u = u[:i]
		}
		u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
		u = strings.TrimPrefix(u, "www.")
		return strings.TrimSuffix(u, "/")
	}
	return norm(a) == norm(b)
}

func rankBandOf(t *targetReportRow) string {
	var best *float64
	// The newest tracked rank, the best across countries and devices.
	var newest string
	for _, r := range t.Tracked {
		if r.Day != nil && *r.Day > newest {
			newest = *r.Day
		}
	}
	tracked := false
	for _, r := range t.Tracked {
		if r.Day == nil || *r.Day != newest {
			continue
		}
		tracked = true
		if r.Position != nil {
			p := float64(*r.Position)
			if best == nil || p < *best {
				best = &p
			}
		}
	}
	if !tracked {
		best = t.Primary.Current.Position
	}
	switch {
	case best == nil:
		return "none"
	case *best <= 3.5:
		return "top3"
	case *best <= 10.5:
		return "top10"
	case *best <= 20.5:
		return "striking"
	}
	return "beyond"
}

func targetTrend(k targetKeywordReport) string {
	c, p := k.Current.Position, k.Previous.Position
	if c == nil || p == nil {
		return "unknown"
	}
	switch d := *p - *c; {
	case d >= 1:
		return "improved"
	case d <= -1:
		return "declined"
	}
	return "steady"
}

// intentMisfits are the page types that rarely answer an intent.
var intentMisfits = map[string][]string{
	"informational": {"product", "pricing"},
	"transactional": {"blog", "docs"},
	"navigational":  {"blog", "comparison"},
}

// judgeTarget sets a target's status, owner and suggestions from what the
// report knows of it. The rules are plain so anyone can check them; the
// notes on the report say them in words.
func judgeTarget(t *targetReportRow, summary string) {
	kw := t.Primary.Keyword
	t.Suggestions = []targetSuggestion{}
	add := func(kind, text, url string) {
		t.Suggestions = append(t.Suggestions, targetSuggestion{Kind: kind, Text: text, URL: url})
	}

	// Another page of ours ranks instead.
	var owner *string
	for _, c := range t.Competing {
		if c.Impressions > t.Primary.Current.Impressions {
			u := c.URL
			owner = &u
			add("retarget", `Another page gets more impressions for "`+kw+`". Link from it to this page with "`+kw+`" as the anchor and take the keyword out of its title and H1, or make it the target instead.`, c.URL)
			break
		}
	}
	for _, c := range t.Competing {
		if owner == nil || c.URL != *owner {
			add("internal_link", `Link from this other page that shows for "`+kw+`" to the target, with "`+kw+`" as the anchor.`, c.URL)
		}
	}
	for _, r := range t.Tracked {
		if r.Position != nil && r.URL != "" && !r.OnTarget {
			if owner == nil {
				u := r.URL
				owner = &u
			}
			add("retarget", `The rank tracker finds another page for "`+kw+`" in `+r.Country+`, not this one.`, r.URL)
			break
		}
	}
	t.Owner = owner

	misfit := t.Intent != "" && t.PageType != "" && slices.Contains(intentMisfits[t.Intent], t.PageType)
	if misfit {
		add("intent", `"`+kw+`" is `+t.Intent+` and this is a `+t.PageType+` page, which rarely ranks for that intent. Target a keyword that fits the page, or move this one to a page that fits it.`, "")
	}

	missing := false
	if c := t.Checks; c.InTitle != nil {
		if !*c.InTitle {
			missing = true
			add("title", `Put "`+kw+`" in the title.`, "")
		}
		if c.InH1 != nil && !*c.InH1 {
			missing = true
			add("h1", `Put "`+kw+`" in the H1.`, "")
		}
		if c.InDescription != nil && !*c.InDescription {
			add("description", `Put "`+kw+`" in the meta description.`, "")
		}
	}
	if summary == "" {
		add("summary", "Add a summary of what the page holds, so its content can be checked against the keywords.", "")
	} else {
		for _, s := range t.Secondary {
			if !seointel.Mentions(summary, s.Keyword) {
				missing = true
				add("cover", `The summary doesn't mention "`+s.Keyword+`": add a section that covers it.`, "")
			}
		}
	}
	if t.Band == "striking" {
		add("striking", `Position 11 to 20: internal links and a link to this page with "`+kw+`" can lift it to page one.`, "")
	}

	known := t.Primary.Current.Impressions > 0 || len(t.Tracked) > 0 || t.Checks.InTitle != nil
	switch {
	case owner != nil:
		t.Status = "cannibalized"
	case misfit:
		t.Status = "wrong_intent"
	case missing:
		t.Status = "missing_content"
	case !known:
		t.Status = "no_data"
	default:
		t.Status = "on_target"
	}
}
