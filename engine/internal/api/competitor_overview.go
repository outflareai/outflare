package api

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/seosync"
)

// The competition overview: who is dominating, and is the brand winning?
// For the brand and each competitor it tracks, it reads what Outflare
// already stores (SEO snapshots, AI answers, Presence) and works out each
// one's share of every area, an overall score out of 100, the rank, how
// the rank moved over the last 28 days, and for the brand where it leads,
// where it trails and whether the gap is closing. It reads only stored
// rows, so opening it costs nothing.

func (s *Server) routesCompetition(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/brands/{brand}/competition", s.onBrand(auth.ScopeSEORead, s.competitionOverview))
}

// An area of competition. Each is a total the brand and its competitors
// can be compared on; its share is one's part of everyone's total.
type compArea struct {
	Key, Label, Unit string
	// Needs says which connection fills it, for an area nobody has data in.
	Needs string
}

var compAreas = []compArea{
	{"search", "Search visits", "visits a month", "an SEO connection (Ahrefs or DataForSEO)"},
	{"keywords", "Keywords in the top 3", "keywords", "an SEO connection (Ahrefs or DataForSEO)"},
	{"links", "Linking sites", "sites", "an SEO connection (Ahrefs or DataForSEO)"},
	{"ai", "AI answers", "answers that name them, last 28 days", "AI visibility questions"},
	{"placements", "Placements", "live pages", "Presence"},
	{"social", "Social followers", "followers across followed profiles", "social profiles (the brand's links, and each competitor's confirmed profiles) and a page reader (Geonode)"},
	{"reviews", "Reviews", "reviews on followed review pages", "review pages (the brand's links, and each competitor's confirmed profiles) and a page reader (Geonode)"},
}

// compWindow is the "now" and "then" the gap's direction compares.
const compWindow = 28 * 24 * time.Hour

// A value one brand or competitor has in an area at a time.
type compPoint struct {
	Day   string
	Value float64
}

type compEntity struct {
	ID, Kind, Name, Host string
	// Area values over time, oldest first, one point a week at most.
	Series map[string][]compPoint
}

type compAreaValue struct {
	Value       *float64 `json:"value"`
	Share       *float64 `json:"share"`
	ShareBefore *float64 `json:"share_before"`
	Rank        *int     `json:"rank"`
}

type compEntry struct {
	ID          string                   `json:"id"`
	Kind        string                   `json:"kind"`
	Name        string                   `json:"name"`
	Host        string                   `json:"host"`
	Score       *float64                 `json:"score"`
	ScoreBefore *float64                 `json:"score_before"`
	Rank        *int                     `json:"rank"`
	RankBefore  *int                     `json:"rank_before"`
	Areas       map[string]compAreaValue `json:"areas"`
	Measured    int                      `json:"measured_areas"`
}

type compAreaView struct {
	Key      string  `json:"key"`
	Label    string  `json:"label"`
	Unit     string  `json:"unit"`
	Weight   float64 `json:"weight"`
	Measured bool    `json:"measured"`
	Needs    string  `json:"needs"`
	LeaderID *string `json:"leader_id"`
}

// How the brand stands in one area against its competitors.
type compStanding struct {
	Area string `json:"area"`
	// leading, ahead, level, behind or far_behind.
	Standing string `json:"standing"`
	// gaining, steady, losing or unknown: its share now against 28 days ago.
	Trend       string   `json:"trend"`
	Share       *float64 `json:"share"`
	ShareBefore *float64 `json:"share_before"`
}

// A pass the trends point to: the brand passing the one just ahead, or
// the one just behind passing the brand.
type compCatchUp struct {
	Area         string  `json:"area"`
	CompetitorID string  `json:"competitor_id"`
	Direction    string  `json:"direction"` // you_pass or they_pass
	Weeks        float64 `json:"weeks"`
}

type compSeriesPoint struct {
	Day    string             `json:"day"`
	Shares map[string]float64 `json:"shares"`
}

type compOverview struct {
	BrandID   string                       `json:"brand_id"`
	AsOf      string                       `json:"as_of"`
	Before    string                       `json:"before"`
	Areas     []compAreaView               `json:"areas"`
	Entries   []compEntry                  `json:"entries"`
	Standings []compStanding               `json:"standings"`
	CatchUps  []compCatchUp                `json:"catch_ups"`
	Series    map[string][]compSeriesPoint `json:"series"`
	Notes     []string                     `json:"notes"`
	Tracked   int                          `json:"tracked"`
	NoWebsite bool                         `json:"no_website"`
}

func (s *Server) competitionOverview(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	weights, err := compWeights(r.URL.Query().Get("weights"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	now := time.Now().UTC()
	ents, noSite, err := s.compEntities(r.Context(), a.WorkspaceID, a.Brand, now)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	out := computeCompetition(ents, weights, now)
	out.BrandID = a.Brand
	out.NoWebsite = noSite
	out.Tracked = len(ents) - 1
	writeJSON(w, http.StatusOK, out)
}

// compWeights reads "search:2,ai:1": how much each area counts in the
// overall score, 0 to 10. Areas left out count 1.
func compWeights(raw string) (map[string]float64, error) {
	out := map[string]float64{}
	for _, a := range compAreas {
		out[a.Key] = 1
	}
	if strings.TrimSpace(raw) == "" {
		return out, nil
	}
	for _, part := range strings.Split(raw, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), ":")
		if _, known := out[k]; !ok || !known {
			return nil, errInvalid("weights is area:weight pairs, such as search:2,ai:1; areas are search, keywords, links, ai, placements, social and reviews")
		}
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || n < 0 || n > 10 || math.IsNaN(n) {
			return nil, errInvalid("each weight is a number from 0 to 10")
		}
		out[k] = n
	}
	return out, nil
}

// compEntities reads the brand and its tracked competitors with every
// area's values over the last year.
func (s *Server) compEntities(ctx context.Context, ws, brand string, now time.Time) ([]compEntity, bool, error) {
	var name string
	var domains []string
	if err := s.pool.QueryRow(ctx, `SELECT name, domains FROM brands WHERE workspace_id = $1 AND id = $2`, ws, brand).Scan(&name, &domains); err != nil {
		return nil, false, err
	}
	rows, err := s.pool.Query(ctx, `SELECT host FROM seo_sites WHERE workspace_id = $1 AND brand_id = $2 ORDER BY created_at`, ws, brand)
	if err != nil {
		return nil, false, err
	}
	var siteHosts []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			rows.Close()
			return nil, false, err
		}
		siteHosts = append(siteHosts, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	hosts := []string{}
	for _, h := range append(slices.Clone(siteHosts), domains...) {
		h = seosync.LinkTarget(strings.ToLower(strings.TrimSpace(h)))
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h, "www."+h)
		}
	}
	you := compEntity{ID: "brand", Kind: "brand", Name: name, Series: map[string][]compPoint{}}
	if len(hosts) > 0 {
		you.Host = hosts[0]
	}
	ents := []compEntity{you}
	names := map[string]int{strings.ToLower(name): 0}

	rows, err = s.pool.Query(ctx, `SELECT DISTINCT ON (host) id::text, host, name, aliases FROM seo_competitors
		WHERE workspace_id = $1 AND brand_id = $2 AND status = 'tracked'
		ORDER BY host, created_at`, ws, brand)
	if err != nil {
		return nil, false, err
	}
	for rows.Next() {
		var e compEntity
		var aliases []string
		if err := rows.Scan(&e.ID, &e.Host, &e.Name, &aliases); err != nil {
			rows.Close()
			return nil, false, err
		}
		e.Kind, e.Series = "competitor", map[string][]compPoint{}
		if e.Name == "" {
			e.Name = e.Host
		}
		ents = append(ents, e)
		for _, n := range append([]string{e.Name, e.Host}, aliases...) {
			if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
				if _, taken := names[n]; !taken {
					names[n] = len(ents) - 1
				}
			}
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	from := now.AddDate(-1, 0, -7)

	// SEO: one connector for everyone, so the numbers are on one scale:
	// the one with the newest overview of the brand's own site, else of
	// any competitor, Ahrefs first on a tie.
	all := slices.Clone(hosts)
	byHost := map[string]int{}
	for _, h := range hosts {
		byHost[h] = 0
	}
	for i, e := range ents[1:] {
		all = append(all, e.Host, "www."+e.Host)
		byHost[e.Host], byHost["www."+e.Host] = i+1, i+1
	}
	var conn string
	err = s.pool.QueryRow(ctx, `SELECT connector FROM seo_snapshots
		WHERE workspace_id = $1 AND report = 'overview' AND key = '' AND host = ANY($2) AND day >= $3
		ORDER BY host <> ALL($4), day DESC, connector <> 'ahrefs' LIMIT 1`, ws, all, from, hosts).Scan(&conn)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, false, err
	}
	if conn != "" {
		rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON (host, wk) host, to_char(day, 'YYYY-MM-DD'), metrics
			FROM (SELECT host, day, date_trunc('week', day) AS wk, metrics, synced_at FROM seo_snapshots
				WHERE workspace_id = $1 AND connector = $2 AND report = 'overview' AND key = '' AND host = ANY($3) AND day >= $4) x
			ORDER BY host, wk, day DESC, synced_at DESC`, ws, conn, all, from)
		if err != nil {
			return nil, false, err
		}
		// The brand's own value is its biggest host's on each day.
		perEntity := map[int]map[string]map[string]float64{}
		for rows.Next() {
			var host, day string
			var raw []byte
			if err := rows.Scan(&host, &day, &raw); err != nil {
				rows.Close()
				return nil, false, err
			}
			var m struct {
				OT *float64 `json:"organic_traffic"`
				T3 *float64 `json:"top3"`
				RD *float64 `json:"referring_domains"`
			}
			_ = json.Unmarshal(raw, &m)
			i := byHost[host]
			if perEntity[i] == nil {
				perEntity[i] = map[string]map[string]float64{}
			}
			for area, v := range map[string]*float64{"search": m.OT, "keywords": m.T3, "links": m.RD} {
				if v == nil {
					continue
				}
				if perEntity[i][area] == nil {
					perEntity[i][area] = map[string]float64{}
				}
				if old, ok := perEntity[i][area][day]; !ok || *v > old {
					perEntity[i][area][day] = *v
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		for i, areas := range perEntity {
			for area, days := range areas {
				for d, v := range days {
					ents[i].Series[area] = append(ents[i].Series[area], compPoint{d, v})
				}
			}
		}
	}

	// AI answers: each week, how many of the brand's sampled answers name
	// each one, over the 28 days to that week's end.
	rows, err = s.pool.Query(ctx, `SELECT to_char(LEAST(w.wk + interval '6 days', $4::timestamptz), 'YYYY-MM-DD'), m.entity, lower(m.name), count(DISTINCT m.sample_id)
		FROM generate_series(date_trunc('week', $3::timestamptz), date_trunc('week', $4::timestamptz), interval '1 week') AS w(wk)
		JOIN ai_mentions m ON m.workspace_id = $1 AND m.sampled_at > w.wk + interval '7 days' - interval '28 days' AND m.sampled_at <= w.wk + interval '7 days'
		JOIN ai_samples a ON a.id = m.sample_id AND a.brand_id = $2
		GROUP BY 1, 2, 3`, ws, brand, from, now)
	if err != nil {
		return nil, false, err
	}
	ai := map[int]map[string]float64{}
	for rows.Next() {
		var day, entity, n string
		var c float64
		if err := rows.Scan(&day, &entity, &n, &c); err != nil {
			rows.Close()
			return nil, false, err
		}
		i, ok := names[n]
		if entity == "brand" {
			i, ok = 0, true
		}
		if !ok {
			continue
		}
		if ai[i] == nil {
			ai[i] = map[string]float64{}
		}
		ai[i][day] += c
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	if len(ai) > 0 {
		// Everyone measured gets a value each week, zero when not named.
		days := map[string]bool{}
		for _, m := range ai {
			for d := range m {
				days[d] = true
			}
		}
		for i := range ents {
			for d := range days {
				ents[i].Series["ai"] = append(ents[i].Series["ai"], compPoint{d, ai[i][d]})
			}
		}
	}

	// Placements: live pages each week. The brand's are its live Presence
	// placements; a competitor's, the pages Presence found for it and still
	// sees.
	if len(ents) > 1 {
		ids := make([]string, 0, len(ents)-1)
		for _, e := range ents[1:] {
			ids = append(ids, e.ID)
		}
		rows, err = s.pool.Query(ctx, `SELECT to_char(LEAST(w.wk + interval '6 days', $4::timestamptz), 'YYYY-MM-DD'), p.competitor_id::text, count(*)
			FROM generate_series(date_trunc('week', $3::timestamptz), date_trunc('week', $4::timestamptz), interval '1 week') AS w(wk)
			JOIN coverage_competitor_placements p ON p.workspace_id = $1 AND p.brand_id = $2 AND p.competitor_id = ANY($5::uuid[])
				AND p.first_seen_at <= w.wk + interval '7 days' AND (p.gone_at IS NULL OR p.gone_at > w.wk + interval '7 days')
			GROUP BY 1, 2`, ws, brand, from, now, ids)
		if err != nil {
			return nil, false, err
		}
		idx := map[string]int{}
		for i, e := range ents {
			idx[e.ID] = i
		}
		place := map[int]map[string]float64{}
		for rows.Next() {
			var day, id string
			var c float64
			if err := rows.Scan(&day, &id, &c); err != nil {
				rows.Close()
				return nil, false, err
			}
			if place[idx[id]] == nil {
				place[idx[id]] = map[string]float64{}
			}
			place[idx[id]][day] = c
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, false, err
		}
		if len(place) > 0 {
			rows, err = s.pool.Query(ctx, `SELECT to_char(LEAST(w.wk + interval '6 days', $4::timestamptz), 'YYYY-MM-DD'), count(p.id)
				FROM generate_series(date_trunc('week', $3::timestamptz), date_trunc('week', $4::timestamptz), interval '1 week') AS w(wk)
				LEFT JOIN coverage_placements p ON p.workspace_id = $1 AND p.brand_id = $2 AND p.stage = 'live' AND p.created_at <= w.wk + interval '7 days'
				GROUP BY 1`, ws, brand, from, now)
			if err != nil {
				return nil, false, err
			}
			own := map[string]float64{}
			for rows.Next() {
				var day string
				var c float64
				if err := rows.Scan(&day, &c); err != nil {
					rows.Close()
					return nil, false, err
				}
				own[day] = c
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, false, err
			}
			place[0] = own
			for i := range ents {
				for d := range own {
					ents[i].Series["placements"] = append(ents[i].Series["placements"], compPoint{d, place[i][d]})
				}
			}
		}
	}
	// Social followers and reviews: each week, the sum over the followed
	// profiles of each one's latest count by that week's end. The brand's
	// are its own accounts and pages, a competitor's the ones a person
	// confirmed for it.
	if err := s.compProfileTotals(ctx, ws, brand, from, now, ents); err != nil {
		return nil, false, err
	}
	for i := range ents {
		for k := range ents[i].Series {
			sort.Slice(ents[i].Series[k], func(a, b int) bool { return ents[i].Series[k][a].Day < ents[i].Series[k][b].Day })
		}
	}
	return ents, len(hosts) == 0, nil
}

// compProfileTotals adds the social and reviews areas' weekly values.
func (s *Server) compProfileTotals(ctx context.Context, ws, brand string, from, now time.Time, ents []compEntity) error {
	idx := map[string]int{"brand": 0}
	ids := []string{}
	for i, e := range ents[1:] {
		idx[e.ID] = i + 1
		ids = append(ids, e.ID)
	}
	for area, sql := range map[string]string{
		"social": `
			WITH f AS (SELECT a.id, COALESCE(a.competitor_id::text, 'brand') AS who FROM social_accounts a
				WHERE a.workspace_id = $1 AND a.brand_id = $2 AND a.status = 'active' AND (a.competitor_id IS NULL OR a.competitor_id = ANY($5::uuid[])))
			SELECT to_char(LEAST(w.wk + interval '6 days', $4::timestamptz), 'YYYY-MM-DD'), f.who, sum(last.n)::float8
			FROM generate_series(date_trunc('week', $3::timestamptz), date_trunc('week', $4::timestamptz), interval '1 week') AS w(wk)
			CROSS JOIN f
			JOIN LATERAL (SELECT d.followers AS n FROM social_account_days d WHERE d.account_id = f.id AND d.followers IS NOT NULL
				AND d.day <= (w.wk + interval '6 days')::date ORDER BY d.day DESC LIMIT 1) last ON true
			GROUP BY 1, 2`,
		"reviews": `
			WITH f AS (SELECT p.id, COALESCE(p.competitor_id::text, 'brand') AS who FROM review_pages p
				WHERE p.workspace_id = $1 AND p.brand_id = $2 AND p.status = 'active' AND (p.competitor_id IS NULL OR p.competitor_id = ANY($5::uuid[])))
			SELECT to_char(LEAST(w.wk + interval '6 days', $4::timestamptz), 'YYYY-MM-DD'), f.who, sum(last.n)::float8
			FROM generate_series(date_trunc('week', $3::timestamptz), date_trunc('week', $4::timestamptz), interval '1 week') AS w(wk)
			CROSS JOIN f
			JOIN LATERAL (SELECT d.review_count AS n FROM review_page_days d WHERE d.page_id = f.id AND d.review_count IS NOT NULL
				AND d.day <= (w.wk + interval '6 days')::date ORDER BY d.day DESC LIMIT 1) last ON true
			GROUP BY 1, 2`,
	} {
		rows, err := s.pool.Query(ctx, sql, ws, brand, from, now, ids)
		if err != nil {
			return err
		}
		var day, who string
		var n float64
		if _, err := pgx.ForEachRow(rows, []any{&day, &who, &n}, func() error {
			if i, ok := idx[who]; ok {
				ents[i].Series[area] = append(ents[i].Series[area], compPoint{day, n})
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// valueAt is an entity's latest value in an area on or before day, and
// not older than 35 days before it (a weekly read can be late).
func valueAt(pts []compPoint, day time.Time) *float64 {
	end := day.Format(time.DateOnly)
	start := day.Add(-35 * 24 * time.Hour).Format(time.DateOnly)
	for i := len(pts) - 1; i >= 0; i-- {
		if pts[i].Day <= end {
			if pts[i].Day < start {
				return nil
			}
			v := pts[i].Value
			return &v
		}
	}
	return nil
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// scoreAt works out every entity's area values, shares and overall score
// at one time. A score is the weighted mean, over the areas an entity is
// measured in, of its value against the area's leader (100 for the
// leader), so a brand that leads everywhere scores 100.
func scoreAt(ents []compEntity, weights map[string]float64, at time.Time) ([]map[string]compAreaValue, []*float64, []int) {
	vals := make([]map[string]compAreaValue, len(ents))
	scores := make([]*float64, len(ents))
	measured := make([]int, len(ents))
	sums := make([]float64, len(ents))
	wsum := make([]float64, len(ents))
	for i := range ents {
		vals[i] = map[string]compAreaValue{}
	}
	for _, a := range compAreas {
		var total, top float64
		got := make([]*float64, len(ents))
		for i, e := range ents {
			got[i] = valueAt(e.Series[a.Key], at)
			if got[i] != nil {
				total += *got[i]
				top = max(top, *got[i])
			}
		}
		order := []int{}
		for i := range ents {
			if got[i] != nil {
				order = append(order, i)
			}
		}
		sort.SliceStable(order, func(x, y int) bool { return *got[order[x]] > *got[order[y]] })
		for pos, i := range order {
			v := compAreaValue{Value: got[i]}
			rank := pos + 1
			if pos > 0 && *got[order[pos-1]] == *got[i] {
				rank = *vals[order[pos-1]][a.Key].Rank
			}
			v.Rank = &rank
			if total > 0 {
				sh := round1(*got[i] / total * 100)
				v.Share = &sh
			}
			vals[i][a.Key] = v
			measured[i]++
			if w := weights[a.Key]; w > 0 && top > 0 {
				sums[i] += w * (*got[i] / top * 100)
				wsum[i] += w
			}
		}
	}
	for i := range ents {
		if wsum[i] > 0 {
			sc := math.Round(sums[i] / wsum[i])
			scores[i] = &sc
		}
	}
	return vals, scores, measured
}

func ranksOf(scores []*float64) []*int {
	order := []int{}
	for i, s := range scores {
		if s != nil {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(x, y int) bool { return *scores[order[x]] > *scores[order[y]] })
	out := make([]*int, len(scores))
	for pos, i := range order {
		rank := pos + 1
		if pos > 0 && *scores[order[pos-1]] == *scores[i] {
			rank = *out[order[pos-1]]
		}
		out[i] = &rank
	}
	return out
}

func computeCompetition(ents []compEntity, weights map[string]float64, now time.Time) compOverview {
	before := now.Add(-compWindow)
	out := compOverview{
		AsOf: now.Format(time.DateOnly), Before: before.Format(time.DateOnly),
		Entries: []compEntry{}, Standings: []compStanding{}, CatchUps: []compCatchUp{}, Series: map[string][]compSeriesPoint{},
		Notes: []string{
			"Search numbers are the SEO connection's estimates, all from one connection so they are on one scale.",
			"A score compares each brand with the leader of every area it is measured in: 100 means it leads them all.",
		},
	}
	nowVals, nowScores, measured := scoreAt(ents, weights, now)
	beforeVals, beforeScores, _ := scoreAt(ents, weights, before)
	nowRanks, beforeRanks := ranksOf(nowScores), ranksOf(beforeScores)
	for i, e := range ents {
		areas := map[string]compAreaValue{}
		for k, v := range nowVals[i] {
			v.ShareBefore = beforeVals[i][k].Share
			areas[k] = v
		}
		out.Entries = append(out.Entries, compEntry{ID: e.ID, Kind: e.Kind, Name: e.Name, Host: e.Host,
			Score: nowScores[i], ScoreBefore: beforeScores[i], Rank: nowRanks[i], RankBefore: beforeRanks[i], Areas: areas, Measured: measured[i]})
	}
	for _, a := range compAreas {
		view := compAreaView{Key: a.Key, Label: a.Label, Unit: a.Unit, Weight: weights[a.Key], Needs: a.Needs}
		for _, en := range out.Entries {
			if v, ok := en.Areas[a.Key]; ok {
				view.Measured = true
				if v.Rank != nil && *v.Rank == 1 && view.LeaderID == nil {
					id := en.ID
					view.LeaderID = &id
				}
			}
		}
		out.Areas = append(out.Areas, view)
		if view.Measured {
			out.Series[a.Key] = shareSeries(ents, a.Key, now)
		}
	}
	if len(ents) < 2 {
		return out
	}
	you := out.Entries[0]
	for _, a := range compAreas {
		v, ok := you.Areas[a.Key]
		if !ok || v.Value == nil {
			continue
		}
		others := []float64{}
		for _, en := range out.Entries[1:] {
			if ov, ok := en.Areas[a.Key]; ok && ov.Value != nil {
				others = append(others, *ov.Value)
			}
		}
		if len(others) == 0 {
			continue
		}
		st := compStanding{Area: a.Key, Standing: standing(*v.Value, *v.Rank, others), Trend: "unknown", Share: v.Share, ShareBefore: v.ShareBefore}
		if v.Share != nil && v.ShareBefore != nil {
			switch d := *v.Share - *v.ShareBefore; {
			case d >= 1:
				st.Trend = "gaining"
			case d <= -1:
				st.Trend = "losing"
			default:
				st.Trend = "steady"
			}
		}
		out.Standings = append(out.Standings, st)
		out.CatchUps = append(out.CatchUps, catchUps(ents, a.Key, now)...)
	}
	return out
}

// standing places the brand's value against its competitors': leading
// when it ranks first, otherwise against the median competitor.
func standing(you float64, rank int, others []float64) string {
	if rank == 1 {
		return "leading"
	}
	sorted := slices.Clone(others)
	slices.Sort(sorted)
	n := len(sorted)
	median := sorted[n/2]
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	switch {
	case median <= 0 || you >= median*1.1:
		return "ahead"
	case you >= median*0.9:
		return "level"
	case you >= median*0.25:
		return "behind"
	default:
		return "far_behind"
	}
}

// shareSeries is every entity's share of an area each week over the year.
func shareSeries(ents []compEntity, area string, now time.Time) []compSeriesPoint {
	days := map[string]bool{}
	for _, e := range ents {
		for _, p := range e.Series[area] {
			days[p.Day] = true
		}
	}
	list := make([]string, 0, len(days))
	for d := range days {
		list = append(list, d)
	}
	slices.Sort(list)
	out := []compSeriesPoint{}
	// One point a week: the last day seen in each week.
	week := func(d string) string {
		t, _ := time.Parse(time.DateOnly, d)
		y, w := t.ISOWeek()
		return strconv.Itoa(y) + "-" + strconv.Itoa(w)
	}
	for i, d := range list {
		if i+1 < len(list) && week(list[i+1]) == week(d) {
			continue
		}
		t, _ := time.Parse(time.DateOnly, d)
		var total float64
		vals := map[string]float64{}
		for _, e := range ents {
			if v := valueAt(e.Series[area], t); v != nil {
				vals[e.ID] = *v
				total += *v
			}
		}
		if total <= 0 {
			continue
		}
		p := compSeriesPoint{Day: d, Shares: map[string]float64{}}
		for id, v := range vals {
			p.Shares[id] = round1(v / total * 100)
		}
		out = append(out, p)
	}
	return out
}

// slope fits a line to the last 12 weeks of a series, in value a week. It
// needs six points over at least six weeks, or there's no steady trend.
func slope(pts []compPoint, now time.Time) (float64, bool) {
	start := now.AddDate(0, 0, -84).Format(time.DateOnly)
	var xs, ys []float64
	for _, p := range pts {
		if p.Day < start {
			continue
		}
		t, _ := time.Parse(time.DateOnly, p.Day)
		xs = append(xs, now.Sub(t).Hours()/-168)
		ys = append(ys, p.Value)
	}
	if len(xs) < 6 || xs[len(xs)-1]-xs[0] < 6 {
		return 0, false
	}
	var mx, my float64
	for i := range xs {
		mx += xs[i]
		my += ys[i]
	}
	mx /= float64(len(xs))
	my /= float64(len(ys))
	var num, den float64
	for i := range xs {
		num += (xs[i] - mx) * (ys[i] - my)
		den += (xs[i] - mx) * (xs[i] - mx)
	}
	if den == 0 {
		return 0, false
	}
	return num / den, true
}

// maxCatchUpWeeks is the furthest out a pass is shown: past two years a
// trend says nothing.
const maxCatchUpWeeks = 104

// catchUps finds, in one area, when the brand passes the competitor just
// ahead of it and when the one just behind passes the brand, if the last
// 12 weeks keep going.
func catchUps(ents []compEntity, area string, now time.Time) []compCatchUp {
	you := valueAt(ents[0].Series[area], now)
	ys, ok := slope(ents[0].Series[area], now)
	if you == nil || !ok {
		return nil
	}
	ahead, behind := -1, -1
	for i, e := range ents[1:] {
		v := valueAt(e.Series[area], now)
		if v == nil {
			continue
		}
		if *v > *you && (ahead < 0 || *v < *valueAt(ents[ahead].Series[area], now)) {
			ahead = i + 1
		}
		if *v <= *you && (behind < 0 || *v > *valueAt(ents[behind].Series[area], now)) {
			behind = i + 1
		}
	}
	out := []compCatchUp{}
	pass := func(j int, dir string) {
		if j < 0 {
			return
		}
		ts, ok := slope(ents[j].Series[area], now)
		if !ok {
			return
		}
		gap := math.Abs(*valueAt(ents[j].Series[area], now) - *you)
		closing := ys - ts
		if dir == "they_pass" {
			closing = ts - ys
		}
		if closing <= 0 {
			return
		}
		if weeks := math.Ceil(gap/closing - 0.01); weeks <= maxCatchUpWeeks {
			out = append(out, compCatchUp{Area: area, CompetitorID: ents[j].ID, Direction: dir, Weeks: weeks})
		}
	}
	pass(ahead, "you_pass")
	pass(behind, "they_pass")
	return out
}
