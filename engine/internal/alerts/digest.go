package alerts

import (
	"context"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/digests"
)

// The digest's Alerts section: how many of each kind came in, and how many
// waited in the app because someone reached the day's email limit.
func init() {
	digests.Register(digests.Builder{ID: "alerts", Title: "Alerts", Build: digestSection})
}

func digestSection(ctx context.Context, q digests.Querier, ws string, p digests.Period) (digests.Section, error) {
	var s digests.Section
	rows, err := q.Query(ctx, `SELECT kind, count(*)::float8 FROM alerts WHERE workspace_id = $1 AND created_at >= $2 AND created_at < $3 GROUP BY kind`,
		ws, p.Start, p.End)
	if err != nil {
		return s, err
	}
	counts := map[string]float64{}
	for rows.Next() {
		var kind string
		var n float64
		if err := rows.Scan(&kind, &n); err != nil {
			rows.Close()
			return s, err
		}
		counts[kind] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return s, err
	}
	for _, k := range kinds {
		s.Metrics = append(s.Metrics, digests.Metric{Key: k.ID, Label: k.Title, Value: counts[k.ID]})
	}
	var capped int
	if err := q.QueryRow(ctx, `SELECT count(*) FROM alert_deliveries WHERE workspace_id = $1 AND reason = 'daily_cap' AND created_at >= $2 AND created_at < $3`,
		ws, p.Start, p.End).Scan(&capped); err != nil {
		return s, err
	}
	if capped > 0 {
		s.Items = append(s.Items, digests.Item{Tab: "alerts", Tone: "neutral",
			Text: fmt.Sprintf("%d alert emails waited in the app because someone already had %d that day.", capped, DailyCap)})
	}
	return s, nil
}
