package api

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

// fillOverviewQueries sets branded and non-branded search from stored
// query rows, and the impression-weighted non-branded position. Weeks
// with no query rows are left unknown, even when traffic totals exist.
func (s *Server) fillOverviewQueries(ctx context.Context, ws string, e *seoOverviewBrand, from, to time.Time, byWeek map[string]*seoOverviewWeek, set func(**float64, *float64)) error {
	if len(e.Hosts) == 0 {
		return nil
	}
	terms := likeAny(e.Terms)
	if terms == nil {
		terms = []string{}
	}
	rows, err := s.pool.Query(ctx, `
		WITH q AS (
			SELECT day, key, max(clicks) c, max(impressions) i, max(position) p
			FROM analytics_daily
			WHERE workspace_id = $1 AND report = 'search_queries' AND key <> '' AND host = ANY($2) AND day BETWEEN $3 AND $4
			GROUP BY host, day, key
		),
		m AS (
			SELECT day, key, c, i, p,
			       (cardinality($5::text[]) > 0 AND lower(key) LIKE ANY($5)) AS branded
			FROM q
		)
		SELECT to_char(date_trunc('week', day), 'YYYY-MM-DD'),
		       sum(c) FILTER (WHERE branded), sum(i) FILTER (WHERE branded),
		       sum(c) FILTER (WHERE NOT branded), sum(i) FILTER (WHERE NOT branded),
		       sum(i * p) FILTER (WHERE NOT branded AND p IS NOT NULL) / NULLIF(sum(i) FILTER (WHERE NOT branded AND p IS NOT NULL), 0)
		FROM m GROUP BY 1`, ws, e.Hosts, from, to, terms)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var week string
		var bc, bi, nc, ni, pos *float64
		if err := rows.Scan(&week, &bc, &bi, &nc, &ni, &pos); err != nil {
			return err
		}
		wk := byWeek[week]
		if wk == nil {
			continue
		}
		// A week that appears here had query rows, so missing branded
		// clicks are zero, not unknown.
		if bc == nil {
			z := 0.0
			bc, bi = &z, &z
		}
		set(&wk.BrandedClicks, bc)
		set(&wk.BrandedImpressions, bi)
		set(&wk.NonBrandedClicks, nc)
		set(&wk.NonBrandedImpressions, ni)
		set(&wk.SearchPosition, pos)
	}
	return rows.Err()
}

// fillNonBranded prefers complete Search Console totals minus branded
// clicks when both sides exist, and sets non-branded CTR from them.
func fillNonBranded(w *seoOverviewWeek) {
	if w.SearchClicks != nil && w.BrandedClicks != nil {
		v := *w.SearchClicks - *w.BrandedClicks
		if v < 0 {
			v = 0
		}
		w.NonBrandedClicks = &v
	}
	if w.SearchImpressions != nil && w.BrandedImpressions != nil {
		v := *w.SearchImpressions - *w.BrandedImpressions
		if v < 0 {
			v = 0
		}
		w.NonBrandedImpressions = &v
	}
	if w.NonBrandedClicks != nil && w.NonBrandedImpressions != nil && *w.NonBrandedImpressions > 0 {
		v := *w.NonBrandedClicks / *w.NonBrandedImpressions
		w.SearchCTR = &v
	}
}

// fillOverviewSuccess adds stored search conversions, tracked-keyword
// top-10 share and AI mention and recommend rates, week by week. It
// never calls a provider.
func (s *Server) fillOverviewSuccess(ctx context.Context, ws string, e *seoOverviewBrand, from, to time.Time, byWeek map[string]*seoOverviewWeek, set func(**float64, *float64)) error {
	if len(e.Hosts) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		WITH d AS (
			SELECT host, path, day, max(search_conversions) sc, max(conversions) c
			FROM page_conversions
			WHERE workspace_id = $1 AND host = ANY($2) AND day BETWEEN $3 AND $4
			GROUP BY host, path, day
		)
		SELECT to_char(date_trunc('week', day), 'YYYY-MM-DD'), sum(sc) FROM d GROUP BY 1`,
		ws, e.Hosts, from, to)
	if err != nil {
		return err
	}
	if err := scanWeekFloats(rows, byWeek, func(w *seoOverviewWeek, v *float64) { set(&w.SearchConversions, v) }); err != nil {
		return err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT to_char(date_trunc('week', o.day), 'YYYY-MM-DD'),
		       count(*) FILTER (WHERE o.position IS NOT NULL AND o.position <= 10)::float8 / NULLIF(count(*), 0),
		       count(*)::float8
		FROM rank_observations o
		JOIN tracked_keywords t ON t.id = o.tracked_id
		JOIN seo_sites s ON s.id = t.site_id
		WHERE o.workspace_id = $1 AND t.status = 'active' AND o.day BETWEEN $2 AND $3
		  AND s.host = ANY($4)
		GROUP BY 1`, ws, from, to, e.Hosts)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var week string
		var share, n *float64
		if err := rows.Scan(&week, &share, &n); err != nil {
			return err
		}
		if wk := byWeek[week]; wk != nil {
			set(&wk.KeywordsTop10, share)
			set(&wk.KeywordsTracked, n)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var brandID *string
	if e.Brand != nil {
		brandID = &e.Brand.ID
	}
	rows, err = s.pool.Query(ctx, `
		SELECT to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand'
		       ))::float8 / NULLIF(count(*), 0),
		       count(*) FILTER (WHERE EXISTS (
		           SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand' AND m.stance = 'recommended'
		       ))::float8 / NULLIF(count(*), 0)
		FROM ai_samples s
		WHERE s.workspace_id = $1 AND s.status = 'ok' AND s.sampled_at >= $2 AND s.sampled_at < ($3::date + 1)
		  AND s.brand_id IS NOT DISTINCT FROM $4::uuid
		GROUP BY 1`, ws, from, to, brandID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var week string
		var mention, recommend *float64
		if err := rows.Scan(&week, &mention, &recommend); err != nil {
			return err
		}
		if wk := byWeek[week]; wk != nil {
			set(&wk.AIMentionRate, mention)
			set(&wk.AIRecommendRate, recommend)
		}
	}
	return rows.Err()
}

func scanWeekFloats(rows pgx.Rows, byWeek map[string]*seoOverviewWeek, set func(*seoOverviewWeek, *float64)) error {
	defer rows.Close()
	for rows.Next() {
		var week string
		var v *float64
		if err := rows.Scan(&week, &v); err != nil {
			return err
		}
		if wk := byWeek[week]; wk != nil {
			set(wk, v)
		}
	}
	return rows.Err()
}

// fillOverviewShareOfVoice sets search-visit and top-3 share of the
// brand versus its tracked competitors, from stored snapshots of the
// same connector the overview tiles use: in trends the end against 4 weeks
// earlier, in period the end against the day before the period. Opening
// the page costs nothing.
func (s *Server) fillOverviewShareOfVoice(ctx context.Context, ws string, e *seoOverviewBrand, to, before time.Time) error {
	if len(e.Hosts) == 0 || e.Trends == nil {
		return nil
	}
	pref := ""
	if len(e.SEO) > 0 {
		pref = e.SEO[len(e.SEO)-1].Connector
	}
	if pref == "" {
		return nil
	}
	var brandID *string
	if e.Brand != nil {
		brandID = &e.Brand.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT host FROM seo_competitors
		WHERE workspace_id = $1 AND status = 'tracked' AND brand_id IS NOT DISTINCT FROM $2::uuid`, ws, brandID)
	if err != nil {
		return err
	}
	comps, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var h string
		return h, row.Scan(&h)
	})
	if err != nil {
		return err
	}
	if len(comps) == 0 {
		return nil
	}
	hosts := append(append([]string{}, e.Hosts...), comps...)
	share := func(asOf time.Time) (search, top3 *float64, err error) {
		rows, err := s.pool.Query(ctx, `
			SELECT DISTINCT ON (host) host, metrics
			FROM seo_snapshots
			WHERE workspace_id = $1 AND report = 'overview' AND key = '' AND connector = $2
			  AND host = ANY($3) AND day <= $4
			ORDER BY host, day DESC, synced_at DESC`, ws, pref, hosts, asOf.Format(time.DateOnly))
		if err != nil {
			return nil, nil, err
		}
		defer rows.Close()
		var brandT, allT, brand3, all3 float64
		var hasT, has3 bool
		brand := map[string]bool{}
		for _, h := range e.Hosts {
			brand[h] = true
		}
		for rows.Next() {
			var host string
			var raw []byte
			if err := rows.Scan(&host, &raw); err != nil {
				return nil, nil, err
			}
			var m struct {
				OT   *float64 `json:"organic_traffic"`
				Top3 *float64 `json:"top3"`
			}
			_ = json.Unmarshal(raw, &m)
			if m.OT != nil {
				allT += *m.OT
				hasT = true
				if brand[host] {
					brandT += *m.OT
				}
			}
			if m.Top3 != nil {
				all3 += *m.Top3
				has3 = true
				if brand[host] {
					brand3 += *m.Top3
				}
			}
		}
		if err := rows.Err(); err != nil {
			return nil, nil, err
		}
		if hasT && allT > 0 {
			v := brandT / allT
			search = &v
		}
		if has3 && all3 > 0 {
			v := brand3 / all3
			top3 = &v
		}
		return search, top3, nil
	}
	curS, cur3, err := share(to)
	if err != nil {
		return err
	}
	prevS, prev3, err := share(to.AddDate(0, 0, -7*trendWeeks))
	if err != nil {
		return err
	}
	e.Trends["share_of_voice"] = trendOf(curS, prevS)
	e.Trends["share_of_voice_top3"] = trendOf(cur3, prev3)
	if e.Period != nil {
		prevS, prev3, err = share(before)
		if err != nil {
			return err
		}
		e.Period["share_of_voice"] = trendOf(curS, prevS)
		e.Period["share_of_voice_top3"] = trendOf(cur3, prev3)
	}
	return nil
}

// weeklyTrendAvg compares the mean of the last trendWeeks weeks with the
// mean of the weeks before. When lowerIsBetter (search position), a drop
// is "up".
func weeklyTrendAvg(weeks []seoOverviewWeek, v func(seoOverviewWeek) *float64, lowerIsBetter bool) seoTrend {
	n := len(weeks)
	if n < 2*trendWeeks {
		return trendOf(nil, nil)
	}
	return meanTrend(weeks[n-trendWeeks:], weeks[n-2*trendWeeks:n-trendWeeks], v, lowerIsBetter)
}

// meanTrend compares the mean of one run of weeks with another's. When
// lowerIsBetter (search position), a drop is "up".
func meanTrend(cur, prev []seoOverviewWeek, v func(seoOverviewWeek) *float64, lowerIsBetter bool) seoTrend {
	mean := func(ws []seoOverviewWeek) *float64 {
		var t float64
		n := 0
		for _, w := range ws {
			if x := v(w); x != nil {
				t += *x
				n++
			}
		}
		if n == 0 {
			return nil
		}
		t /= float64(n)
		return &t
	}
	t := trendOf(mean(cur), mean(prev))
	if lowerIsBetter && (t.Direction == "up" || t.Direction == "down") {
		if t.Direction == "up" {
			t.Direction = "down"
		} else {
			t.Direction = "up"
		}
		if t.Change != nil {
			c := -*t.Change
			t.Change = &c
		}
	}
	return t
}
