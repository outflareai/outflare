package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/aivis"
)

// The AI visibility overview: for each cohort, over the last days, how
// often AI answers name the brand (mention rate), its share of all the
// brands named (share of voice), where it comes in the answer, how often
// they cite its pages, and the same for each competitor, by model and by
// week. Everything is counted per answer: an answer that names the brand
// three times counts once.

type aiVisEntity struct {
	Name string `json:"name"`
	// Answers naming it, out of the cohort's answers.
	Answers     int      `json:"answers"`
	MentionRate *float64 `json:"mention_rate"`
	// Its answers out of every brand's and competitor's answers.
	ShareOfVoice *float64 `json:"share_of_voice"`
	AvgPosition  *float64 `json:"avg_position"`
	// Answers recommending it, listing it and warning against it.
	Recommended   int      `json:"recommended"`
	Listed        int      `json:"listed"`
	Warned        int      `json:"warned"`
	RecommendRate *float64 `json:"recommend_rate"`
	aiVisScore
	// The benchmark: stored AI answers mentioning it on the latest day
	// counted, its share of all the cohort's brands', and the change since
	// the count before.
	BenchmarkMentions *int     `json:"benchmark_mentions"`
	BenchmarkShare    *float64 `json:"benchmark_share"`
	BenchmarkChange   *int     `json:"benchmark_change"`
}

type aiVisBrand struct {
	aiVisEntity
	// Answers citing one of the brand's pages, out of all answers.
	CitationRate *float64 `json:"citation_rate"`
}

type aiVisModel struct {
	Provider    string   `json:"provider"`
	Model       string   `json:"model"`
	WebSearch   bool     `json:"web_search"`
	Answers     int      `json:"answers"`
	MentionRate *float64 `json:"mention_rate"`
}

type aiVisHost struct {
	Host       string `json:"host"`
	Owner      string `json:"owner"`
	Competitor string `json:"competitor"`
	SourceType string `json:"source_type"`
	Answers    int    `json:"answers"`
}

type aiVisCount struct {
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Answers int    `json:"answers"`
	// Host is the brand's website, when AI visibility put it on the
	// Competitors module's list (a suggested competitor), and its id there.
	Host         string `json:"host,omitempty"`
	CompetitorID string `json:"competitor_id,omitempty"`
}

// aiVisOther is a company or tool the answers name that isn't a
// competitor, what it is, and its website.
type aiVisOther struct {
	Name    string `json:"name"`
	Label   string `json:"label"`
	Host    string `json:"host"`
	Answers int    `json:"answers"`
}

type aiVisWeek struct {
	Week         string   `json:"week"`
	Answers      int      `json:"answers"`
	MentionRate  *float64 `json:"mention_rate"`
	ShareOfVoice *float64 `json:"share_of_voice"`
	aiVisScore
	// RealChange: the score's range doesn't overlap the week before's.
	RealChange bool `json:"real_change"`
}

// aiVisFacts is how accurate the answers naming the brand were about its
// facts.
type aiVisFacts struct {
	// Answers checked against the facts, and those contradicting at least
	// one of them.
	Checked int `json:"checked_answers"`
	Wrong   int `json:"wrong_answers"`
	// Checked answers with nothing wrong, out of all checked.
	Accuracy *float64 `json:"accuracy"`
	// Facts at least one answer contradicted.
	FactsWrong int `json:"facts_wrong"`
}

type aiVisCohort struct {
	Cohort struct {
		ID      string  `json:"id"`
		Name    string  `json:"name"`
		BrandID *string `json:"brand_id"`
	} `json:"cohort"`
	Answers     int           `json:"answers"`
	Brand       aiVisBrand    `json:"brand"`
	Competitors []aiVisEntity `json:"competitors"`
	// Brands answers recommend or list that the cohort doesn't track.
	Suggested   []aiVisCount `json:"suggested_competitors"`
	Others      []aiVisOther `json:"named_others"`
	SourceTypes []aiVisCount `json:"source_types"`
	Models      []aiVisModel `json:"models"`
	CitedHosts  []aiVisHost  `json:"cited_hosts"`
	Weeks       []aiVisWeek  `json:"weeks"`
	Facts       aiVisFacts   `json:"facts"`
	OpenFixes   int          `json:"open_fixes"`
	// BenchmarkDay is the latest day the benchmark was counted, or null.
	BenchmarkDay *string `json:"benchmark_day"`
	// Grid is the brand by awareness level and question type.
	Grid []aiVisCell `json:"grid"`
	// Traffic is what AI answers brought the brand's website; null for a
	// cohort without a brand.
	Traffic *aiVisTraffic `json:"traffic"`
}

func rate(n, of int) *float64 {
	if of == 0 {
		return nil
	}
	v := math.Round(float64(n)/float64(of)*1000) / 1000
	return &v
}

func (s *Server) getAIVisibility(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	sel, ok := s.brandFilter(w, r)
	if !ok {
		return
	}
	days := 28
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	out, err := s.aiVisibilityOverview(r.Context(), a.WorkspaceID, sel, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "days": days, "from": from.Format(time.RFC3339)})
}

func (s *Server) aiVisibilityOverview(ctx context.Context, ws string, sel brandSel, from time.Time) ([]aiVisCohort, error) {
	args := []any{ws}
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name, c.brand_id::text, COALESCE(b.name, '')
		FROM ai_cohorts c LEFT JOIN brands b ON b.id = COALESCE(c.brand_id, outflare_first_brand(c.workspace_id))
		WHERE c.workspace_id = $1`+sel.sql("c.brand_id", &args)+` ORDER BY c.name`, args...)
	if err != nil {
		return nil, err
	}
	var list []*aiVisCohort
	byID := map[string]*aiVisCohort{}
	var ids []string
	var id, name, brandName string
	var brand *string
	if _, err := pgx.ForEachRow(rows, []any{&id, &name, &brand, &brandName}, func() error {
		c := &aiVisCohort{Competitors: []aiVisEntity{}, Suggested: []aiVisCount{}, Others: []aiVisOther{}, SourceTypes: []aiVisCount{}, Models: []aiVisModel{}, CitedHosts: []aiVisHost{}, Weeks: []aiVisWeek{}}
		c.Cohort.ID, c.Cohort.Name, c.Cohort.BrandID = id, name, brand
		c.Brand.Name = brandName
		list = append(list, c)
		byID[id] = c
		ids = append(ids, id)
		return nil
	}); err != nil {
		return nil, err
	}
	out := make([]aiVisCohort, 0, len(list))
	if len(ids) == 0 {
		return out, nil
	}
	// Answers per cohort, and how many name or cite the brand.
	rows, err = s.pool.Query(ctx, `
		SELECT s.cohort_id, count(*),
		       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ai_citations c WHERE c.sample_id = s.id AND c.owner = 'brand'))
		FROM ai_samples s WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY s.cohort_id`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var n, cited int
	if _, err := pgx.ForEachRow(rows, []any{&id, &n, &cited}, func() error {
		byID[id].Answers = n
		byID[id].Brand.CitationRate = rate(cited, n)
		return nil
	}); err != nil {
		return nil, err
	}
	// Each brand and competitor: answers naming it, and where.
	rows, err = s.pool.Query(ctx, `
		SELECT m.cohort_id, m.entity, m.name, count(*), avg(m.position)::float8,
		       count(*) FILTER (WHERE m.stance = 'recommended'), count(*) FILTER (WHERE m.stance = 'listed'), count(*) FILTER (WHERE m.stance = 'warned')
		FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND m.cohort_id = ANY($2) AND s.status = 'ok' AND m.sampled_at >= $3
		GROUP BY m.cohort_id, m.entity, m.name`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	totals := map[string]int{}
	var entity string
	var pos float64
	var rec, listed, warned int
	if _, err := pgx.ForEachRow(rows, []any{&id, &entity, &name, &n, &pos, &rec, &listed, &warned}, func() error {
		c := byID[id]
		p := math.Round(pos*10) / 10
		e := aiVisEntity{Name: name, Answers: n, MentionRate: rate(n, c.Answers), AvgPosition: &p,
			Recommended: rec, Listed: listed, Warned: warned, RecommendRate: rate(rec, c.Answers)}
		totals[id] += n
		if entity == "brand" {
			e.Name = c.Brand.Name
			c.Brand.aiVisEntity = e
		} else {
			c.Competitors = append(c.Competitors, e)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// By model.
	rows, err = s.pool.Query(ctx, `
		SELECT s.cohort_id, s.provider, s.model, s.web_search, count(*),
		       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand'))
		FROM ai_samples s WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY s.cohort_id, s.provider, s.model, s.web_search ORDER BY s.provider, s.model, s.web_search`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var m aiVisModel
	var named int
	if _, err := pgx.ForEachRow(rows, []any{&id, &m.Provider, &m.Model, &m.WebSearch, &m.Answers, &named}, func() error {
		m.MentionRate = rate(named, m.Answers)
		byID[id].Models = append(byID[id].Models, m)
		return nil
	}); err != nil {
		return nil, err
	}
	// The sites answers cite most.
	rows, err = s.pool.Query(ctx, `
		SELECT cohort_id, host, owner, competitor, source_type, answers FROM (
			SELECT c.cohort_id, c.host, min(c.owner) AS owner, min(c.competitor) AS competitor,
			       COALESCE(mode() WITHIN GROUP (ORDER BY c.source_type) FILTER (WHERE c.source_type <> ''), '') AS source_type,
			       count(DISTINCT c.sample_id) AS answers,
			       row_number() OVER (PARTITION BY c.cohort_id ORDER BY count(DISTINCT c.sample_id) DESC, c.host) AS rank
			FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
			WHERE c.workspace_id = $1 AND c.cohort_id = ANY($2) AND s.status = 'ok' AND c.sampled_at >= $3 AND `+aivis.NotAssetSQL("c.host")+`
			GROUP BY c.cohort_id, c.host) t
		WHERE rank <= 15 ORDER BY cohort_id, answers DESC, host`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var h aiVisHost
	if _, err := pgx.ForEachRow(rows, []any{&id, &h.Host, &h.Owner, &h.Competitor, &h.SourceType, &h.Answers}, func() error {
		byID[id].CitedHosts = append(byID[id].CitedHosts, h)
		return nil
	}); err != nil {
		return nil, err
	}
	// Brands the answers recommend or list that aren't on the brand's
	// competitor list yet: one said Yes or No to on Competitors (by name or
	// alias) is left out, and so is one AI visibility found is a listed
	// competitor's product, not a competitor, or not a brand
	// (ai_named_brands). The rest have their suggestion's row, or the
	// website found for them.
	rows, err = s.pool.Query(ctx, `
		SELECT t.cohort_id, t.name, t.answers, COALESCE(x.host, d.host, ''), COALESCE(x.id::text, '')
		FROM (
			SELECT s.cohort_id, s.brand_id, lower(b.name) AS k, min(b.name) AS name, count(DISTINCT s.id) AS answers
			FROM ai_samples s CROSS JOIN LATERAL unnest(s.other_brands) AS b(name)
			WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.sampled_at >= $3
			GROUP BY s.cohort_id, s.brand_id, lower(b.name)) t
		LEFT JOIN ai_named_brands d ON d.workspace_id = $1 AND d.brand_id = t.brand_id AND d.name_key = t.k
		LEFT JOIN LATERAL (SELECT y.id, y.host, y.status FROM seo_competitors y
			WHERE y.workspace_id = $1 AND y.brand_id = t.brand_id
			  AND (lower(y.name) = t.k OR t.k = ANY(SELECT lower(a) FROM unnest(y.aliases) AS a) OR y.id = d.competitor_id)
			ORDER BY y.status = 'suggested' DESC, y.created_at LIMIT 1) x ON true
		WHERE COALESCE(x.status, 'suggested') = 'suggested' AND COALESCE(d.kind, '') NOT IN ('other', 'not_brand')
		ORDER BY t.cohort_id, t.answers DESC, t.name`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var sc aiVisCount
	if _, err := pgx.ForEachRow(rows, []any{&id, &sc.Name, &sc.Answers, &sc.Host, &sc.CompetitorID}, func() error {
		if len(byID[id].Suggested) < 15 {
			byID[id].Suggested = append(byID[id].Suggested, sc)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Companies and tools the answers name that aren't competitors: a
	// library, a framework, a review site.
	rows, err = s.pool.Query(ctx, `
		SELECT s.cohort_id, min(d.name), d.label, d.host, count(DISTINCT s.id)::int
		FROM ai_samples s CROSS JOIN LATERAL unnest(s.other_brands) AS b(name)
		JOIN ai_named_brands d ON d.workspace_id = s.workspace_id AND d.brand_id = s.brand_id AND d.name_key = lower(b.name) AND d.kind = 'other'
		WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.sampled_at >= $3
		  AND NOT EXISTS (SELECT 1 FROM seo_competitors y WHERE y.workspace_id = s.workspace_id AND y.brand_id = s.brand_id
		                  AND (lower(y.name) = d.name_key OR d.name_key = ANY(SELECT lower(a) FROM unnest(y.aliases) AS a)))
		GROUP BY s.cohort_id, d.name_key, d.label, d.host
		ORDER BY s.cohort_id, count(DISTINCT s.id) DESC, d.name_key`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var on aiVisOther
	if _, err := pgx.ForEachRow(rows, []any{&id, &on.Name, &on.Label, &on.Host, &on.Answers}, func() error {
		if len(byID[id].Others) < 30 {
			byID[id].Others = append(byID[id].Others, on)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// What kind of sites they cite.
	rows, err = s.pool.Query(ctx, `
		SELECT c.cohort_id, c.source_type, count(DISTINCT c.sample_id)
		FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
		WHERE c.workspace_id = $1 AND c.cohort_id = ANY($2) AND s.status = 'ok' AND c.sampled_at >= $3 AND c.source_type <> ''
		GROUP BY 1, 2 ORDER BY 1, 3 DESC, 2`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var st aiVisCount
	if _, err := pgx.ForEachRow(rows, []any{&id, &st.Type, &st.Answers}, func() error {
		byID[id].SourceTypes = append(byID[id].SourceTypes, st)
		return nil
	}); err != nil {
		return nil, err
	}
	// Week by week (weeks start on Monday, UTC).
	rows, err = s.pool.Query(ctx, `
		SELECT s.cohort_id, to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*),
		       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand')),
		       COALESCE(sum((SELECT count(*) FROM ai_mentions m WHERE m.sample_id = s.id)), 0)
		FROM ai_samples s WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY 1, 2 ORDER BY 1, 2`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var week string
	var all int
	if _, err := pgx.ForEachRow(rows, []any{&id, &week, &n, &named, &all}, func() error {
		byID[id].Weeks = append(byID[id].Weeks, aiVisWeek{Week: week, Answers: n, MentionRate: rate(named, n), ShareOfVoice: rate(named, all)})
		return nil
	}); err != nil {
		return nil, err
	}
	if err := s.aiVisibilityScores(ctx, ws, ids, byID, from); err != nil {
		return nil, err
	}
	if err := s.aiVisibilityBenchmarks(ctx, ws, ids, byID); err != nil {
		return nil, err
	}
	if err := s.aiVisibilityTraffic(ctx, ws, ids, byID, from); err != nil {
		return nil, err
	}
	// Fact accuracy.
	rows, err = s.pool.Query(ctx, `
		SELECT s.cohort_id, count(*),
		       count(*) FILTER (WHERE EXISTS (SELECT 1 FROM ai_fact_checks k WHERE k.sample_id = s.id AND k.verdict = 'contradicted')),
		       (SELECT count(DISTINCT k.fact_id) FROM ai_fact_checks k
		        WHERE k.cohort_id = s.cohort_id AND k.workspace_id = $1 AND k.verdict = 'contradicted' AND k.sampled_at >= $3)
		FROM ai_samples s WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.facts_status = 'checked' AND s.sampled_at >= $3
		GROUP BY s.cohort_id`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var wrong, factsWrong int
	if _, err := pgx.ForEachRow(rows, []any{&id, &n, &wrong, &factsWrong}, func() error {
		byID[id].Facts = aiVisFacts{Checked: n, Wrong: wrong, Accuracy: rate(n-wrong, n), FactsWrong: factsWrong}
		return nil
	}); err != nil {
		return nil, err
	}
	// Open fixes on the cohorts' prompts.
	rows, err = s.pool.Query(ctx, `
		SELECT p.cohort_id, count(*) FROM fixes f JOIN ai_prompts p ON p.id = f.subject_id
		WHERE f.workspace_id = $1 AND f.source = 'ai_visibility' AND f.status IN ('open', 'in_progress') AND p.cohort_id = ANY($2)
		GROUP BY p.cohort_id`, ws, ids)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&id, &n}, func() error {
		byID[id].OpenFixes = n
		return nil
	}); err != nil {
		return nil, err
	}
	for _, c := range list {
		c.Brand.ShareOfVoice = rate(c.Brand.Answers, totals[c.Cohort.ID])
		if c.Brand.Answers == 0 {
			c.Brand.MentionRate, c.Brand.RecommendRate = rate(0, c.Answers), rate(0, c.Answers)
		}
		for i := range c.Competitors {
			c.Competitors[i].ShareOfVoice = rate(c.Competitors[i].Answers, totals[c.Cohort.ID])
		}
		sort.SliceStable(c.Competitors, func(i, j int) bool { return c.Competitors[i].Answers > c.Competitors[j].Answers })
		out = append(out, *c)
	}
	return out, nil
}
