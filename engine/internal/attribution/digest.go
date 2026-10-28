package attribution

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/digests"
	"github.com/outflareai/outflare/engine/internal/utm"
)

// The digest's Channels section: the CMO view in brief. Visits,
// conversions, revenue and cost across every channel under the
// workspace's default model, against the period before, and the channels
// that brought the most conversions.
func init() {
	digests.Register(digests.Builder{ID: "channels", Title: "Channels", Build: digestSection})
}

// channelTotals are one period's numbers. Money is in major units per
// currency.
type channelTotals struct {
	visits      float64
	conversions float64
	revenue     map[string]float64
	cost        map[string]float64
	byChannel   map[string]float64
	revByCh     map[string]map[string]float64
}

func digestSection(ctx context.Context, q digests.Querier, ws string, p digests.Period) (digests.Section, error) {
	var s digests.Section
	model := LastNonDirect
	err := q.QueryRow(ctx, `SELECT model FROM attribution_settings WHERE workspace_id = $1`, ws).Scan(&model)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return s, err
	}
	now, err := periodTotals(ctx, q, ws, model, p.Start, p.End)
	if err != nil {
		return s, err
	}
	length := p.End.Sub(p.Start)
	prev, err := periodTotals(ctx, q, ws, model, p.Start.Add(-length), p.Start)
	if err != nil {
		return s, err
	}
	metric := func(key, label string, v, before float64) digests.Metric {
		return digests.Metric{Key: key, Label: label, Value: round1(v), Previous: ptr(round1(before))}
	}
	s.Metrics = []digests.Metric{
		metric("channel_visits", "Visits", now.visits, prev.visits),
		metric("channel_conversions", "Conversions", now.conversions, prev.conversions),
	}
	for _, cur := range currencies(now.revenue, prev.revenue) {
		s.Metrics = append(s.Metrics, metric("channel_revenue_"+strings.ToLower(cur), "Revenue, "+cur, now.revenue[cur], prev.revenue[cur]))
	}
	for _, cur := range currencies(now.cost, prev.cost) {
		s.Metrics = append(s.Metrics, metric("channel_cost_"+strings.ToLower(cur), "Cost, "+cur, now.cost[cur], prev.cost[cur]))
	}

	// The three channels with the most conversions, with their revenue.
	top := make([]string, 0, len(now.byChannel))
	for ch, n := range now.byChannel {
		if n > 0 {
			top = append(top, ch)
		}
	}
	slices.SortFunc(top, func(a, b string) int {
		if d := now.byChannel[b] - now.byChannel[a]; d != 0 {
			if d > 0 {
				return 1
			}
			return -1
		}
		return strings.Compare(a, b)
	})
	if len(top) > 3 {
		top = top[:3]
	}
	for _, ch := range top {
		text := fmt.Sprintf("%s: %s", channelLabel(ch), plural(now.byChannel[ch], "conversion"))
		for _, cur := range currencies(now.revByCh[ch]) {
			if v := now.revByCh[ch][cur]; v != 0 {
				text += fmt.Sprintf(", %.0f %s", v, cur)
			}
		}
		s.Items = append(s.Items, digests.Item{Text: text, Tab: "channels", Tone: "neutral"})
	}
	return s, nil
}

func periodTotals(ctx context.Context, q digests.Querier, ws, model string, start, end any) (channelTotals, error) {
	t := channelTotals{revenue: map[string]float64{}, cost: map[string]float64{}, byChannel: map[string]float64{}, revByCh: map[string]map[string]float64{}}
	if err := q.QueryRow(ctx, `SELECT count(*)::float8 FROM site_sessions
		WHERE workspace_id = $1 AND channel <> '' AND started_at >= $2 AND started_at < $3`, ws, start, end).Scan(&t.visits); err != nil {
		return t, err
	}
	rows, err := q.Query(ctx, `SELECT cr.channel, c.currency,
			coalesce(sum(cr.weight) FILTER (WHERE c.name <> 'payment.refunded'), 0)::float8,
			coalesce(sum(cr.weight * c.value_minor), 0)::float8
		FROM attribution_credits cr JOIN attribution_conversions c ON c.event_id = cr.event_id
		WHERE cr.workspace_id = $1 AND cr.model = $2 AND c.occurred_at >= $3 AND c.occurred_at < $4
		GROUP BY cr.channel, c.currency`, ws, model, start, end)
	if err != nil {
		return t, err
	}
	for rows.Next() {
		var ch, cur string
		var n, minor float64
		if err := rows.Scan(&ch, &cur, &n, &minor); err != nil {
			rows.Close()
			return t, err
		}
		t.conversions += n
		t.byChannel[ch] += n
		if cur != "" && minor != 0 {
			t.revenue[cur] += minor / 100
			if t.revByCh[ch] == nil {
				t.revByCh[ch] = map[string]float64{}
			}
			t.revByCh[ch][cur] += minor / 100
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return t, err
	}
	rows, err = q.Query(ctx, `SELECT currency, sum(amount_cents)::float8 FROM spend_entries
		WHERE workspace_id = $1 AND amount_cents IS NOT NULL AND occurred_at >= $2 AND occurred_at < $3
		GROUP BY currency`, ws, start, end)
	if err != nil {
		return t, err
	}
	defer rows.Close()
	for rows.Next() {
		var cur string
		var cents float64
		if err := rows.Scan(&cur, &cents); err != nil {
			return t, err
		}
		if cur != "" {
			t.cost[cur] += cents / 100
		}
	}
	return t, rows.Err()
}

// currencies are the currencies in any of the maps, sorted.
func currencies(maps ...map[string]float64) []string {
	out := []string{}
	for _, m := range maps {
		for c := range m {
			if !slices.Contains(out, c) {
				out = append(out, c)
			}
		}
	}
	slices.Sort(out)
	return out
}

func channelLabel(ch string) string {
	if c, ok := utm.ChannelByID(ch); ok {
		return c.Label
	}
	if ch == Direct {
		return "Direct"
	}
	return ch
}

func plural(n float64, one string) string {
	n = round1(n)
	if n == 1 {
		return "1 " + one
	}
	if n == math.Trunc(n) {
		return fmt.Sprintf("%.0f %ss", n, one)
	}
	return fmt.Sprintf("%.1f %ss", n, one)
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func ptr(v float64) *float64 { return &v }
