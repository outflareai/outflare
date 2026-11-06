package api

import (
	"context"
	"encoding/csv"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Per-question AI visibility: for each of a brand's tracked questions,
// over the last days, how its answers treated the brand (named,
// recommended, listed, warned, where it came), by assistant, which
// competitors they recommended and which other brands they named, and the
// same numbers for the period before, so a question that was gained or
// lost stands out. Scored like the overview: recommended 1, named or
// listed 0.6, warned or absent 0, as a mean over the answers (no demand
// weighting within one question).

// aiVisQuestionPeriod is how one question's answers treated the brand in
// a period.
type aiVisQuestionPeriod struct {
	Answers       int      `json:"answers"`
	Named         int      `json:"named"`
	Recommended   int      `json:"recommended"`
	Listed        int      `json:"listed"`
	Warned        int      `json:"warned"`
	MentionRate   *float64 `json:"mention_rate"`
	RecommendRate *float64 `json:"recommend_rate"`
	AvgPosition   *float64 `json:"avg_position"`
	Score         *float64 `json:"score"`

	positions int
}

type aiVisQuestionModel struct {
	Provider  string `json:"provider"`
	Model     string `json:"model"`
	WebSearch bool   `json:"web_search"`
	Surface   string `json:"surface"`
	aiVisQuestionPeriod
}

// aiVisQuestionBrand is a competitor the question's answers named.
type aiVisQuestionBrand struct {
	Name        string `json:"name"`
	Answers     int    `json:"answers"`
	Recommended int    `json:"recommended"`
}

// aiVisQuestionWeek is one week of a question's answers, to see whether
// the brand is shown over time.
type aiVisQuestionWeek struct {
	Week string `json:"week"`
	aiVisQuestionPeriod
}

type aiVisQuestion struct {
	ID           string `json:"id"`
	CohortID     string `json:"cohort_id"`
	Text         string `json:"text"`
	Awareness    string `json:"awareness"`
	QuestionType string `json:"question_type"`
	Topic        string `json:"topic"`
	Persona      string `json:"persona"`
	Competitor   string `json:"competitor"`
	Demand       *int   `json:"demand"`
	Status       string `json:"status"`
	aiVisQuestionPeriod
	// Previous is the period before, or null when it had no answers.
	Previous *aiVisQuestionPeriod `json:"previous"`
	// Change is the score now minus the score before, or null.
	Change *float64 `json:"change"`
	// Competitors the answers named, most recommended first, and brands
	// the answers recommend or list that the question set doesn't track.
	Competitors []aiVisQuestionBrand `json:"competitors"`
	OtherBrands []aiVisCount         `json:"other_brands"`
	ByAssistant []aiVisQuestionModel `json:"by_assistant"`
	// Weeks covers this period and the one before, oldest first; a week
	// without answers is left out.
	Weeks []aiVisQuestionWeek `json:"weeks"`
}

func (p *aiVisQuestionPeriod) finish() {
	if p.Answers == 0 {
		return
	}
	p.MentionRate = rate(p.Named, p.Answers)
	p.RecommendRate = rate(p.Recommended, p.Answers)
	if p.Named > 0 {
		v := float64(p.positions) / float64(p.Named)
		p.AvgPosition = &v
	}
	sc := math.Round((float64(p.Recommended)*pointsRecommended+float64(p.Listed)*pointsNamed)/float64(p.Answers)*1000) / 10
	p.Score = &sc
}

func (s *Server) getAIVisibilityQuestions(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	days := 28
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
	if f := r.URL.Query().Get("format"); f != "" && f != "json" && f != "csv" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "format is json or csv")
		return
	}
	from := time.Now().UTC().AddDate(0, 0, -days)
	before := from.AddDate(0, 0, -days)
	out, err := s.aiVisibilityQuestions(r.Context(), a.WorkspaceID, a.Brand, cohort, from, before)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	if r.URL.Query().Get("format") == "csv" {
		header := []string{"question", "awareness", "question_type", "topic", "persona", "demand", "answers", "named", "recommended", "listed", "warned", "mention_rate", "recommend_rate", "avg_position", "score", "previous_score", "change", "competitors"}
		rows := make([][]string, 0, len(out))
		for _, q := range out {
			comps := make([]string, 0, len(q.Competitors))
			for _, c := range q.Competitors {
				comps = append(comps, c.Name)
			}
			prev := ""
			if q.Previous != nil {
				prev = csvFloat(q.Previous.Score)
			}
			rows = append(rows, []string{q.Text, q.Awareness, q.QuestionType, q.Topic, q.Persona, csvInt(q.Demand), strconv.Itoa(q.Answers), strconv.Itoa(q.Named), strconv.Itoa(q.Recommended),
				strconv.Itoa(q.Listed), strconv.Itoa(q.Warned), csvFloat(q.MentionRate), csvFloat(q.RecommendRate), csvFloat(q.AvgPosition), csvFloat(q.Score), prev, csvFloat(q.Change), strings.Join(comps, "; ")})
		}
		writeCSV(w, "ai-visibility-questions", header, rows)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out, "days": days, "from": from.Format(time.RFC3339), "previous_from": before.Format(time.RFC3339)})
}

// writeCSV sends rows as a CSV download, cells made safe for spreadsheets.
func writeCSV(w http.ResponseWriter, name string, header []string, rows [][]string) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.csv"`)
	cw := csv.NewWriter(w)
	cw.Write(header) //nolint:errcheck
	for _, row := range rows {
		for i, v := range row {
			row[i] = csvSafe(v)
		}
		cw.Write(row) //nolint:errcheck
	}
	cw.Flush()
}

func csvFloat(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func csvInt(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func (s *Server) aiVisibilityQuestions(ctx context.Context, ws, brand, cohort string, from, before time.Time) ([]*aiVisQuestion, error) {
	args := []any{ws, brand}
	where := ""
	if cohort != "" {
		args = append(args, cohort)
		where = " AND p.cohort_id = $3"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.cohort_id, p.text, p.awareness, p.question_type, p.topic, p.persona, p.competitor, p.demand, p.status
		FROM ai_prompts p JOIN ai_cohorts c ON c.id = p.cohort_id
		WHERE p.workspace_id = $1 AND c.workspace_id = $1 AND c.brand_id = $2 AND p.status <> 'suggested'`+where, args...)
	if err != nil {
		return nil, err
	}
	list := []*aiVisQuestion{}
	byID := map[string]*aiVisQuestion{}
	q := &aiVisQuestion{}
	if _, err := pgx.ForEachRow(rows, []any{&q.ID, &q.CohortID, &q.Text, &q.Awareness, &q.QuestionType, &q.Topic, &q.Persona, &q.Competitor, &q.Demand, &q.Status}, func() error {
		c := *q
		c.Competitors, c.OtherBrands, c.ByAssistant, c.Weeks = []aiVisQuestionBrand{}, []aiVisCount{}, []aiVisQuestionModel{}, []aiVisQuestionWeek{}
		list = append(list, &c)
		byID[c.ID] = &c
		return nil
	}); err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return list, nil
	}
	ids := make([]string, 0, len(list))
	for _, q := range list {
		ids = append(ids, q.ID)
	}
	type mkey struct {
		prompt, provider, model, surface string
		web                              bool
	}
	models := map[mkey]*aiVisQuestionModel{}
	previous := map[string]*aiVisQuestionPeriod{}
	type wkey struct{ prompt, week string }
	weeks := map[wkey]*aiVisQuestionWeek{}
	// period is the question's, or the assistant's, numbers for the
	// period an answer falls in, and its week's.
	period := func(pid string, now bool, k mkey, week string) []*aiVisQuestionPeriod {
		q := byID[pid]
		wk := weeks[wkey{pid, week}]
		if wk == nil {
			wk = &aiVisQuestionWeek{Week: week}
			weeks[wkey{pid, week}] = wk
		}
		if !now {
			p := previous[pid]
			if p == nil {
				p = &aiVisQuestionPeriod{}
				previous[pid] = p
			}
			return []*aiVisQuestionPeriod{p, &wk.aiVisQuestionPeriod}
		}
		m := models[k]
		if m == nil {
			m = &aiVisQuestionModel{Provider: k.provider, Model: k.model, WebSearch: k.web, Surface: k.surface}
			models[k] = m
			q.ByAssistant = append(q.ByAssistant, *m)
		}
		return []*aiVisQuestionPeriod{&q.aiVisQuestionPeriod, &m.aiVisQuestionPeriod, &wk.aiVisQuestionPeriod}
	}
	// Answers per question, period and assistant.
	rows, err = s.pool.Query(ctx, `
		SELECT s.prompt_id, s.sampled_at >= $3, s.provider, s.model, s.web_search, s.surface,
		       to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*)
		FROM ai_samples s
		WHERE s.workspace_id = $1 AND s.brand_id = $2 AND s.prompt_id = ANY($5) AND s.status = 'ok' AND s.sampled_at >= $4
		GROUP BY 1, 2, 3, 4, 5, 6, 7`, ws, brand, from, before, ids)
	if err != nil {
		return nil, err
	}
	var pid, provider, model, surface, week string
	var now, web bool
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&pid, &now, &provider, &model, &web, &surface, &week, &n}, func() error {
		for _, p := range period(pid, now, mkey{pid, provider, model, surface, web}, week) {
			p.Answers += n
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// How those answers treated the brand.
	rows, err = s.pool.Query(ctx, `
		SELECT s.prompt_id, s.sampled_at >= $3, s.provider, s.model, s.web_search, s.surface,
		       to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), m.stance, count(*), sum(m.position)
		FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND s.brand_id = $2 AND s.prompt_id = ANY($5) AND m.entity = 'brand' AND s.status = 'ok' AND s.sampled_at >= $4
		GROUP BY 1, 2, 3, 4, 5, 6, 7, 8`, ws, brand, from, before, ids)
	if err != nil {
		return nil, err
	}
	var stance string
	var positions int
	if _, err := pgx.ForEachRow(rows, []any{&pid, &now, &provider, &model, &web, &surface, &week, &stance, &n, &positions}, func() error {
		for _, p := range period(pid, now, mkey{pid, provider, model, surface, web}, week) {
			p.Named += n
			p.positions += positions
			switch stance {
			case "recommended":
				p.Recommended += n
			case "warned":
				p.Warned += n
			default:
				p.Listed += n
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	// Competitors the answers named this period.
	rows, err = s.pool.Query(ctx, `
		SELECT s.prompt_id, m.name, count(*), count(*) FILTER (WHERE m.stance = 'recommended')
		FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND s.brand_id = $2 AND s.prompt_id = ANY($4) AND m.entity = 'competitor' AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY 1, 2`, ws, brand, from, ids)
	if err != nil {
		return nil, err
	}
	var name string
	var rec int
	if _, err := pgx.ForEachRow(rows, []any{&pid, &name, &n, &rec}, func() error {
		q := byID[pid]
		q.Competitors = append(q.Competitors, aiVisQuestionBrand{Name: name, Answers: n, Recommended: rec})
		return nil
	}); err != nil {
		return nil, err
	}
	// Brands the answers recommend or list that aren't tracked.
	rows, err = s.pool.Query(ctx, `
		SELECT s.prompt_id, b, count(*)
		FROM ai_samples s, unnest(s.other_brands) AS b
		WHERE s.workspace_id = $1 AND s.brand_id = $2 AND s.prompt_id = ANY($4) AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY 1, 2`, ws, brand, from, ids)
	if err != nil {
		return nil, err
	}
	if _, err := pgx.ForEachRow(rows, []any{&pid, &name, &n}, func() error {
		q := byID[pid]
		q.OtherBrands = append(q.OtherBrands, aiVisCount{Name: name, Answers: n})
		return nil
	}); err != nil {
		return nil, err
	}
	for k, wk := range weeks {
		wk.finish()
		byID[k.prompt].Weeks = append(byID[k.prompt].Weeks, *wk)
	}
	for _, q := range list {
		q.finish()
		sort.Slice(q.Weeks, func(i, j int) bool { return q.Weeks[i].Week < q.Weeks[j].Week })
		if p := previous[q.ID]; p != nil {
			p.finish()
			q.Previous = p
			if q.Score != nil && p.Score != nil {
				d := math.Round((*q.Score-*p.Score)*10) / 10
				q.Change = &d
			}
		}
		for i := range q.ByAssistant {
			m := &q.ByAssistant[i]
			*m = *models[mkey{q.ID, m.Provider, m.Model, m.Surface, m.WebSearch}]
			m.finish()
		}
		sort.SliceStable(q.ByAssistant, func(i, j int) bool {
			a, b := q.ByAssistant[i], q.ByAssistant[j]
			return a.Surface+a.Provider+a.Model < b.Surface+b.Provider+b.Model
		})
		sort.SliceStable(q.Competitors, func(i, j int) bool {
			a, b := q.Competitors[i], q.Competitors[j]
			return a.Recommended > b.Recommended || a.Recommended == b.Recommended && (a.Answers > b.Answers || a.Answers == b.Answers && a.Name < b.Name)
		})
		sort.SliceStable(q.OtherBrands, func(i, j int) bool {
			a, b := q.OtherBrands[i], q.OtherBrands[j]
			return a.Answers > b.Answers || a.Answers == b.Answers && a.Name < b.Name
		})
	}
	// Most asked first, then alphabetical; questions without a demand go
	// after those with one.
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch {
		case (a.Demand != nil) != (b.Demand != nil):
			return a.Demand != nil
		case a.Demand != nil && *a.Demand != *b.Demand:
			return *a.Demand > *b.Demand
		}
		return a.Text < b.Text
	})
	return list, nil
}
