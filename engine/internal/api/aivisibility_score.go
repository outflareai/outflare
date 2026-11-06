package api

import (
	"context"
	"math"
	"sort"

	"github.com/jackc/pgx/v5"
)

// The AI visibility score: 0 to 100, how well AI answers treat a brand
// across a cohort's questions, each question weighted by how many people
// ask it. An answer that recommends the brand scores 1, one that names or
// lists it 0.6, one that warns against it or leaves it out 0. A question's
// score is the mean of its answers; the cohort's is the questions' mean
// weighted by demand (questions without a demand count as the cohort's
// median, or all alike when none has one). The range is where the score
// would land nine times in ten if the same questions were asked again, so
// a week whose range doesn't overlap the week before is a real change, not
// the answers' usual wobble.

const (
	pointsRecommended = 1.0
	pointsNamed       = 0.6
	// scoreZ is the normal quantile for a 90% range.
	scoreZ = 1.645
)

// aiVisScore is a score with its range, 0 to 100.
type aiVisScore struct {
	Score *float64 `json:"score"`
	Low   *float64 `json:"score_low"`
	High  *float64 `json:"score_high"`
}

// aiVisCell is the brand in one awareness level and question type.
type aiVisCell struct {
	Awareness    string   `json:"awareness"`
	QuestionType string   `json:"question_type"`
	Questions    int      `json:"questions"`
	Answers      int      `json:"answers"`
	MentionRate  *float64 `json:"mention_rate"`
	aiVisScore
}

// scoreQuestion is one question's answers and one entity's points on it.
type scoreQuestion struct {
	weight         float64
	answers        int
	points, square float64
	named          int
}

// demandScore is the demand-weighted score of the questions, with its
// range.
func demandScore(qs []scoreQuestion) aiVisScore {
	var w, mean, variance float64
	for _, q := range qs {
		if q.answers > 0 {
			w += q.weight
		}
	}
	if w == 0 {
		return aiVisScore{}
	}
	for _, q := range qs {
		if q.answers == 0 {
			continue
		}
		n := float64(q.answers)
		m := q.points / n
		mean += q.weight / w * m
		// A smoothed mean keeps one answer from claiming certainty.
		sm := (q.points + 0.5) / (n + 1)
		v := math.Max(q.square/n-m*m, sm*(1-sm)) / n
		variance += (q.weight / w) * (q.weight / w) * v
	}
	r := func(x float64) *float64 {
		x = math.Round(math.Max(0, math.Min(100, x*100))*10) / 10
		return &x
	}
	sd := math.Sqrt(variance)
	return aiVisScore{Score: r(mean), Low: r(mean - scoreZ*sd), High: r(mean + scoreZ*sd)}
}

// weights fills in each question's weight from its demand.
func weights(demand map[string]*int) map[string]float64 {
	known := []float64{}
	for _, d := range demand {
		if d != nil && *d > 0 {
			known = append(known, float64(*d))
		}
	}
	median := 1.0
	if len(known) > 0 {
		sort.Float64s(known)
		median = known[len(known)/2]
	}
	out := map[string]float64{}
	for p, d := range demand {
		if len(known) == 0 {
			out[p] = 1
		} else if d != nil && *d > 0 {
			out[p] = float64(*d)
		} else {
			out[p] = median
		}
	}
	return out
}

// aiVisibilityScores adds the score to each cohort's brand, competitors
// and weeks, and the brand's awareness by question type grid.
func (s *Server) aiVisibilityScores(ctx context.Context, ws string, ids []string, byID map[string]*aiVisCohort, from any) error {
	type prompt struct {
		cohort, awareness, qtype string
		demand                   *int
	}
	prompts := map[string]prompt{}
	demand := map[string]map[string]*int{}
	// Answers per question, overall and week by week.
	type key struct{ prompt, week string }
	answers := map[key]int{}
	rows, err := s.pool.Query(ctx, `
		SELECT s.cohort_id, s.prompt_id, p.awareness, p.question_type, p.demand,
		       to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'), count(*)
		FROM ai_samples s JOIN ai_prompts p ON p.id = s.prompt_id
		WHERE s.workspace_id = $1 AND s.cohort_id = ANY($2) AND s.status = 'ok' AND s.sampled_at >= $3
		GROUP BY 1, 2, 3, 4, 5, 6`, ws, ids, from)
	if err != nil {
		return err
	}
	var c, pid, aw, qt, week string
	var d *int
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&c, &pid, &aw, &qt, &d, &week, &n}, func() error {
		prompts[pid] = prompt{cohort: c, awareness: aw, qtype: qt, demand: d}
		if demand[c] == nil {
			demand[c] = map[string]*int{}
		}
		demand[c][pid] = d
		answers[key{pid, ""}] += n
		answers[key{pid, week}] += n
		return nil
	}); err != nil {
		return err
	}
	// Each entity's points per question, overall and week by week.
	type ekey struct{ entity, name, prompt, week string }
	type pts struct {
		points, square float64
		named          int
	}
	points := map[ekey]pts{}
	rows, err = s.pool.Query(ctx, `
		SELECT s.prompt_id, m.entity, m.name, to_char(date_trunc('week', s.sampled_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD'),
		       sum(CASE m.stance WHEN 'recommended' THEN $4::float8 WHEN 'warned' THEN 0 ELSE $5::float8 END),
		       sum(CASE m.stance WHEN 'recommended' THEN $4::float8 * $4 WHEN 'warned' THEN 0 ELSE $5::float8 * $5 END),
		       count(*)
		FROM ai_mentions m JOIN ai_samples s ON s.id = m.sample_id
		WHERE m.workspace_id = $1 AND m.cohort_id = ANY($2) AND s.status = 'ok' AND m.sampled_at >= $3
		GROUP BY 1, 2, 3, 4`, ws, ids, from, pointsRecommended, pointsNamed)
	if err != nil {
		return err
	}
	var entity, name string
	var p, sq float64
	if _, err := pgx.ForEachRow(rows, []any{&pid, &entity, &name, &week, &p, &sq, &n}, func() error {
		if entity == "brand" {
			name = "" // the brand is one, whatever its name was then
		}
		for _, k := range []ekey{{entity, name, pid, ""}, {entity, name, pid, week}} {
			v := points[k]
			v.points, v.square, v.named = v.points+p, v.square+sq, v.named+n
			points[k] = v
		}
		return nil
	}); err != nil {
		return err
	}
	w := map[string]map[string]float64{}
	for c, dm := range demand {
		w[c] = weights(dm)
	}
	questions := func(cohort, entity, name, week string, keep func(prompt) bool) []scoreQuestion {
		out := []scoreQuestion{}
		for pid, pr := range prompts {
			if pr.cohort != cohort || (keep != nil && !keep(pr)) {
				continue
			}
			v := points[ekey{entity, name, pid, week}]
			out = append(out, scoreQuestion{weight: w[cohort][pid], answers: answers[key{pid, week}], points: v.points, square: v.square, named: v.named})
		}
		return out
	}
	for _, id := range ids {
		co := byID[id]
		co.Brand.aiVisScore = demandScore(questions(id, "brand", "", "", nil))
		for i := range co.Competitors {
			co.Competitors[i].aiVisScore = demandScore(questions(id, "competitor", co.Competitors[i].Name, "", nil))
		}
		for i := range co.Weeks {
			co.Weeks[i].aiVisScore = demandScore(questions(id, "brand", "", co.Weeks[i].Week, nil))
			if i > 0 {
				a, b := co.Weeks[i-1].aiVisScore, co.Weeks[i].aiVisScore
				co.Weeks[i].RealChange = a.Low != nil && b.Low != nil && (*b.Low > *a.High || *b.High < *a.Low)
			}
		}
		// The grid: every awareness level and question type the cohort's
		// answered questions have.
		cells := map[[2]string]*aiVisCell{}
		for pid, pr := range prompts {
			if pr.cohort != id {
				continue
			}
			ck := [2]string{pr.awareness, pr.qtype}
			cell := cells[ck]
			if cell == nil {
				cell = &aiVisCell{Awareness: pr.awareness, QuestionType: pr.qtype}
				cells[ck] = cell
			}
			cell.Questions++
			cell.Answers += answers[key{pid, ""}]
		}
		co.Grid = []aiVisCell{}
		for ck, cell := range cells {
			qs := questions(id, "brand", "", "", func(pr prompt) bool { return pr.awareness == ck[0] && pr.qtype == ck[1] })
			named := 0
			for _, q := range qs {
				named += q.named
			}
			cell.MentionRate = rate(named, cell.Answers)
			cell.aiVisScore = demandScore(qs)
			co.Grid = append(co.Grid, *cell)
		}
		sort.Slice(co.Grid, func(i, j int) bool {
			if co.Grid[i].Awareness != co.Grid[j].Awareness {
				return co.Grid[i].Awareness < co.Grid[j].Awareness
			}
			return co.Grid[i].QuestionType < co.Grid[j].QuestionType
		})
	}
	return nil
}
