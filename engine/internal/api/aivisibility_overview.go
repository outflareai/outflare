package api

import (
	"context"
	"math"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
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
	Answers    int    `json:"answers"`
}

type aiVisWeek struct {
	Week         string   `json:"week"`
	Answers      int      `json:"answers"`
	MentionRate  *float64 `json:"mention_rate"`
	ShareOfVoice *float64 `json:"share_of_voice"`
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
	Models      []aiVisModel  `json:"models"`
	CitedHosts  []aiVisHost   `json:"cited_hosts"`
	Weeks       []aiVisWeek   `json:"weeks"`
	Facts       aiVisFacts    `json:"facts"`
	OpenFixes   int           `json:"open_fixes"`
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
		SELECT c.id, c.name, c.brand_id::text, COALESCE(b.name, x.company, '')
		FROM ai_cohorts c LEFT JOIN brands b ON b.id = c.brand_id LEFT JOIN workspace_context x ON x.workspace_id = c.workspace_id
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
		c := &aiVisCohort{Competitors: []aiVisEntity{}, Models: []aiVisModel{}, CitedHosts: []aiVisHost{}, Weeks: []aiVisWeek{}}
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
		SELECT m.cohort_id, m.entity, m.name, count(*), avg(m.position)::float8
		FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND m.cohort_id = ANY($2) AND s.status = 'ok' AND m.sampled_at >= $3
		GROUP BY m.cohort_id, m.entity, m.name`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	totals := map[string]int{}
	var entity string
	var pos float64
	if _, err := pgx.ForEachRow(rows, []any{&id, &entity, &name, &n, &pos}, func() error {
		c := byID[id]
		p := math.Round(pos*10) / 10
		e := aiVisEntity{Name: name, Answers: n, MentionRate: rate(n, c.Answers), AvgPosition: &p}
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
		SELECT cohort_id, host, owner, competitor, answers FROM (
			SELECT c.cohort_id, c.host, min(c.owner) AS owner, min(c.competitor) AS competitor, count(DISTINCT c.sample_id) AS answers,
			       row_number() OVER (PARTITION BY c.cohort_id ORDER BY count(DISTINCT c.sample_id) DESC, c.host) AS rank
			FROM ai_citations c JOIN ai_samples s ON s.id = c.sample_id
			WHERE c.workspace_id = $1 AND c.cohort_id = ANY($2) AND s.status = 'ok' AND c.sampled_at >= $3
			GROUP BY c.cohort_id, c.host) t
		WHERE rank <= 15 ORDER BY cohort_id, answers DESC, host`, ws, ids, from)
	if err != nil {
		return nil, err
	}
	var h aiVisHost
	if _, err := pgx.ForEachRow(rows, []any{&id, &h.Host, &h.Owner, &h.Competitor, &h.Answers}, func() error {
		byID[id].CitedHosts = append(byID[id].CitedHosts, h)
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
			c.Brand.MentionRate = rate(0, c.Answers)
		}
		for i := range c.Competitors {
			c.Competitors[i].ShareOfVoice = rate(c.Competitors[i].Answers, totals[c.Cohort.ID])
		}
		sort.SliceStable(c.Competitors, func(i, j int) bool { return c.Competitors[i].Answers > c.Competitors[j].Answers })
		out = append(out, *c)
	}
	return out, nil
}
