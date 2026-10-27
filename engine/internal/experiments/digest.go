package experiments

import (
	"context"
	"fmt"
	"math"

	"github.com/outflareai/outflare/engine/internal/digests"
)

// The digest's Experiments section: what is measuring, and what ended in
// the period with its verdict.
func init() {
	digests.Register(digests.Builder{ID: "experiments", Title: "Experiments", Build: digestSection})
}

func digestSection(ctx context.Context, q digests.Querier, ws string, p digests.Period) (digests.Section, error) {
	var s digests.Section
	var measuring, waiting float64
	if err := q.QueryRow(ctx, `SELECT count(*) FILTER (WHERE status = 'measuring')::float8, count(*) FILTER (WHERE status = 'waiting')::float8
		FROM experiments WHERE workspace_id = $1`, ws).Scan(&measuring, &waiting); err != nil {
		return s, err
	}
	rows, err := q.Query(ctx, `SELECT name, result->>'verdict', (result->>'lift')::float8, metric FROM experiments
		WHERE workspace_id = $1 AND status = 'done' AND concluded_at >= $2 AND concluded_at < $3 ORDER BY concluded_at`, ws, p.Start, p.End)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	ended := 0.0
	for rows.Next() {
		var name, metric string
		var verdict *string
		var lift *float64
		if err := rows.Scan(&name, &verdict, &lift, &metric); err != nil {
			return s, err
		}
		ended++
		text, tone := name+": no clear change", "neutral"
		if verdict != nil && lift != nil && (*verdict == "better" || *verdict == "worse") {
			text = fmt.Sprintf("%s: %s %+.0f%% %s", name, *verdict, math.Round(*lift*100), metric)
			if *verdict == "better" {
				tone = "good"
			} else {
				tone = "bad"
			}
		}
		s.Items = append(s.Items, digests.Item{Text: text, Tab: "experiments", Tone: tone})
	}
	s.Metrics = []digests.Metric{
		{Key: "experiments_ended", Label: "Ended", Value: ended},
		{Key: "experiments_measuring", Label: "Measuring now", Value: measuring},
		{Key: "experiments_waiting", Label: "Waiting for a deploy", Value: waiting},
	}
	return s, rows.Err()
}
