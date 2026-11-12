package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// The SEO overview answers one question per brand: is it growing in
// search? It reads only what is stored (analytics syncs, SEO snapshots,
// site audits and the fixes list), so it calls and costs nothing.
//
// Branded search is the search console queries that name the brand: its
// name, its handle or its domains' names. Consoles hide rare queries, so
// it is a floor, not a total.

// trendWeeks is how many weeks each side of a trend compares.
const trendWeeks = 4

type seoOverviewWeek struct {
	Week                  string   `json:"week"`
	BrandedClicks         *float64 `json:"branded_clicks"`
	BrandedImpressions    *float64 `json:"branded_impressions"`
	NonBrandedClicks      *float64 `json:"non_branded_clicks"`
	NonBrandedImpressions *float64 `json:"non_branded_impressions"`
	SearchClicks          *float64 `json:"search_clicks"`
	SearchImpressions     *float64 `json:"search_impressions"`
	SearchCTR             *float64 `json:"search_ctr"`
	SearchPosition        *float64 `json:"search_position"`
	Visitors              *float64 `json:"visitors"`
	Sessions              *float64 `json:"sessions"`
	SearchConversions     *float64 `json:"search_conversions"`
	KeywordsTop10         *float64 `json:"keywords_top10"`
	KeywordsTracked       *float64 `json:"keywords_tracked"`
	AIMentionRate         *float64 `json:"ai_mention_rate"`
	AIRecommendRate       *float64 `json:"ai_recommend_rate"`
}

type seoOverviewSnapshot struct {
	Day              string   `json:"day"`
	Connector        string   `json:"connector"`
	DomainRating     *float64 `json:"domain_rating"`
	Backlinks        *float64 `json:"backlinks"`
	ReferringDomains *float64 `json:"referring_domains"`
	OrganicTraffic   *float64 `json:"organic_traffic"`
	OrganicKeywords  *float64 `json:"organic_keywords"`
	Top3             *float64 `json:"top3"`
}

type seoTrend struct {
	Current   *float64 `json:"current"`
	Previous  *float64 `json:"previous"`
	Change    *float64 `json:"change"`
	Direction string   `json:"direction"`
}

type seoOverviewSite struct {
	ID          string  `json:"id"`
	Host        string  `json:"host"`
	HealthScore *int    `json:"health_score"`
	CrawledAt   *string `json:"crawled_at"`
}

type seoOverviewBrand struct {
	Brand  *seoOverviewBrandRef  `json:"brand"`
	Terms  []string              `json:"terms"`
	Hosts  []string              `json:"hosts"`
	Weeks  []seoOverviewWeek     `json:"weeks"`
	SEO    []seoOverviewSnapshot `json:"seo"`
	Trends map[string]seoTrend   `json:"trends"`
	// PreviousWeeks are as many weeks as Weeks, just before them, and
	// Period sets the whole period against them.
	PreviousWeeks []seoOverviewWeek   `json:"previous_weeks"`
	Period        map[string]seoTrend `json:"period"`
	Sites         []seoOverviewSite   `json:"sites"`
	Fixes         *int                `json:"open_fixes"`
	// SEOStatus says why the SEO snapshot numbers are there or not.
	SEOStatus seoOverviewStatus `json:"seo_status"`
	// Sources says, for search and visitors, whether the brand's websites
	// have data, or the workspace's tools read other websites only.
	Sources map[string]seoOverviewSource `json:"sources"`
}

// seoOverviewSource is where one kind of number stands for a brand: ready
// (its websites have data), other_sites (a connected tool reads websites
// of the workspace, but none of the brand's) or not_connected.
type seoOverviewSource struct {
	State string   `json:"state"`
	Hosts []string `json:"hosts"`
}

// seoOverviewStatus is where a brand's SEO snapshots stand: not_connected
// (no SEO tool connected), not_shared (connected for other brands only), no_website (nothing to read), not_followed (its
// SEO tools read other domains), off (snapshots are turned off), reading
// (the first read is under way), failed (the last read failed) or ready.
type seoOverviewStatus struct {
	State         string  `json:"state"`
	Message       string  `json:"message"`
	IntegrationID *string `json:"integration_id"`
	Connector     *string `json:"connector"`
	LastOKAt      *string `json:"last_ok_at"`
}

type seoOverviewBrandRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Handle string `json:"handle"`
}

func (s *Server) seoOverview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	ws := a.WorkspaceID
	// Whole weeks, Monday to Sunday, ending with the last one that is over,
	// or the weeks a from and to fall in.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	latest := today.AddDate(0, 0, -int((today.Weekday()+6)%7)-1)
	from, to, err := overviewRange(r.URL.Query(), latest)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	// The period before: as many weeks, ending the day before from.
	weeks := int(to.Sub(from).Hours()/24+1) / 7
	prevFrom := from.AddDate(0, 0, -7*weeks)

	brands, err := s.loadBrands(r.Context(), ws, nil, "active")
	if err != nil {
		s.internal(w, r, err)
		return
	}
	hosts, chosen, err := s.workspaceHosts(r.Context(), ws)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	owner := func(host string) string {
		if id, ok := chosen[host]; ok {
			return id
		}
		id, best := "", 0
		for _, b := range brands {
			for _, d := range b.Domains {
				if len(d) > best && (host == d || strings.HasSuffix(host, "."+d)) {
					id, best = b.ID, len(d)
				}
			}
		}
		return id
	}

	var out []*seoOverviewBrand
	for _, b := range brands {
		if !sel.matches(&b.ID) {
			continue
		}
		e := &seoOverviewBrand{Brand: &seoOverviewBrandRef{ID: b.ID, Name: b.Name, Handle: b.Handle}, Terms: brandTerms(b.Name, b.Handle, b.Domains)}
		e.Hosts = slices.Clone(b.Domains)
		for _, h := range hosts {
			if owner(h) == b.ID && !slices.Contains(e.Hosts, h) {
				e.Hosts = append(e.Hosts, h)
			}
		}
		out = append(out, e)
	}
	// Shared: the websites no brand claims. Shown alone when there are no
	// brands, and beside the brands when it has a website of its own.
	if sel.matches(nil) {
		e := &seoOverviewBrand{}
		for _, h := range hosts {
			if owner(h) == "" {
				e.Hosts = append(e.Hosts, h)
			}
		}
		if len(brands) == 0 || len(e.Hosts) > 0 {
			var name string
			if err := s.pool.QueryRow(r.Context(), `SELECT name FROM workspaces WHERE id = $1`, ws).Scan(&name); err != nil {
				s.internal(w, r, err)
				return
			}
			e.Terms = brandTerms(name, "", e.Hosts)
			out = append(out, e)
		}
	}

	seesSites := a.has(auth.ScopeSEORead)
	conns, err := s.seoOverviewConns(r.Context(), ws)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	measured, err := s.measuredHosts(r.Context(), ws, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	for _, e := range out {
		if e.Hosts == nil {
			e.Hosts = []string{}
		}
		e.Sources = map[string]seoOverviewSource{
			"search":  sourceOf(e.Hosts, measured, func(m hostMeasures) bool { return m.search }),
			"traffic": sourceOf(e.Hosts, measured, func(m hostMeasures) bool { return m.traffic }),
		}
		if err := s.fillSEOOverview(r.Context(), ws, e, from, to, to.Equal(latest), seesSites); err != nil {
			s.internal(w, r, err)
			return
		}
		e.SEOStatus = seoStatusOf(e, conns, seesSites)
	}
	if out == nil {
		out = []*seoOverviewBrand{}
	}
	notes := []string{
		"Branded search counts the search console queries that name the brand. Consoles hide rare queries, so it is a floor, not a total.",
		"Non-branded clicks are search clicks minus branded clicks, from the same stored Search Console totals.",
		"Visitors are all channels, not organic only.",
		"SEO snapshot tiles use one connector (Ahrefs when it has a snapshot in the period) so the arrows compare like with like.",
		"Organic sign-ups are search-landing conversions already stored from product analytics. Keyword shares, AI mention rate and competitor share of voice also come from stored reads only.",
	}
	if !seesSites {
		notes = append(notes, "Site audits and fixes need the seo.read scope, so they are left out.")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":          from.Format(time.DateOnly),
		"to":            to.Format(time.DateOnly),
		"previous_from": prevFrom.Format(time.DateOnly),
		"previous_to":   from.AddDate(0, 0, -1).Format(time.DateOnly),
		"data":          out,
		"notes":         notes,
	})
}

// maxOverviewWeeks is the longest period the overview shows: two years,
// so a year can be set against the year before.
const maxOverviewWeeks = 104

// overviewRange reads the period the overview shows: whole weeks, Monday to
// Sunday. from and to (dates) pick the weeks they fall in, never past the
// latest week that is over; weeks counts back from to (default 12).
func overviewRange(q url.Values, latest time.Time) (from, to time.Time, err error) {
	monday := func(d time.Time) time.Time { return d.AddDate(0, 0, -int((d.Weekday()+6)%7)) }
	parse := func(name string) (time.Time, bool, error) {
		v := q.Get(name)
		if v == "" {
			return time.Time{}, false, nil
		}
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return d, false, fmt.Errorf("%s should be a date, YYYY-MM-DD", name)
		}
		return d, true, nil
	}
	to = latest
	t, hasTo, err := parse("to")
	if err != nil {
		return from, to, err
	}
	if hasTo {
		if t = monday(t).AddDate(0, 0, 6); t.Before(latest) {
			to = t
		}
	}
	f, hasFrom, err := parse("from")
	if err != nil {
		return from, to, err
	}
	if hasFrom {
		from = monday(f)
		if from.After(to) {
			return from, to, fmt.Errorf("from should be before %s, the end of the last week that is over", to.Format(time.DateOnly))
		}
		if to.Sub(from) > time.Duration(7*maxOverviewWeeks)*24*time.Hour {
			return from, to, fmt.Errorf("pick at most %d weeks", maxOverviewWeeks)
		}
		return from, to, nil
	}
	weeks := 12
	if v := q.Get("weeks"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < trendWeeks || n > maxOverviewWeeks {
			return from, to, fmt.Errorf("weeks should be a number from %d to %d", trendWeeks, maxOverviewWeeks)
		}
		weeks = n
	}
	return to.AddDate(0, 0, -7*weeks+1), to, nil
}

// workspaceHosts lists every website the workspace has data for, with
// the brand an analytics site was set to report for, when one was.
func (s *Server) workspaceHosts(ctx context.Context, ws string) ([]string, map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host, COALESCE(outflare_site_brand(integration_id, site)::text, ''), true
		FROM analytics_syncs a WHERE workspace_id = $1 AND host <> ''
		  AND EXISTS (SELECT 1 FROM analytics_site_brands o WHERE o.integration_id = a.integration_id AND o.site = a.site)
		UNION SELECT host, '', false FROM analytics_syncs WHERE workspace_id = $1 AND host <> ''
		UNION SELECT host, '', false FROM seo_syncs WHERE workspace_id = $1
		UNION SELECT host, '', false FROM seo_sites WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []string
	set := map[string]string{}
	for rows.Next() {
		var h, brand string
		var chosen bool
		if err := rows.Scan(&h, &brand, &chosen); err != nil {
			return nil, nil, err
		}
		if chosen {
			set[h] = brand
		}
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	slices.Sort(out)
	return out, set, rows.Err()
}

// hostMeasures says what the workspace's tools measure for a website.
type hostMeasures struct{ search, traffic bool }

// measuredHosts lists the websites with search or visitor numbers since
// from, whichever brand they belong to.
func (s *Server) measuredHosts(ctx context.Context, ws string, from time.Time) (map[string]hostMeasures, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host, bool_or(clicks IS NOT NULL OR impressions IS NOT NULL), bool_or(visitors IS NOT NULL OR sessions IS NOT NULL)
		FROM analytics_daily
		WHERE workspace_id = $1 AND report = 'traffic' AND key = '' AND host <> '' AND day >= $2
		GROUP BY host`, ws, from)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]hostMeasures{}
	for rows.Next() {
		var h string
		var m hostMeasures
		if err := rows.Scan(&h, &m.search, &m.traffic); err != nil {
			return nil, err
		}
		out[h] = m
	}
	return out, rows.Err()
}

// sourceOf says whether a brand's websites have a kind of number, and if
// not, which other websites do.
func sourceOf(hosts []string, measured map[string]hostMeasures, has func(hostMeasures) bool) seoOverviewSource {
	var others []string
	for h, m := range measured {
		if !has(m) {
			continue
		}
		if slices.Contains(hosts, h) {
			return seoOverviewSource{State: "ready", Hosts: []string{}}
		}
		others = append(others, h)
	}
	if len(others) == 0 {
		return seoOverviewSource{State: "not_connected", Hosts: []string{}}
	}
	slices.Sort(others)
	return seoOverviewSource{State: "other_sites", Hosts: others}
}

// brandTerms are the words a search must contain to count as branded: the
// name (also without spaces), the handle and each domain's own name, such
// as repocket for repocket.com. Shorter than three letters is too common.
func brandTerms(name, handle string, domains []string) []string {
	var out []string
	add := func(t string) {
		t = strings.ToLower(strings.TrimSpace(t))
		if len([]rune(t)) >= 3 && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	add(name)
	add(strings.ReplaceAll(name, " ", ""))
	add(strings.ReplaceAll(handle, "-", " "))
	add(strings.ReplaceAll(handle, "-", ""))
	for _, d := range domains {
		reg, err := publicsuffix.EffectiveTLDPlusOne(d)
		if err != nil {
			reg = d
		}
		label, _, _ := strings.Cut(reg, ".")
		add(label)
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func likeAny(terms []string) []string {
	out := make([]string, len(terms))
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for i, t := range terms {
		out[i] = "%" + r.Replace(t) + "%"
	}
	return out
}

func (s *Server) fillSEOOverview(ctx context.Context, ws string, e *seoOverviewBrand, from, to time.Time, latest, seesSites bool) error {
	e.SEO = []seoOverviewSnapshot{}
	e.Sites = []seoOverviewSite{}
	// Read the period before as well, and at least the 2*trendWeeks the
	// trends compare.
	weeks := int(to.Sub(from).Hours()/24+1) / 7
	prevFrom := from.AddDate(0, 0, -7*weeks)
	start := to.AddDate(0, 0, -7*2*trendWeeks+1)
	if prevFrom.Before(start) {
		start = prevFrom
	}
	var all []seoOverviewWeek
	for d := start; !d.After(to); d = d.AddDate(0, 0, 7) {
		all = append(all, seoOverviewWeek{Week: d.Format(time.DateOnly)})
	}
	byWeek := map[string]*seoOverviewWeek{}
	for i := range all {
		byWeek[all[i].Week] = &all[i]
	}
	set := func(p **float64, v *float64) {
		if v != nil {
			*p = v
		}
	}

	// Traffic and search totals: each website's day once, taking the tool
	// that measured most, so two tools on one site don't count it twice.
	rows, err := s.pool.Query(ctx, `
		WITH d AS (
			SELECT host, day, max(visitors) v, max(sessions) s, max(clicks) c, max(impressions) i
			FROM analytics_daily
			WHERE workspace_id = $1 AND report = 'traffic' AND key = '' AND host = ANY($2) AND day BETWEEN $3 AND $4
			GROUP BY host, day
		)
		SELECT to_char(date_trunc('week', day), 'YYYY-MM-DD'), sum(v), sum(s), sum(c), sum(i) FROM d GROUP BY 1`,
		ws, e.Hosts, start, to)
	if err != nil {
		return err
	}
	for rows.Next() {
		var week string
		var v, se, c, i *float64
		if err := rows.Scan(&week, &v, &se, &c, &i); err != nil {
			rows.Close()
			return err
		}
		if wk := byWeek[week]; wk != nil {
			set(&wk.Visitors, v)
			set(&wk.Sessions, se)
			set(&wk.SearchClicks, c)
			set(&wk.SearchImpressions, i)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Split branded and non-branded from query rows. A week with search
	// totals but no query rows stays unknown: missing query data is not
	// zero branded clicks.
	if err := s.fillOverviewQueries(ctx, ws, e, start, to, byWeek, set); err != nil {
		return err
	}
	for i := range all {
		fillNonBranded(&all[i])
	}

	// SEO snapshots: per day, Ahrefs first (its DR is the one people quote),
	// the brand's main website for the rating, links added up. The latest
	// weeks run up to today, so the newest snapshot shows.
	end := to
	if latest {
		end = time.Now().UTC()
	}
	rows, err = s.pool.Query(ctx, `
		SELECT DISTINCT ON (host, day) to_char(day, 'YYYY-MM-DD'), host, connector, metrics
		FROM seo_snapshots
		WHERE workspace_id = $1 AND report = 'overview' AND key = '' AND host = ANY($2) AND day BETWEEN $3 AND $4
		ORDER BY host, day, connector <> 'ahrefs', synced_at DESC`, ws, e.Hosts, start.AddDate(0, 0, -7), end)
	if err != nil {
		return err
	}
	type snap struct {
		host, connector string
		m               struct {
			DR   *float64 `json:"domain_rating"`
			BL   *float64 `json:"backlinks"`
			RD   *float64 `json:"referring_domains"`
			OT   *float64 `json:"organic_traffic"`
			OK   *float64 `json:"organic_keywords"`
			Top3 *float64 `json:"top3"`
		}
	}
	days := map[string][]snap{}
	for rows.Next() {
		var day string
		var sn snap
		var raw []byte
		if err := rows.Scan(&day, &sn.host, &sn.connector, &raw); err != nil {
			rows.Close()
			return err
		}
		_ = json.Unmarshal(raw, &sn.m)
		days[day] = append(days[day], sn)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	keys := make([]string, 0, len(days))
	for d := range days {
		keys = append(keys, d)
	}
	slices.Sort(keys)
	add := func(a *float64, b *float64) *float64 {
		if b == nil {
			return a
		}
		if a == nil {
			v := *b
			return &v
		}
		v := *a + *b
		return &v
	}
	// One connector for the whole series, so a later DataForSEO snapshot
	// is not compared with an earlier Ahrefs one. Ahrefs wins when it
	// has any snapshot in the window.
	pref := ""
	for i := len(keys) - 1; i >= 0 && pref == ""; i-- {
		for _, sn := range days[keys[i]] {
			if sn.connector == "ahrefs" {
				pref = "ahrefs"
				break
			}
			if pref == "" {
				pref = sn.connector
			}
		}
	}
	if pref == "ahrefs" {
		// Keep Ahrefs even when a later day only has another tool.
	} else if pref == "" {
		pref = "ahrefs"
	}
	var snaps []seoOverviewSnapshot
	for _, d := range keys {
		var used []snap
		for _, sn := range days[d] {
			if sn.connector == pref {
				used = append(used, sn)
			}
		}
		if len(used) == 0 {
			continue
		}
		o := seoOverviewSnapshot{Day: d, Connector: pref}
		for _, sn := range used {
			if sn.m.DR != nil && (o.DomainRating == nil || len(e.Hosts) > 0 && sn.host == e.Hosts[0]) {
				o.DomainRating = sn.m.DR
			}
			o.Backlinks = add(o.Backlinks, sn.m.BL)
			o.ReferringDomains = add(o.ReferringDomains, sn.m.RD)
			o.OrganicTraffic = add(o.OrganicTraffic, sn.m.OT)
			o.OrganicKeywords = add(o.OrganicKeywords, sn.m.OK)
			o.Top3 = add(o.Top3, sn.m.Top3)
		}
		snaps = append(snaps, o)
	}
	// One snapshot from before the period, so its line starts at the start.
	first := from.AddDate(0, 0, -7).Format(time.DateOnly)
	for _, o := range snaps {
		if o.Day >= first {
			e.SEO = append(e.SEO, o)
		}
	}

	if err := s.fillOverviewSuccess(ctx, ws, e, start, to, byWeek, set); err != nil {
		return err
	}
	// The weeks shown, and as many before them.
	shown := len(all) - weeks
	e.Weeks = slices.Clone(all[shown:])
	e.PreviousWeeks = slices.Clone(all[shown-weeks : shown])

	e.Trends = map[string]seoTrend{
		"branded_search":      weeklyTrend(all, func(w seoOverviewWeek) *float64 { return w.BrandedClicks }),
		"non_branded_search":  weeklyTrend(all, func(w seoOverviewWeek) *float64 { return w.NonBrandedClicks }),
		"search_clicks":       weeklyTrend(all, func(w seoOverviewWeek) *float64 { return w.SearchClicks }),
		"search_ctr":          weeklyTrendAvg(all, func(w seoOverviewWeek) *float64 { return w.SearchCTR }, false),
		"search_position":     weeklyTrendAvg(all, func(w seoOverviewWeek) *float64 { return w.SearchPosition }, true),
		"visitors":            weeklyTrend(all, func(w seoOverviewWeek) *float64 { return w.Visitors }),
		"search_conversions":  weeklyTrend(all, func(w seoOverviewWeek) *float64 { return w.SearchConversions }),
		"keywords_top3":       snapshotTrend(snaps, func(s seoOverviewSnapshot) *float64 { return s.Top3 }),
		"keywords_top10":      weeklyTrendAvg(all, func(w seoOverviewWeek) *float64 { return w.KeywordsTop10 }, false),
		"ai_mention_rate":     weeklyTrendAvg(all, func(w seoOverviewWeek) *float64 { return w.AIMentionRate }, false),
		"ai_recommend_rate":   weeklyTrendAvg(all, func(w seoOverviewWeek) *float64 { return w.AIRecommendRate }, false),
		"share_of_voice":      seoTrend{Direction: "unknown"},
		"share_of_voice_top3": seoTrend{Direction: "unknown"},
		"domain_rating":       snapshotTrend(snaps, func(s seoOverviewSnapshot) *float64 { return s.DomainRating }),
		"backlinks":           snapshotTrend(snaps, func(s seoOverviewSnapshot) *float64 { return s.Backlinks }),
		"referring_domains":   snapshotTrend(snaps, func(s seoOverviewSnapshot) *float64 { return s.ReferringDomains }),
		"organic_traffic":     snapshotTrend(snaps, func(s seoOverviewSnapshot) *float64 { return s.OrganicTraffic }),
		"organic_keywords":    snapshotTrend(snaps, func(s seoOverviewSnapshot) *float64 { return s.OrganicKeywords }),
	}
	// The whole period against the one before: totals for counts, the
	// mean for rates and positions, and for snapshots the latest against
	// the last one before the period began.
	before := from.AddDate(0, 0, -1).Format(time.DateOnly)
	week := func(v func(seoOverviewWeek) *float64) seoTrend {
		return trendOf(sumWeeks(e.Weeks, v), sumWeeks(e.PreviousWeeks, v))
	}
	mean := func(v func(seoOverviewWeek) *float64, lowerIsBetter bool) seoTrend {
		return meanTrend(e.Weeks, e.PreviousWeeks, v, lowerIsBetter)
	}
	snapshot := func(v func(seoOverviewSnapshot) *float64) seoTrend { return snapshotPeriod(snaps, before, v) }
	e.Period = map[string]seoTrend{
		"branded_search":      week(func(w seoOverviewWeek) *float64 { return w.BrandedClicks }),
		"non_branded_search":  week(func(w seoOverviewWeek) *float64 { return w.NonBrandedClicks }),
		"search_clicks":       week(func(w seoOverviewWeek) *float64 { return w.SearchClicks }),
		"search_ctr":          mean(func(w seoOverviewWeek) *float64 { return w.SearchCTR }, false),
		"search_position":     mean(func(w seoOverviewWeek) *float64 { return w.SearchPosition }, true),
		"visitors":            week(func(w seoOverviewWeek) *float64 { return w.Visitors }),
		"search_conversions":  week(func(w seoOverviewWeek) *float64 { return w.SearchConversions }),
		"keywords_top3":       snapshot(func(s seoOverviewSnapshot) *float64 { return s.Top3 }),
		"keywords_top10":      mean(func(w seoOverviewWeek) *float64 { return w.KeywordsTop10 }, false),
		"ai_mention_rate":     mean(func(w seoOverviewWeek) *float64 { return w.AIMentionRate }, false),
		"ai_recommend_rate":   mean(func(w seoOverviewWeek) *float64 { return w.AIRecommendRate }, false),
		"share_of_voice":      {Direction: "unknown"},
		"share_of_voice_top3": {Direction: "unknown"},
		"domain_rating":       snapshot(func(s seoOverviewSnapshot) *float64 { return s.DomainRating }),
		"backlinks":           snapshot(func(s seoOverviewSnapshot) *float64 { return s.Backlinks }),
		"referring_domains":   snapshot(func(s seoOverviewSnapshot) *float64 { return s.ReferringDomains }),
		"organic_traffic":     snapshot(func(s seoOverviewSnapshot) *float64 { return s.OrganicTraffic }),
		"organic_keywords":    snapshot(func(s seoOverviewSnapshot) *float64 { return s.OrganicKeywords }),
	}
	if err := s.fillOverviewShareOfVoice(ctx, ws, e, to, from.AddDate(0, 0, -1)); err != nil {
		return err
	}

	if !seesSites {
		return nil
	}
	var brandID *string
	if e.Brand != nil {
		brandID = &e.Brand.ID
	}
	rows, err = s.pool.Query(ctx, `
		SELECT s.id::text, s.host, c.health_score, to_char(c.finished_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM seo_sites s
		LEFT JOIN LATERAL (
			SELECT health_score, finished_at FROM crawl_runs
			WHERE site_id = s.id AND health_score IS NOT NULL ORDER BY created_at DESC LIMIT 1
		) c ON true
		WHERE s.workspace_id = $1 AND s.brand_id IS NOT DISTINCT FROM $2::uuid
		ORDER BY s.host`, ws, brandID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var st seoOverviewSite
		if err := rows.Scan(&st.ID, &st.Host, &st.HealthScore, &st.CrawledAt); err != nil {
			rows.Close()
			return err
		}
		e.Sites = append(e.Sites, st)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	// A shared site on the brand's website counts as the brand's.
	ids := make([]string, len(e.Sites))
	for i, st := range e.Sites {
		ids[i] = st.ID
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM fixes WHERE workspace_id = $1 AND status IN ('open', 'in_progress')
		AND (brand_id IS NOT DISTINCT FROM $2::uuid OR subject_id = ANY($3::uuid[]))`, ws, brandID, ids).Scan(&n); err != nil {
		return err
	}
	e.Fixes = &n
	return nil
}

// seoOverviewConn is one SEO connection: what it reads and how its last
// read of each domain went.
type seoOverviewConn struct {
	connectors.Integration
	ConnectorName string
	Sync          string
	Domains       []string
	Syncs         map[string]seoOverviewSync
}

type seoOverviewSync struct {
	LastOKAt  *time.Time
	LastError *string
}

// seoOverviewConns lists the workspace's SEO connections, with the domains
// each reads and its last read of each.
func (s *Server) seoOverviewConns(ctx context.Context, ws string) ([]seoOverviewConn, error) {
	if s.integrations == nil {
		return nil, nil
	}
	list, err := s.integrations.List(ctx, s.pool, ws, "")
	if err != nil {
		return nil, err
	}
	var out []seoOverviewConn
	for _, i := range list {
		c, ok := s.integrations.Registry.Get(i.Connector)
		if !ok {
			continue
		}
		if _, ok := c.(connectors.SEOReader); !ok {
			continue
		}
		domains, _, err := seosync.Domains(ctx, s.pool, i)
		if err != nil {
			return nil, err
		}
		sc := seoOverviewConn{Integration: i, ConnectorName: c.Definition().Name, Sync: seosync.SettingsOf(i.Config).Sync, Domains: domains, Syncs: map[string]seoOverviewSync{}}
		rows, err := s.pool.Query(ctx, `SELECT host, last_ok_at, last_error FROM seo_syncs WHERE integration_id = $1`, i.ID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var h string
			var y seoOverviewSync
			if err := rows.Scan(&h, &y.LastOKAt, &y.LastError); err != nil {
				rows.Close()
				return nil, err
			}
			sc.Syncs[h] = y
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// seoStatusOf says where a brand's SEO snapshots stand and what to do
// next, so the page explains a gap instead of showing blanks. The failure
// text of a read is shown only with seo.read, like the SEO settings.
func seoStatusOf(e *seoOverviewBrand, conns []seoOverviewConn, seesErrors bool) seoOverviewStatus {
	brand := ""
	if e.Brand != nil {
		brand = e.Brand.ID
	}
	all := make([]connectors.Integration, len(conns))
	for i, c := range conns {
		all[i] = c.Integration
	}
	var serving []seoOverviewConn
	for _, c := range conns {
		if c.Status == "connected" && c.Serves(brand, all) {
			serving = append(serving, c)
		}
	}
	// Ahrefs first: its numbers are the ones the overview shows.
	slices.SortStableFunc(serving, func(a, b seoOverviewConn) int {
		return cmpBool(a.Connector != "ahrefs", b.Connector != "ahrefs")
	})
	if len(serving) == 0 {
		// Connected, but for other brands only: say so rather than ask to
		// connect it again.
		for _, c := range conns {
			if c.Status == "connected" {
				return seoOverviewStatus{State: "not_shared", IntegrationID: &c.ID, Connector: &c.Connector,
					Message: c.ConnectorName + " is connected, but not for this brand. Share it with this brand, or connect this brand's own account."}
			}
		}
		return seoOverviewStatus{State: "not_connected", Message: "Connect Ahrefs or DataForSEO to see domain rating, links and organic traffic. Each runs on your own account."}
	}
	c := serving[0]
	st := seoOverviewStatus{IntegrationID: &c.ID, Connector: &c.Connector}
	name := c.ConnectorName
	if len(e.Hosts) == 0 {
		st.State, st.Message = "no_website", "Add this brand's website to its brand settings, and "+name+" reads it within the hour."
		return st
	}
	// The brand's domains this connection reads.
	var read []string
	for _, h := range e.Hosts {
		for _, d := range c.Domains {
			if (h == d || strings.HasSuffix(h, "."+d)) && !slices.Contains(read, d) {
				read = append(read, d)
			}
		}
	}
	if len(read) == 0 {
		st.State = "not_followed"
		st.Message = name + " reads " + listOr(c.Domains, "no domains yet") + ", not " + e.Hosts[0] + ". Add " + e.Hosts[0] + " to its domains in SEO settings."
		return st
	}
	var ok *time.Time
	var failed *string
	waiting := false
	for _, d := range read {
		y, has := c.Syncs[d]
		switch {
		case !has:
			waiting = true
		case y.LastError != nil:
			failed = y.LastError
		}
		if has && y.LastOKAt != nil && (ok == nil || y.LastOKAt.After(*ok)) {
			ok = y.LastOKAt
		}
	}
	if ok != nil {
		v := ok.UTC().Format(time.RFC3339)
		st.LastOKAt = &v
	}
	switch {
	case failed != nil:
		st.State = "failed"
		st.Message = "The last read from " + name + " failed"
		if seesErrors {
			st.Message += ": " + strings.TrimSuffix(*failed, ".")
		}
		st.Message += "."
	case waiting && ok == nil && c.Sync == "off":
		st.State, st.Message = "off", name+" snapshots are turned off. Turn them on in SEO settings to see these numbers."
	case waiting && ok == nil:
		st.State, st.Message = "reading", name+" is reading "+strings.Join(read, ", ")+" for the first time. The numbers show here within the hour."
	default:
		st.State, st.Message = "ready", name+" reads "+strings.Join(read, ", ")+" every week."
	}
	return st
}

func cmpBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

func listOr(l []string, none string) string {
	if len(l) == 0 {
		return none
	}
	return strings.Join(l, ", ")
}

// weeklyTrend compares the last trendWeeks weeks with the ones before.
func weeklyTrend(weeks []seoOverviewWeek, v func(seoOverviewWeek) *float64) seoTrend {
	n := len(weeks)
	if n < 2*trendWeeks {
		return trendOf(nil, nil)
	}
	return trendOf(sumWeeks(weeks[n-trendWeeks:], v), sumWeeks(weeks[n-2*trendWeeks:n-trendWeeks], v))
}

// sumWeeks adds up one number over weeks; nil when no week measured it.
func sumWeeks(weeks []seoOverviewWeek, v func(seoOverviewWeek) *float64) *float64 {
	var t *float64
	for _, w := range weeks {
		if x := v(w); x != nil {
			if t == nil {
				t = new(float64)
			}
			*t += *x
		}
	}
	return t
}

// lastSnapshot is the newest value on or before a day.
func lastSnapshot(snaps []seoOverviewSnapshot, day string, v func(seoOverviewSnapshot) *float64) (*float64, string) {
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Day <= day {
			if x := v(snaps[i]); x != nil {
				return x, snaps[i].Day
			}
		}
	}
	return nil, ""
}

// snapshotTrend compares the latest snapshot with the last one at least
// trendWeeks weeks older.
func snapshotTrend(snaps []seoOverviewSnapshot, v func(seoOverviewSnapshot) *float64) seoTrend {
	cur, curDay := lastSnapshot(snaps, "9999-12-31", v)
	if cur == nil {
		return trendOf(nil, nil)
	}
	last, _ := time.Parse(time.DateOnly, curDay)
	prev, _ := lastSnapshot(snaps, last.AddDate(0, 0, -7*trendWeeks).Format(time.DateOnly), v)
	return trendOf(cur, prev)
}

// snapshotPeriod compares the latest snapshot with the last one on or
// before the day before the period.
func snapshotPeriod(snaps []seoOverviewSnapshot, before string, v func(seoOverviewSnapshot) *float64) seoTrend {
	cur, _ := lastSnapshot(snaps, "9999-12-31", v)
	prev, _ := lastSnapshot(snaps, before, v)
	return trendOf(cur, prev)
}

// trendOf says which way a number went. Within 3% is flat.
func trendOf(cur, prev *float64) seoTrend {
	t := seoTrend{Current: cur, Previous: prev, Direction: "unknown"}
	if cur == nil || prev == nil {
		return t
	}
	switch {
	case *prev == 0 && *cur == 0:
		t.Direction = "flat"
	case *prev == 0:
		t.Direction = "up"
	default:
		c := (*cur - *prev) / *prev
		t.Change = &c
		switch {
		case c > 0.03:
			t.Direction = "up"
		case c < -0.03:
			t.Direction = "down"
		default:
			t.Direction = "flat"
		}
	}
	return t
}
