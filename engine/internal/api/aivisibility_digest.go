package api

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/digests"
)

// AI visibility in the brand's digest: the score, how often answers named
// and recommended the brand over the period against the one before, the
// questions it stopped or started being named for, and new objections the
// answers raised.

func init() {
	digests.Register(digests.Builder{ID: "ai_visibility", Title: "AI visibility", Build: aiVisibilityDigest})
}

const digestQuestions = 5

func aiVisibilityDigest(ctx context.Context, q digests.Querier, ws, brand string, p digests.Period) (digests.Section, error) {
	var out digests.Section
	// Per period: answers, the brand's mention and recommend rates, and the
	// demand-weighted score (each question the mean of its answers' points;
	// a question without a demand counts as the median).
	const stats = `
		WITH a AS (
			SELECT s.id, s.prompt_id, p.demand,
			       (SELECT max(CASE m.stance WHEN 'recommended' THEN $4::float8 WHEN 'warned' THEN 0 ELSE $5::float8 END) FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand') AS pts,
			       EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand') AS named,
			       EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand' AND m.stance = 'recommended') AS rec
			FROM ai_samples s JOIN ai_prompts p ON p.id = s.prompt_id
			WHERE s.workspace_id = $1 AND s.brand_id = $2 AND s.status = 'ok' AND s.sampled_at >= $3 AND s.sampled_at < $6
		),
		q AS (SELECT prompt_id, demand, avg(COALESCE(pts, 0)) AS score FROM a GROUP BY 1, 2),
		w AS (SELECT prompt_id, score, COALESCE(NULLIF(demand, 0)::float8, (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY demand) FROM q WHERE demand > 0), 1) AS weight FROM q)
		SELECT (SELECT count(*) FROM a)::float8, (SELECT count(*) FILTER (WHERE named) FROM a)::float8, (SELECT count(*) FILTER (WHERE rec) FROM a)::float8,
		       COALESCE((SELECT sum(weight * score) / NULLIF(sum(weight), 0) * 100 FROM w), 0)`
	var now, before [4]float64
	for i, period := range []digests.Period{p, {Start: p.Start.Add(-p.End.Sub(p.Start)), End: p.Start}} {
		dst := &now
		if i == 1 {
			dst = &before
		}
		if err := q.QueryRow(ctx, stats, ws, brand, period.Start, pointsRecommended, pointsNamed, period.End).Scan(&dst[0], &dst[1], &dst[2], &dst[3]); err != nil {
			return out, err
		}
	}
	if now[0] == 0 && before[0] == 0 {
		return out, nil
	}
	pct := func(n, of float64) float64 {
		if of == 0 {
			return 0
		}
		return float64(int(n/of*1000+0.5)) / 10
	}
	prev := func(v float64, had bool) *float64 {
		if !had {
			return nil
		}
		return &v
	}
	had := before[0] > 0
	out.Metrics = []digests.Metric{
		{Key: "ai_score", Label: "AI visibility score", Value: float64(int(now[3]*10+0.5)) / 10, Previous: prev(float64(int(before[3]*10+0.5))/10, had)},
		{Key: "ai_named_pct", Label: "Answers naming you (%)", Value: pct(now[1], now[0]), Previous: prev(pct(before[1], before[0]), had)},
		{Key: "ai_recommended_pct", Label: "Answers recommending you (%)", Value: pct(now[2], now[0]), Previous: prev(pct(before[2], before[0]), had)},
		{Key: "ai_answers", Label: "AI answers read", Value: now[0], Previous: prev(before[0], had)},
	}
	// Questions the answers stopped naming the brand for, and started.
	rows, err := q.Query(ctx, `
		WITH per AS (
			SELECT s.prompt_id, s.sampled_at >= $3 AND s.sampled_at < $4 AS now,
			       bool_or(EXISTS (SELECT 1 FROM ai_mentions m WHERE m.sample_id = s.id AND m.entity = 'brand')) AS named
			FROM ai_samples s WHERE s.workspace_id = $1 AND s.brand_id = $2 AND s.status = 'ok' AND s.sampled_at >= $5 AND s.sampled_at < $4
			GROUP BY 1, 2
		)
		SELECT p.text, n.named FROM per n JOIN per b ON b.prompt_id = n.prompt_id AND NOT b.now JOIN ai_prompts p ON p.id = n.prompt_id
		WHERE n.now AND n.named <> b.named ORDER BY n.named, p.text LIMIT $6`, ws, brand, p.Start, p.End, p.Start.Add(-p.End.Sub(p.Start)), 2*digestQuestions)
	if err != nil {
		return out, err
	}
	var text string
	var named bool
	lost, gained := 0, 0
	if _, err := pgx.ForEachRow(rows, []any{&text, &named}, func() error {
		if named {
			if gained++; gained > digestQuestions {
				return nil
			}
			out.Items = append(out.Items, digests.Item{Text: "Now named for: " + text, Tab: "ai-questions", Tone: "good"})
		} else {
			if lost++; lost > digestQuestions {
				return nil
			}
			out.Items = append(out.Items, digests.Item{Text: "No longer named for: " + text, Tab: "ai-questions", Tone: "bad"})
		}
		return nil
	}); err != nil {
		return out, err
	}
	// Objections the answers raised this period that they hadn't before.
	rows, err = q.Query(ctx, `
		SELECT c.text, count(*) FROM ai_claims c
		WHERE c.workspace_id = $1 AND c.brand_id = $2 AND c.entity = 'brand' AND c.kind = 'objection' AND c.sampled_at >= $3 AND c.sampled_at < $4
		  AND NOT EXISTS (SELECT 1 FROM ai_claims x WHERE x.workspace_id = $1 AND x.brand_id = $2 AND x.entity = 'brand' AND x.kind = 'objection' AND x.key = c.key AND x.sampled_at < $3)
		GROUP BY c.key, c.text ORDER BY 2 DESC, 1 LIMIT 3`, ws, brand, p.Start, p.End)
	if err != nil {
		return out, err
	}
	var n int
	if _, err := pgx.ForEachRow(rows, []any{&text, &n}, func() error {
		out.Items = append(out.Items, digests.Item{Text: fmt.Sprintf("New objection in %d answer%s: %s", n, map[bool]string{true: "", false: "s"}[n == 1], text), Tab: "ai-perception", Tone: "bad"})
		return nil
	}); err != nil {
		return out, err
	}
	return out, nil
}
