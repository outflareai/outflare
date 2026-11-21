package api

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
)

// Links at a glance for a brand: is its referring-domain footprint
// growing (weekly, against its tracked competitors), how many links and
// domains it gains and loses a week, which links it lost lately and
// whether any were bought, whether bought links are still live with the
// ordered anchor and target, and the anchor profile of its live links.
// Reads only what is stored: SEO snapshots, backlinks and link orders.

const (
	linksWeeks       = 13
	linksVelocity    = 12
	linksLostDays    = 30
	linksLostShown   = 200
	linksCompetitors = 8
	linksStaleCheck  = 72 * time.Hour
)

type linksPoint struct {
	Week string  `json:"week"`
	RD   float64 `json:"referring_domains"`
}

type linksSeries struct {
	Host       string       `json:"host"`
	Competitor bool         `json:"competitor"`
	Connector  string       `json:"connector"`
	Points     []linksPoint `json:"points"`
	Latest     *float64     `json:"latest"`
	WoW        *float64     `json:"wow"`
	MoM        *float64     `json:"mom"`
}

type linksWeek struct {
	Week        string `json:"week"`
	New         int    `json:"new"`
	Lost        int    `json:"lost"`
	Regained    int    `json:"regained"`
	NewDomains  int    `json:"new_domains"`
	LostDomains int    `json:"lost_domains"`
}

type linksLost struct {
	SourceURL    string     `json:"source_url"`
	SourceDomain string     `json:"source_domain"`
	TargetURL    string     `json:"target_url"`
	Anchor       string     `json:"anchor"`
	Rating       *float64   `json:"source_rating"`
	FirstSeen    *time.Time `json:"first_seen_at"`
	LostAt       time.Time  `json:"lost_at"`
	OrderID      *string    `json:"order_id"`
}

type linksOrder struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	TargetURL    string     `json:"target_url"`
	PublishedURL string     `json:"published_url"`
	Anchor       string     `json:"anchor"`
	LiveAnchor   string     `json:"live_anchor"`
	CheckedAt    *time.Time `json:"checked_at"`
	// not_live, wrong_target, anchor_mismatch, unchecked or lost.
	Problems []string `json:"problems"`
}

var (
	linkURLish    = regexp.MustCompile(`(?i)^(https?://|www\.)|\.[a-z]{2,}(/|$)`)
	linkGeneric   = []string{"click here", "here", "this", "link", "website", "site", "source", "read more", "learn more", "more", "visit", "this article", "this post", "this page", "homepage", "official site", "check it out", "see here"}
	linkWordSplit = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

func (s *Server) seoLinks(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a := accessFrom(ctx)
	ws, brand := a.WorkspaceID, a.Brand
	var name, handle string
	var domains []string
	if err := s.pool.QueryRow(ctx, `SELECT name, handle, COALESCE(domains, '{}') FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&name, &handle, &domains); err != nil {
		s.internal(w, r, err)
		return
	}
	series, err := s.linksSeries(ctx, ws, brand, domains)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	velocity, err := s.linksVelocity(ctx, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	lost, err := s.linksLost(ctx, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	orders, live, total, err := s.linksOrders(ctx, ws, brand)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	anchors, err := s.linksAnchors(ctx, ws, brand, brandTerms(name, handle, domains))
	if err != nil {
		s.internal(w, r, err)
		return
	}

	sum := map[string]any{"referring_domains": nil, "rd_wow": nil, "rd_mom": nil, "new_domains_week": 0, "lost_domains_week": 0,
		"lost_links": len(lost), "lost_paid": 0, "bought_live": live, "bought_total": total, "bought_problems": len(orders)}
	var rd *float64
	for _, x := range series {
		if x.Competitor || x.Latest == nil {
			continue
		}
		v := *x.Latest
		if rd != nil {
			v += *rd
		}
		rd = &v
	}
	if rd != nil {
		sum["referring_domains"] = *rd
		// The change of the brand's sites added up, when there is one site
		// (several sites of one brand rarely share a week of snapshots).
		own := 0
		for _, x := range series {
			if !x.Competitor && x.Latest != nil {
				own++
				sum["rd_wow"], sum["rd_mom"] = x.WoW, x.MoM
			}
		}
		if own != 1 {
			sum["rd_wow"], sum["rd_mom"] = nil, nil
		}
	}
	if n := len(velocity); n > 0 {
		sum["new_domains_week"], sum["lost_domains_week"] = velocity[n-1].NewDomains, velocity[n-1].LostDomains
	}
	paid := 0
	for _, l := range lost {
		if l.OrderID != nil {
			paid++
		}
	}
	sum["lost_paid"] = paid
	writeJSON(w, http.StatusOK, map[string]any{
		"summary": sum, "series": series, "velocity": velocity, "lost": lost, "orders": orders, "anchors": anchors,
		"notes": []string{
			"Referring domains are from the weekly SEO snapshots, Ahrefs when it has any for a domain in the window, else the other connection, the last snapshot of each week; competitors are the brand's tracked competitors with the most referring domains.",
			"Velocity counts backlink events a week: new, lost and regained links, and the linking domains behind the new and lost ones. Only links the SEO connections read are seen (the strongest per domain), so it is a trend, not a full count.",
			"Lost links are links to the brand lost in the last 30 days; a link is marked bought when a link order was published on the same page.",
			"Bought links are published, paid, verified or lost link orders; each is flagged when the last live check did not find the link, found it pointing elsewhere or with a different anchor than ordered, or has not run for 3 days.",
			"Anchors are the brand's live links: branded (the brand's name or domain), URL, generic (click here, website), exact (a target keyword of a page), partial (most words of one) or other.",
		},
	})
}

// linksSeries is weekly referring domains for the brand's own domains
// and its biggest tracked competitors.
func (s *Server) linksSeries(ctx context.Context, ws, brand string, domains []string) ([]linksSeries, error) {
	own := map[string]bool{}
	add := func(h string) {
		h = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(h)), "www.")
		if h != "" {
			own[h] = true
		}
	}
	for _, d := range domains {
		add(d)
	}
	rows, err := s.pool.Query(ctx, `SELECT host FROM seo_sites WHERE workspace_id = $1 AND brand_id = $2`, ws, brand)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		add(h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	comp := map[string]bool{}
	rows, err = s.pool.Query(ctx, `SELECT DISTINCT lower(host) FROM seo_competitors WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked'`, ws, brand)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, err
		}
		h = strings.TrimPrefix(h, "www.")
		if !own[h] {
			comp[h] = true
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var hosts []string
	for h := range own {
		hosts = append(hosts, h, "www."+h)
	}
	for h := range comp {
		hosts = append(hosts, h, "www."+h)
	}
	out := []linksSeries{}
	if len(hosts) == 0 {
		return out, nil
	}
	rows, err = s.pool.Query(ctx, `
		SELECT regexp_replace(lower(host), '^www\.', ''), connector, to_char(date_trunc('week', day), 'YYYY-MM-DD'),
			(metrics->>'referring_domains')::float8
		FROM seo_snapshots
		WHERE workspace_id = $1 AND report = 'overview' AND key = '' AND host = ANY($2)
			AND day >= current_date - $3::int * 7 AND jsonb_typeof(metrics->'referring_domains') = 'number'
		ORDER BY 1, 2, 3, day, synced_at`, ws, hosts, linksWeeks)
	if err != nil {
		return nil, err
	}
	// host → connector → week → last value of the week.
	data := map[string]map[string]map[string]float64{}
	for rows.Next() {
		var h, conn, week string
		var v float64
		if err := rows.Scan(&h, &conn, &week, &v); err != nil {
			rows.Close()
			return nil, err
		}
		if data[h] == nil {
			data[h] = map[string]map[string]float64{}
		}
		if data[h][conn] == nil {
			data[h][conn] = map[string]float64{}
		}
		data[h][conn][week] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for h, byConn := range data {
		conn := ""
		if _, ok := byConn["ahrefs"]; ok {
			conn = "ahrefs"
		} else {
			for c := range byConn {
				if conn == "" || c < conn {
					conn = c
				}
			}
		}
		x := linksSeries{Host: h, Competitor: !own[h], Connector: conn, Points: []linksPoint{}}
		for wk, v := range byConn[conn] {
			x.Points = append(x.Points, linksPoint{Week: wk, RD: v})
		}
		slices.SortFunc(x.Points, func(a, b linksPoint) int { return strings.Compare(a.Week, b.Week) })
		if n := len(x.Points); n > 0 {
			last := x.Points[n-1]
			x.Latest = &last.RD
			lastWeek, _ := time.Parse("2006-01-02", last.Week)
			for _, p := range x.Points[:n-1] {
				wk, _ := time.Parse("2006-01-02", p.Week)
				d := p.RD
				if lastWeek.Sub(wk) == 7*24*time.Hour {
					x.WoW = ptrDelta(last.RD, d)
				}
				if lastWeek.Sub(wk) == 28*24*time.Hour {
					x.MoM = ptrDelta(last.RD, d)
				}
			}
		}
		out = append(out, x)
	}
	slices.SortFunc(out, func(a, b linksSeries) int {
		if a.Competitor != b.Competitor {
			if a.Competitor {
				return 1
			}
			return -1
		}
		la, lb := 0.0, 0.0
		if a.Latest != nil {
			la = *a.Latest
		}
		if b.Latest != nil {
			lb = *b.Latest
		}
		switch {
		case la > lb:
			return -1
		case la < lb:
			return 1
		}
		return strings.Compare(a.Host, b.Host)
	})
	kept := out[:0]
	competitors := 0
	for _, x := range out {
		if x.Competitor {
			if competitors >= linksCompetitors {
				continue
			}
			competitors++
		}
		kept = append(kept, x)
	}
	return kept, nil
}

func ptrDelta(now, before float64) *float64 {
	d := now - before
	return &d
}

func (s *Server) linksVelocity(ctx context.Context, ws, brand string) ([]linksWeek, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT to_char(date_trunc('week', e.at), 'YYYY-MM-DD'),
			count(*) FILTER (WHERE e.kind = 'new'), count(*) FILTER (WHERE e.kind = 'lost'), count(*) FILTER (WHERE e.kind = 'regained'),
			count(DISTINCT b.source_domain) FILTER (WHERE e.kind = 'new'), count(DISTINCT b.source_domain) FILTER (WHERE e.kind = 'lost')
		FROM backlink_events e JOIN backlinks b ON b.id = e.backlink_id AND b.workspace_id = e.workspace_id
		WHERE e.workspace_id = $1 AND b.brand_id = $2 AND e.at >= date_trunc('week', now()) - make_interval(weeks => $3 - 1)
		GROUP BY 1 ORDER BY 1`, ws, brand, linksVelocity)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	got := map[string]linksWeek{}
	for rows.Next() {
		var x linksWeek
		if err := rows.Scan(&x.Week, &x.New, &x.Lost, &x.Regained, &x.NewDomains, &x.LostDomains); err != nil {
			return nil, err
		}
		got[x.Week] = x
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Every week shows, a quiet one as zeros.
	now := time.Now().UTC()
	monday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -((int(now.Weekday()) + 6) % 7))
	out := make([]linksWeek, 0, linksVelocity)
	for i := linksVelocity - 1; i >= 0; i-- {
		wk := monday.AddDate(0, 0, -7*i).Format("2006-01-02")
		x, ok := got[wk]
		if !ok {
			x = linksWeek{Week: wk}
		}
		out = append(out, x)
	}
	return out, nil
}

func (s *Server) linksLost(ctx context.Context, ws, brand string) ([]linksLost, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.source_url, b.source_domain, b.target_url, b.anchor, b.source_rating::float8, b.first_seen_at, b.lost_at, o.id::text
		FROM backlinks b
		LEFT JOIN LATERAL (
			SELECT id FROM coverage_link_orders o
			WHERE o.workspace_id = b.workspace_id AND o.brand_id = $2 AND o.published_url <> ''
				AND rtrim(lower(regexp_replace(o.published_url, '^https?://(www\.)?', '')), '/') = rtrim(lower(regexp_replace(b.source_url, '^https?://(www\.)?', '')), '/')
			ORDER BY o.created_at DESC LIMIT 1
		) o ON true
		WHERE b.workspace_id = $1 AND b.brand_id = $2 AND b.status = 'lost' AND b.lost_at > now() - make_interval(days => $3)
		ORDER BY (o.id IS NULL), b.source_rating DESC NULLS LAST, b.lost_at DESC
		LIMIT $4`, ws, brand, linksLostDays, linksLostShown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []linksLost{}
	for rows.Next() {
		var x linksLost
		if err := rows.Scan(&x.SourceURL, &x.SourceDomain, &x.TargetURL, &x.Anchor, &x.Rating, &x.FirstSeen, &x.LostAt, &x.OrderID); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

// linksOrders is every bought link with a problem, and how many bought
// links are live out of all of them.
func (s *Server) linksOrders(ctx context.Context, ws, brand string) ([]linksOrder, int, int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, status, target_url, published_url, anchor_primary, last_check_anchor, last_check_at,
			last_check_live, last_check_target_match, last_check_anchor_match
		FROM coverage_link_orders
		WHERE workspace_id = $1 AND brand_id = $2 AND status IN ('published', 'paid', 'verified', 'lost')
		ORDER BY published_at DESC NULLS LAST, created_at DESC`, ws, brand)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	out := []linksOrder{}
	live, total := 0, 0
	for rows.Next() {
		var x linksOrder
		var isLive, target *bool
		var match string
		if err := rows.Scan(&x.ID, &x.Status, &x.TargetURL, &x.PublishedURL, &x.Anchor, &x.LiveAnchor, &x.CheckedAt, &isLive, &target, &match); err != nil {
			return nil, 0, 0, err
		}
		total++
		x.Problems = []string{}
		switch {
		case x.Status == "lost":
			x.Problems = append(x.Problems, "lost")
		case isLive != nil && !*isLive:
			x.Problems = append(x.Problems, "not_live")
		default:
			if isLive != nil && *isLive {
				live++
			}
			if target != nil && !*target {
				x.Problems = append(x.Problems, "wrong_target")
			}
			if match == "different" {
				x.Problems = append(x.Problems, "anchor_mismatch")
			}
		}
		if x.Status != "lost" && (x.CheckedAt == nil || time.Since(*x.CheckedAt) > linksStaleCheck) {
			x.Problems = append(x.Problems, "unchecked")
		}
		if len(x.Problems) > 0 {
			out = append(out, x)
		}
	}
	return out, live, total, rows.Err()
}

// linksAnchors sorts the brand's live links by the kind of anchor.
func (s *Server) linksAnchors(ctx context.Context, ws, brand string, terms []string) (map[string]int, error) {
	var keywords []string
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT lower(t.keyword) FROM page_targets t JOIN seo_sites s ON s.id = t.site_id
		WHERE t.workspace_id = $1 AND s.brand_id = $2 AND t.keyword <> ''`, ws, brand)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			rows.Close()
			return nil, err
		}
		keywords = append(keywords, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := map[string]int{"branded": 0, "url": 0, "generic": 0, "exact": 0, "partial": 0, "other": 0, "empty": 0}
	rows, err = s.pool.Query(ctx, `
		SELECT lower(anchor), count(*)::int FROM backlinks
		WHERE workspace_id = $1 AND brand_id = $2 AND status <> 'lost' GROUP BY 1`, ws, brand)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var anchor string
		var n int
		if err := rows.Scan(&anchor, &n); err != nil {
			return nil, err
		}
		out[anchorKind(anchor, terms, keywords)] += n
	}
	return out, rows.Err()
}

func anchorKind(anchor string, terms, keywords []string) string {
	a := strings.Join(strings.Fields(strings.ToLower(anchor)), " ")
	switch {
	case a == "":
		return "empty"
	case linkURLish.MatchString(a):
		return "url"
	}
	for _, t := range terms {
		if strings.Contains(a, t) {
			return "branded"
		}
	}
	if slices.Contains(linkGeneric, strings.Trim(a, " .!?:")) {
		return "generic"
	}
	words := linkWords(a)
	for _, k := range keywords {
		if linkWords(k) == words {
			return "exact"
		}
	}
	for _, k := range keywords {
		kw := strings.Fields(linkWords(k))
		if len(kw) == 0 {
			continue
		}
		has := 0
		for _, x := range kw {
			if slices.Contains(strings.Fields(words), x) {
				has++
			}
		}
		if has*2 > len(kw) {
			return "partial"
		}
	}
	return "other"
}

func linkWords(s string) string {
	return strings.Join(strings.Fields(linkWordSplit.ReplaceAllString(strings.ToLower(s), " ")), " ")
}
