package visitors

import (
	"context"
	"fmt"

	"github.com/outflareai/outflare/engine/internal/digests"
)

// The digest's Website visitors section: how many known people came to the
// site, and who became hot in the period.
func init() {
	digests.Register(digests.Builder{ID: "visitors", Title: "Website visitors", Build: digestSection})
}

func digestSection(ctx context.Context, q digests.Querier, ws string, p digests.Period) (digests.Section, error) {
	var s digests.Section
	var people, browsers, hot float64
	if err := q.QueryRow(ctx, `SELECT count(DISTINCT record_id)::float8, count(*)::float8,
			(SELECT count(*) FROM visitor_alerts WHERE workspace_id = $1 AND alerted_at >= $2 AND alerted_at < $3)::float8
		FROM site_visitors WHERE workspace_id = $1 AND last_seen_at >= $2 AND last_seen_at < $3`, ws, p.Start, p.End).Scan(&people, &browsers, &hot); err != nil {
		return s, err
	}
	s.Metrics = []digests.Metric{
		{Key: "visitors", Label: "Visitors", Value: browsers},
		{Key: "known_people", Label: "Known people who visited", Value: people},
		{Key: "hot", Label: "Became hot", Value: hot},
	}
	rows, err := q.Query(ctx, `SELECT r.record_type, r.name, a.score FROM visitor_alerts a JOIN records r ON r.id = a.record_id
		WHERE a.workspace_id = $1 AND a.alerted_at >= $2 AND a.alerted_at < $3 AND r.merged_into IS NULL
		ORDER BY a.score DESC, a.alerted_at DESC LIMIT 5`, ws, p.Start, p.End)
	if err != nil {
		return s, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		var score int
		if err := rows.Scan(&typ, &name, &score); err != nil {
			return s, err
		}
		if name == "" {
			name = "Someone without a name yet"
			if typ == "account" {
				name = "A company without a name yet"
			}
		}
		s.Items = append(s.Items, digests.Item{Text: fmt.Sprintf("%s is hot on your website (score %d).", name, score), Tab: "visitors", Tone: "good"})
	}
	return s, rows.Err()
}
