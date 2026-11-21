package paidads

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/digests"
)

// The digest's Paid ads section: the week's spend, clicks, the sign-ups
// and new paying customers the ads brought, and what a new paying
// customer cost, each against the period before, in the brand's currency.
// Under them: the campaigns that spent the most with what they brought,
// what waits for a decision, and how the changes made earlier turned out.
func init() {
	digests.Register(digests.Builder{ID: "paid_ads", Title: "Paid ads", Build: digestSection})
}

// adTotals are one period's numbers, money in micros of the brand's
// currency.
type adTotals struct {
	spend           int64
	clicks          float64
	signups, payers float64
}

func (t adTotals) costPerPayer() float64 {
	if t.payers == 0 {
		return 0
	}
	return float64(t.spend) / t.payers / 1e6
}

func digestSection(ctx context.Context, q digests.Querier, ws, brand string, p digests.Period) (digests.Section, error) {
	var s digests.Section
	econ, err := Load(ctx, q, ws, brand)
	if errors.Is(err, ErrNoBrand) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	rates, err := LoadRates(ctx, q)
	if err != nil {
		return s, err
	}
	now, campaigns, err := adPeriod(ctx, q, ws, brand, econ.Currency, rates, p.Start, p.End)
	if err != nil {
		return s, err
	}
	length := p.End.Sub(p.Start)
	prev, _, err := adPeriod(ctx, q, ws, brand, econ.Currency, rates, p.Start.Add(-length), p.Start)
	if err != nil {
		return s, err
	}
	if now.spend == 0 && prev.spend == 0 && now.signups == 0 && now.payers == 0 {
		return s, nil
	}
	cur := econ.Currency
	metric := func(key, label string, v, before float64) digests.Metric {
		before = math.Round(before*100) / 100
		return digests.Metric{Key: key, Label: label, Value: math.Round(v*100) / 100, Previous: &before}
	}
	s.Metrics = []digests.Metric{
		metric("ad_spend_"+strings.ToLower(cur), "Ad spend, "+cur, float64(now.spend)/1e6, float64(prev.spend)/1e6),
		metric("ad_clicks", "Clicks", now.clicks, prev.clicks),
		metric("ad_signups", "Sign-ups from ads", now.signups, prev.signups),
		metric("ad_new_payers", "New paying customers from ads", now.payers, prev.payers),
	}
	if now.payers > 0 || prev.payers > 0 {
		s.Metrics = append(s.Metrics, metric("ad_cost_per_new_payer_"+strings.ToLower(cur), "Cost per new paying customer, "+cur, now.costPerPayer(), prev.costPerPayer()))
	}

	// The three campaigns that spent the most, with what they brought. One
	// that spent more than the brand's target without a single new paying
	// customer is the line to read first.
	for i, c := range campaigns {
		if i == 3 || c.spend == 0 {
			break
		}
		text := fmt.Sprintf("%s spent %s", c.name, money(c.spend, cur))
		tone := "neutral"
		switch {
		case c.payers > 0:
			text += fmt.Sprintf(" and brought %s, %s each.", countOf(c.payers, "new paying customer"), money(int64(float64(c.spend)/c.payers), cur))
			if econ.HasTarget() {
				if float64(c.spend)/c.payers <= float64(*econ.TargetCostPerPayerMicros) {
					tone = "good"
				} else {
					tone = "bad"
				}
			}
		case c.signups > 0:
			text += fmt.Sprintf(" and brought %s, none paying yet.", countOf(c.signups, "sign-up"))
		default:
			text += " and brought no sign-up."
			if econ.HasTarget() && c.spend > *econ.TargetCostPerPayerMicros {
				tone = "bad"
			}
		}
		s.Items = append(s.Items, digests.Item{Text: text, Tab: "ads", Tone: tone})
	}

	var waiting, alerts, worked, failed int
	if err := q.QueryRow(ctx, `SELECT
			count(*) FILTER (WHERE status = 'open' AND autonomy <> 'alert'),
			count(*) FILTER (WHERE status = 'open' AND autonomy = 'alert'),
			count(*) FILTER (WHERE status = 'judged' AND verdict = 'worked' AND judged_at >= $3 AND judged_at < $4),
			count(*) FILTER (WHERE status = 'judged' AND verdict = 'did_not_work' AND judged_at >= $3 AND judged_at < $4)
		FROM ad_suggestions WHERE workspace_id = $1 AND brand_id = $2 AND NOT shadow`, ws, brand, p.Start, p.End).Scan(&waiting, &alerts, &worked, &failed); err != nil {
		return s, err
	}
	if alerts > 0 {
		s.Items = append(s.Items, digests.Item{Text: countOf(float64(alerts), "alert") + " on your ads " + isAre(alerts) + " open.", Tab: "ads", Tone: "bad"})
	}
	if waiting > 0 {
		s.Items = append(s.Items, digests.Item{Text: countOf(float64(waiting), "recommendation") + " " + isAre(waiting) + " waiting for your decision.", Tab: "ads", Tone: "neutral"})
	}
	if worked+failed > 0 {
		tone := "good"
		if failed > worked {
			tone = "bad"
		}
		s.Items = append(s.Items, digests.Item{Text: fmt.Sprintf("Of the changes measured this period, %d worked and %d did not.", worked, failed), Tab: "ads", Tone: tone})
	}
	return s, nil
}

type adCampaign struct {
	name            string
	spend           int64
	signups, payers float64
}

// adPeriod adds up one period for the brand, and its campaigns by spend.
// Spend in a currency with no rate is left out rather than added as if it
// were the brand's.
func adPeriod(ctx context.Context, q digests.Querier, ws, brand, cur string, rates Rates, start, end any) (adTotals, []adCampaign, error) {
	var t adTotals
	rows, err := q.Query(ctx, `
		WITH spend AS (
			SELECT m.ad_entity_id AS id, m.currency, sum(m.spend_micros)::bigint AS spend, sum(m.clicks)::float8 AS clicks
			FROM ad_metrics_daily m JOIN ad_entities e ON e.id = m.ad_entity_id
			WHERE m.workspace_id = $1 AND m.brand_id = $2 AND e.level = 'campaign' AND m.day >= ($3::timestamptz)::date AND m.day < ($4::timestamptz)::date
			GROUP BY 1, 2
		), got AS (
			SELECT campaign_entity_id AS id,
				count(*) FILTER (WHERE signed_up_at >= $3 AND signed_up_at < $4)::float8 AS signups,
				count(*) FILTER (WHERE first_paid_at >= $3 AND first_paid_at < $4)::float8 AS payers
			FROM ad_acquisitions WHERE workspace_id = $1 AND brand_id = $2
			  AND ((signed_up_at >= $3 AND signed_up_at < $4) OR (first_paid_at >= $3 AND first_paid_at < $4))
			GROUP BY 1
		)
		SELECT COALESCE(e.name, ''), COALESCE(s.currency, ''), COALESCE(s.spend, 0), COALESCE(s.clicks, 0), COALESCE(g.signups, 0), COALESCE(g.payers, 0)
		FROM spend s FULL JOIN got g ON g.id = s.id LEFT JOIN ad_entities e ON e.id = COALESCE(s.id, g.id)
		ORDER BY COALESCE(s.spend, 0) DESC, 1`, ws, brand, start, end)
	if err != nil {
		return t, nil, err
	}
	var list []adCampaign
	var name, currency string
	var spend int64
	var clicks, signups, payers float64
	_, err = pgx.ForEachRow(rows, []any{&name, &currency, &spend, &clicks, &signups, &payers}, func() error {
		if spend > 0 {
			v, ok := rates.Micros(spend, currency, cur)
			if !ok {
				spend = 0
			} else {
				spend = v
			}
		}
		t.spend += spend
		t.clicks += clicks
		t.signups += signups
		t.payers += payers
		if name == "" {
			name = "A campaign"
		}
		list = append(list, adCampaign{name: name, spend: spend, signups: signups, payers: payers})
		return nil
	})
	if err != nil {
		return t, nil, err
	}
	// Converted spend can reorder campaigns billed in different currencies.
	for i := 1; i < len(list); i++ {
		for j := i; j > 0 && list[j].spend > list[j-1].spend; j-- {
			list[j], list[j-1] = list[j-1], list[j]
		}
	}
	return t, list, nil
}

func money(micros int64, cur string) string {
	return fmt.Sprintf("%.2f %s", float64(micros)/1e6, cur)
}

func countOf(n float64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%.0f %ss", n, noun)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}
