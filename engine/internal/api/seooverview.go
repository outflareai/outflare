package api

import (
	"context"
	"encoding/json"
	"net/http"
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
	Week               string   `json:"week"`
	BrandedClicks      *float64 `json:"branded_clicks"`
	BrandedImpressions *float64 `json:"branded_impressions"`
	SearchClicks       *float64 `json:"search_clicks"`
	SearchImpressions  *float64 `json:"search_impressions"`
	Visitors           *float64 `json:"visitors"`
	Sessions           *float64 `json:"sessions"`
}

type seoOverviewSnapshot struct {
	Day              string   `json:"day"`
	Connector        string   `json:"connector"`
	DomainRating     *float64 `json:"domain_rating"`
	Backlinks        *float64 `json:"backlinks"`
	ReferringDomains *float64 `json:"referring_domains"`
	OrganicTraffic   *float64 `json:"organic_traffic"`
	OrganicKeywords  *float64 `json:"organic_keywords"`
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
	Sites  []seoOverviewSite     `json:"sites"`
	Fixes  *int                  `json:"open_fixes"`
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
	weeks := 12
	if v := r.URL.Query().Get("weeks"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2*trendWeeks || n > 52 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "weeks should be a number from 8 to 52")
			return
		}
		weeks = n
	}
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}

	// Whole weeks, Monday to Sunday, ending with the last one that is over.
	today := time.Now().UTC().Truncate(24 * time.Hour)
	to := today.AddDate(0, 0, -int((today.Weekday()+6)%7)-1)
	from := to.AddDate(0, 0, -7*weeks+1)

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
		if err := s.fillSEOOverview(r.Context(), ws, e, from, to, seesSites); err != nil {
			s.internal(w, r, err)
			return
		}
		e.SEOStatus = seoStatusOf(e, conns, seesSites)
	}
	if out == nil {
		out = []*seoOverviewBrand{}
	}
	notes := []string{"Branded search counts the search console queries that name the brand. Consoles hide rare queries, so it is a floor, not a total."}
	if !seesSites {
		notes = append(notes, "Site audits and fixes need the seo.read scope, so they are left out.")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from":  from.Format(time.DateOnly),
		"to":    to.Format(time.DateOnly),
		"data":  out,
		"notes": notes,
	})
}

// workspaceHosts lists every website the workspace has data for, with
// the brand an analytics site was set to report for, when one was.
func (s *Server) workspaceHosts(ctx context.Context, ws string) ([]string, map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT host, COALESCE(analytics_site_brand($1, integration_id, site, host)::text, ''), true
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

func (s *Server) fillSEOOverview(ctx context.Context, ws string, e *seoOverviewBrand, from, to time.Time, seesSites bool) error {
	e.Weeks = []seoOverviewWeek{}
	e.SEO = []seoOverviewSnapshot{}
	e.Sites = []seoOverviewSite{}
	for d := from; !d.After(to); d = d.AddDate(0, 0, 7) {
		e.Weeks = append(e.Weeks, seoOverviewWeek{Week: d.Format(time.DateOnly)})
	}
	byWeek := map[string]*seoOverviewWeek{}
	for i := range e.Weeks {
		byWeek[e.Weeks[i].Week] = &e.Weeks[i]
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
		ws, e.Hosts, from, to)
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

	if len(e.Terms) > 0 {
		rows, err = s.pool.Query(ctx, `
			WITH q AS (
				SELECT day, max(clicks) c, max(impressions) i
				FROM analytics_daily
				WHERE workspace_id = $1 AND report = 'search_queries' AND key <> '' AND host = ANY($2) AND day BETWEEN $3 AND $4
				  AND lower(key) LIKE ANY($5)
				GROUP BY host, day, key
			)
			SELECT to_char(date_trunc('week', day), 'YYYY-MM-DD'), sum(c), sum(i) FROM q GROUP BY 1`,
			ws, e.Hosts, from, to, likeAny(e.Terms))
		if err != nil {
			return err
		}
		for rows.Next() {
			var week string
			var c, i *float64
			if err := rows.Scan(&week, &c, &i); err != nil {
				rows.Close()
				return err
			}
			if wk := byWeek[week]; wk != nil {
				set(&wk.BrandedClicks, c)
				set(&wk.BrandedImpressions, i)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	// Weeks the search consoles covered but no query named the brand are
	// zero, not unknown.
	for i := range e.Weeks {
		if e.Weeks[i].BrandedClicks == nil && e.Weeks[i].SearchImpressions != nil {
			zero := 0.0
			e.Weeks[i].BrandedClicks, e.Weeks[i].BrandedImpressions = &zero, &zero
		}
	}

	// SEO snapshots: per day, Ahrefs first (its DR is the one people quote),
	// the brand's main website for the rating, links added up.
	rows, err = s.pool.Query(ctx, `
		SELECT DISTINCT ON (host, day) to_char(day, 'YYYY-MM-DD'), host, connector, metrics
		FROM seo_snapshots
		WHERE workspace_id = $1 AND report = 'overview' AND key = '' AND host = ANY($2) AND day BETWEEN $3 AND $4
		ORDER BY host, day, connector <> 'ahrefs', synced_at DESC`, ws, e.Hosts, from.AddDate(0, 0, -7), time.Now().UTC())
	if err != nil {
		return err
	}
	type snap struct {
		host, connector string
		m               struct {
			DR *float64 `json:"domain_rating"`
			BL *float64 `json:"backlinks"`
			RD *float64 `json:"referring_domains"`
			OT *float64 `json:"organic_traffic"`
			OK *float64 `json:"organic_keywords"`
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
	for _, d := range keys {
		o := seoOverviewSnapshot{Day: d}
		for _, sn := range days[d] {
			if o.Connector == "" || sn.connector == "ahrefs" {
				o.Connector = sn.connector
			}
			if sn.m.DR != nil && (o.DomainRating == nil || len(e.Hosts) > 0 && sn.host == e.Hosts[0]) {
				o.DomainRating = sn.m.DR
			}
			o.Backlinks = add(o.Backlinks, sn.m.BL)
			o.ReferringDomains = add(o.ReferringDomains, sn.m.RD)
			o.OrganicTraffic = add(o.OrganicTraffic, sn.m.OT)
			o.OrganicKeywords = add(o.OrganicKeywords, sn.m.OK)
		}
		e.SEO = append(e.SEO, o)
	}

	e.Trends = map[string]seoTrend{
		"branded_search":    weeklyTrend(e.Weeks, func(w seoOverviewWeek) *float64 { return w.BrandedClicks }),
		"search_clicks":     weeklyTrend(e.Weeks, func(w seoOverviewWeek) *float64 { return w.SearchClicks }),
		"visitors":          weeklyTrend(e.Weeks, func(w seoOverviewWeek) *float64 { return w.Visitors }),
		"domain_rating":     snapshotTrend(e.SEO, to, func(s seoOverviewSnapshot) *float64 { return s.DomainRating }),
		"backlinks":         snapshotTrend(e.SEO, to, func(s seoOverviewSnapshot) *float64 { return s.Backlinks }),
		"referring_domains": snapshotTrend(e.SEO, to, func(s seoOverviewSnapshot) *float64 { return s.ReferringDomains }),
		"organic_traffic":   snapshotTrend(e.SEO, to, func(s seoOverviewSnapshot) *float64 { return s.OrganicTraffic }),
		"organic_keywords":  snapshotTrend(e.SEO, to, func(s seoOverviewSnapshot) *float64 { return s.OrganicKeywords }),
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
		WHERE s.workspace_id = $1 AND (s.brand_id IS NOT DISTINCT FROM $2::uuid OR s.brand_id IS NULL AND s.host = ANY($3))
		ORDER BY s.host`, ws, brandID, e.Hosts)
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
	sum := func(ws []seoOverviewWeek) *float64 {
		var t *float64
		for _, w := range ws {
			if x := v(w); x != nil {
				if t == nil {
					t = new(float64)
				}
				*t += *x
			}
		}
		return t
	}
	n := len(weeks)
	if n < 2*trendWeeks {
		return trendOf(nil, nil)
	}
	return trendOf(sum(weeks[n-trendWeeks:]), sum(weeks[n-2*trendWeeks:n-trendWeeks]))
}

// snapshotTrend compares the latest snapshot with the last one at least
// trendWeeks weeks older.
func snapshotTrend(snaps []seoOverviewSnapshot, to time.Time, v func(seoOverviewSnapshot) *float64) seoTrend {
	var cur, prev *float64
	var curDay string
	for i := len(snaps) - 1; i >= 0; i-- {
		if x := v(snaps[i]); x != nil {
			cur, curDay = x, snaps[i].Day
			break
		}
	}
	if cur == nil {
		return trendOf(nil, nil)
	}
	last, _ := time.Parse(time.DateOnly, curDay)
	cutoff := last.AddDate(0, 0, -7*trendWeeks).Format(time.DateOnly)
	for i := len(snaps) - 1; i >= 0; i-- {
		if snaps[i].Day <= cutoff {
			if x := v(snaps[i]); x != nil {
				prev = x
				break
			}
		}
	}
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
