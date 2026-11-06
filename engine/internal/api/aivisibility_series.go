package api

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// The brand's AI visibility week by week, split one way: against each
// competitor, by assistant, or by the questions' awareness level, type,
// topic or persona. Each series carries the score with its range per
// week, so the chart can say which lines moved for real.

// seriesBy lists the ways the series can be split.
var seriesBy = map[string]bool{"competitor": true, "assistant": true, "awareness": true, "question_type": true, "topic": true, "persona": true, "market": true}

const maxSeries = 12

type aiVisSeriesWeek struct {
	Week        string   `json:"week"`
	Answers     int      `json:"answers"`
	MentionRate *float64 `json:"mention_rate"`
	aiVisScore
}

type aiVisSeries struct {
	// Key is what the series is: "brand" or a competitor's name, an
	// assistant's "provider/model[/web]", or the attribute's value.
	Key   string `json:"key"`
	Label string `json:"label"`
	// You is the brand itself (only when split by competitor).
	You       bool   `json:"you"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	WebSearch *bool  `json:"web_search,omitempty"`
	Surface   string `json:"surface,omitempty"`
	// Questions is how many questions the series covers.
	Questions   int      `json:"questions"`
	Answers     int      `json:"answers"`
	MentionRate *float64 `json:"mention_rate"`
	aiVisScore
	Weeks []aiVisSeriesWeek `json:"weeks"`
}

func (s *Server) getAIVisibilitySeries(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	by := r.URL.Query().Get("by")
	if by == "" {
		by = "competitor"
	}
	if !seriesBy[by] {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "by is competitor, assistant, awareness, question_type, topic, persona or market")
		return
	}
	days := 84
	if v := r.URL.Query().Get("days"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 365 {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "days is 1 to 365")
			return
		}
		days = n
	}
	cohort := r.URL.Query().Get("cohort")
	if cohort != "" && !validUUID(cohort) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "cohort is a question set's id")
		return
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	out, weeks, err := s.aiVisibilitySeries(r.Context(), a.WorkspaceID, a.Brand, cohort, by, from)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"by": by, "days": days, "from": from.Format(time.RFC3339), "weeks": weeks, "series": out})
}

// weeksFrom lists the Monday-aligned weeks from a day to now, so every
// series has the same points.
func weeksFrom(from time.Time) []string {
	day := from.UTC().Truncate(24 * time.Hour)
	for day.Weekday() != time.Monday {
		day = day.AddDate(0, 0, -1)
	}
	out := []string{}
	for now := time.Now().UTC(); !day.After(now); day = day.AddDate(0, 0, 7) {
		out = append(out, day.Format("2006-01-02"))
	}
	return out
}

func (s *Server) aiVisibilitySeries(ctx context.Context, ws, brand, cohort, by string, from time.Time) ([]*aiVisSeries, []string, error) {
	weeks := weeksFrom(from)
	var cohortID *string
	if cohort != "" {
		cohortID = &cohort
	}
	args := []any{ws, brand, from, cohortID}
	where := " AND ($4::uuid IS NULL OR s.cohort_id = $4::uuid)"
	type prompt struct {
		attrs  map[string]string
		demand *int
	}
	prompts := map[string]prompt{}
	// Answers per question, week and assistant.
	type akey struct{ assistant, prompt, week string }
	answers := map[akey]int{}
	type assistant struct {
		provider, model, surface string
		web                      bool
	}
	assistants := map[string]assistant{}
	rows, err := s.pool.Query(ctx, `
		SELECT s.prompt_id, p.awareness, p.question_type, p.topic, p.persona, c.locale, p.demand,
		       s.provider, s.model, s.web_search, s.surface,
		       to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*)
		FROM ai_samples s JOIN ai_prompts p ON p.id = s.prompt_id JOIN ai_cohorts c ON c.id = s.cohort_id
		WHERE s.workspace_id = $1 AND s.brand_id = $2 AND s.status = 'ok' AND s.sampled_at >= $3`+where+`
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12`, args...)
	if err != nil {
		return nil, nil, err
	}
	var pid, aw, qt, topic, persona, locale, provider, model, surface, week string
	var demand *int
	var web bool
	var n int
	akeyOf := func() string {
		k := provider + "/" + model
		if web {
			k += "/web"
		}
		if surface != "" && surface != "api" {
			k += "/" + surface
		}
		if _, ok := assistants[k]; !ok {
			assistants[k] = assistant{provider, model, surface, web}
		}
		return k
	}
	if _, err := pgx.ForEachRow(rows, []any{&pid, &aw, &qt, &topic, &persona, &locale, &demand, &provider, &model, &web, &surface, &week, &n}, func() error {
		prompts[pid] = prompt{attrs: map[string]string{"awareness": aw, "question_type": qt, "topic": topic, "persona": persona, "market": locale}, demand: demand}
		ak := akeyOf()
		for _, k := range []akey{{"", pid, ""}, {"", pid, week}, {ak, pid, ""}, {ak, pid, week}} {
			answers[k] += n
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if len(prompts) == 0 {
		return []*aiVisSeries{}, weeks, nil
	}
	// Each entity's points per question, week and assistant.
	type pkey struct{ entity, name, assistant, prompt, week string }
	type pts struct {
		points, square float64
		named          int
	}
	points := map[pkey]pts{}
	names := map[string]bool{}
	rows, err = s.pool.Query(ctx, `
		SELECT s.prompt_id, m.entity, m.name, s.provider, s.model, s.web_search, s.surface,
		       to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
		       sum(CASE m.stance WHEN 'recommended' THEN $5::float8 WHEN 'warned' THEN 0 ELSE $6::float8 END),
		       sum(CASE m.stance WHEN 'recommended' THEN $5::float8 * $5 WHEN 'warned' THEN 0 ELSE $6::float8 * $6 END),
		       count(*)
		FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND s.brand_id = $2 AND s.status = 'ok' AND s.sampled_at >= $3`+where+` AND m.entity IN ('brand', 'competitor')
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8`, ws, brand, from, cohortID, pointsRecommended, pointsNamed)
	if err != nil {
		return nil, nil, err
	}
	var entity, name string
	var p, sq float64
	if _, err := pgx.ForEachRow(rows, []any{&pid, &entity, &name, &provider, &model, &web, &surface, &week, &p, &sq, &n}, func() error {
		if _, ok := prompts[pid]; !ok {
			return nil
		}
		if entity == "brand" {
			name = ""
		} else {
			names[name] = true
		}
		ak := akeyOf()
		for _, k := range []pkey{{entity, name, "", pid, ""}, {entity, name, "", pid, week}, {entity, name, ak, pid, ""}, {entity, name, ak, pid, week}} {
			v := points[k]
			v.points, v.square, v.named = v.points+p, v.square+sq, v.named+n
			points[k] = v
		}
		return nil
	}); err != nil {
		return nil, nil, err
	}
	dm := map[string]*int{}
	for id, pr := range prompts {
		dm[id] = pr.demand
	}
	w := weights(dm)
	// one series: the entity's score on the questions kept, from one
	// assistant's answers or all.
	one := func(entity, name, ak string, keep func(prompt) bool) *aiVisSeries {
		out := &aiVisSeries{Weeks: []aiVisSeriesWeek{}}
		questions := func(week string) ([]scoreQuestion, int, int) {
			qs := []scoreQuestion{}
			total, named := 0, 0
			for id, pr := range prompts {
				if keep != nil && !keep(pr) {
					continue
				}
				v := points[pkey{entity, name, ak, id, week}]
				a := answers[akey{ak, id, week}]
				qs = append(qs, scoreQuestion{weight: w[id], answers: a, points: v.points, square: v.square, named: v.named})
				total += a
				named += v.named
			}
			return qs, total, named
		}
		qs, total, named := questions("")
		out.Questions, out.Answers, out.MentionRate, out.aiVisScore = len(qs), total, rate(named, total), demandScore(qs)
		for _, wk := range weeks {
			qs, total, named := questions(wk)
			out.Weeks = append(out.Weeks, aiVisSeriesWeek{Week: wk, Answers: total, MentionRate: rate(named, total), aiVisScore: demandScore(qs)})
		}
		return out
	}
	list := []*aiVisSeries{}
	switch by {
	case "competitor":
		var brandName string
		if err := s.pool.QueryRow(ctx, `SELECT name FROM brands WHERE id = $1 AND workspace_id = $2`, brand, ws).Scan(&brandName); err != nil && err != pgx.ErrNoRows {
			return nil, nil, err
		}
		you := one("brand", "", "", nil)
		you.Key, you.Label, you.You = "brand", brandName, true
		list = append(list, you)
		for name := range names {
			sr := one("competitor", name, "", nil)
			sr.Key, sr.Label = name, name
			list = append(list, sr)
		}
	case "assistant":
		for k, a := range assistants {
			sr := one("brand", "", k, nil)
			web := a.web
			sr.Key, sr.Label, sr.Provider, sr.Model, sr.WebSearch, sr.Surface = k, assistantLabel(a.model, a.web, a.surface), a.provider, a.model, &web, a.surface
			list = append(list, sr)
		}
	default:
		values := map[string]bool{}
		for _, pr := range prompts {
			values[pr.attrs[by]] = true
		}
		for v := range values {
			sr := one("brand", "", "", func(pr prompt) bool { return pr.attrs[by] == v })
			sr.Key, sr.Label = v, v
			if v == "" {
				sr.Label = "Not set"
			}
			list = append(list, sr)
		}
	}
	// The brand first, then the most answered; at most maxSeries lines.
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].You != list[j].You {
			return list[i].You
		}
		if list[i].Answers != list[j].Answers {
			return list[i].Answers > list[j].Answers
		}
		return list[i].Label < list[j].Label
	})
	if by == "competitor" {
		// Competitors by how often they're named, since every one shares
		// the brand's answers.
		sort.SliceStable(list[1:], func(i, j int) bool {
			a, b := list[1+i], list[1+j]
			if (a.Score == nil) != (b.Score == nil) {
				return a.Score != nil
			}
			if a.Score != nil && *a.Score != *b.Score {
				return *a.Score > *b.Score
			}
			return a.Label < b.Label
		})
	}
	if len(list) > maxSeries {
		list = list[:maxSeries]
	}
	return list, weeks, nil
}

// assistantLabel names an assistant as the pages do.
func assistantLabel(model string, web bool, surface string) string {
	label := model
	switch model {
	case "chatgpt":
		label = "ChatGPT"
	case "gemini":
		label = "Gemini"
	}
	if surface == "screen" {
		return label + " (screen)"
	}
	if web {
		return label + " + web"
	}
	return label
}
