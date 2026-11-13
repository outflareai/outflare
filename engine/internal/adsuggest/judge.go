package adsuggest

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

// Verdicts. A change is measured from the end of its settling time over
// the brand's judgement window and compared with as many days just before
// it, in the finding's unit: new paying customers from the ledger, or the
// network's conversions where the brand has no target. The trailing lag
// days (the brand's median click-to-payment lag) are left out of the
// compared range so late payments don't read as a drop. A campaign whose
// cost per payer runs over twice the target three days running is judged
// early. Each verdict writes a brand finding, so the agent remembers what
// the lever did on this brand and network.

// measuring is one finding whose change is being measured.
type measuring struct {
	id, kind, entity, entityName, account, connector, currency, unit, rule string
	brand                                                                  *string
	from, until                                                            time.Time
	target                                                                 *int64
	pmax, troas                                                            bool
}

// floor is the data floor each side: 30 payers under a tROAS, 5 otherwise.
func (m measuring) floor() float64 {
	if m.troas {
		return minPayersROAS
	}
	return minPayers
}

func (r *Runner) judge(ctx context.Context, ws string) (int, error) {
	last := r.lastDay()
	// A change the network refused was never made: nothing to measure.
	if _, err := r.Pool.Exec(ctx, `UPDATE ad_suggestions s SET status = 'judged', verdict = 'not_sure', judged_at = now(), updated_at = now(),
			verdict_note = 'The platform refused the change, so it was never made: ' || c.error
		FROM ad_changes c WHERE c.id = s.change_id AND s.workspace_id = $1 AND s.status = 'measuring' AND c.status = 'failed'`, ws); err != nil {
		return 0, err
	}
	rows, err := r.Pool.Query(ctx, `
		SELECT s.id, s.kind, s.ad_entity_id, e.name, a.id, a.connector, a.currency, s.unit, s.rule, s.brand_id::text, s.measure_from, s.measure_until,
			CASE WHEN s.unit = 'payers' THEN x.target_cost_per_payer_micros ELSE COALESCE(e.target_cost_per_conversion_micros, a.target_cost_per_conversion_micros, e.platform_target_cpa_micros) END,
			e.objective = 'performance_max', e.platform_target_roas IS NOT NULL
		FROM ad_suggestions s JOIN ad_entities e ON e.id = s.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		LEFT JOIN ad_brand_economics x ON x.brand_id = s.brand_id
		WHERE s.workspace_id = $1 AND s.status = 'measuring' AND s.measure_from IS NOT NULL AND s.measure_from <= $2`, ws, last)
	if err != nil {
		return 0, err
	}
	list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (measuring, error) {
		var m measuring
		err := row.Scan(&m.id, &m.kind, &m.entity, &m.entityName, &m.account, &m.connector, &m.currency, &m.unit, &m.rule, &m.brand, &m.from, &m.until, &m.target, &m.pmax, &m.troas)
		return m, err
	})
	if err != nil {
		return 0, err
	}
	lags := map[string]int{}
	n := 0
	for _, m := range list {
		lagDays := 0
		if m.brand != nil && m.unit == UnitPayers {
			if v, ok := lags[*m.brand]; ok {
				lagDays = v
			} else {
				lag, err := paidads.ClickToPaymentLag(ctx, r.Pool, *m.brand)
				if err != nil {
					return n, err
				}
				_, lagDays = windowOf(lag)
				lags[*m.brand] = lagDays
			}
		}
		done, err := r.judgeOne(ctx, ws, m, last, lagDays)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
}

// sum reads a campaign's (or, with whole, its account's) numbers between
// two days, in the finding's unit.
func (r *Runner) sum(ctx context.Context, ws string, m measuring, whole bool, from, to time.Time) (numbers, error) {
	t := numbers{Days: int(to.Sub(from).Hours()/24) + 1}
	ids := []string{m.entity}
	if whole {
		rows, err := r.Pool.Query(ctx, `SELECT id::text FROM ad_entities WHERE ad_account_id = $1 AND level = 'campaign'`, m.account)
		if err != nil {
			return t, err
		}
		if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return t, err
		}
	}
	var conv float64
	if err := r.Pool.QueryRow(ctx, `SELECT COALESCE(sum(spend_micros), 0)::bigint, COALESCE(sum(clicks), 0)::bigint, COALESCE(sum(impressions), 0)::bigint, COALESCE(sum(conversions), 0)::float8
		FROM ad_metrics_daily WHERE workspace_id = $1 AND ad_entity_id = ANY($2::uuid[]) AND day BETWEEN $3 AND $4`, ws, ids, from, to).Scan(&t.Spend, &t.Clicks, &t.Impressions, &conv); err != nil {
		return t, err
	}
	if m.unit != UnitPayers || m.brand == nil {
		t.Conv = conv
		return t, nil
	}
	daily, err := paidads.Daily(ctx, r.Pool, *m.brand, ids, from, to)
	if err != nil {
		return t, err
	}
	for _, days := range daily {
		for _, s := range days {
			t.Conv += float64(s.NewPayers)
			t.Signups += s.Signups
			t.Revenue += s.NewRevenue
		}
	}
	return t, nil
}

// judgeOne gives a verdict once the window has run (its trailing lag days
// left out), or early when the changed campaign costs over twice the
// target three days running.
func (r *Runner) judgeOne(ctx context.Context, ws string, m measuring, last time.Time, lagDays int) (bool, error) {
	end := m.until
	settled := last.AddDate(0, 0, -lagDays)
	early := false
	if settled.Before(m.until) {
		if m.target == nil || m.kind == Pause || last.Sub(m.from) < (stopRun-1)*24*time.Hour {
			return false, nil
		}
		over := 0
		for d := 0; d < stopRun; d++ {
			t, err := r.sum(ctx, ws, m, false, m.from, last.AddDate(0, 0, -d))
			if err != nil {
				return false, err
			}
			if t.Spend > 0 && t.cost() > stopFactor*float64(*m.target) {
				over++
			}
		}
		if over < stopRun {
			return false, nil
		}
		end, early = last, true
	}
	days := int(end.Sub(m.from).Hours()/24) + 1
	var changed time.Time
	if err := r.Pool.QueryRow(ctx, `SELECT COALESCE(c.applied_at, c.created_at) FROM ad_suggestions s JOIN ad_changes c ON c.id = s.change_id WHERE s.id = $1`, m.id).Scan(&changed); err != nil {
		return false, err
	}
	bEnd := time.Date(changed.Year(), changed.Month(), changed.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, -1)
	bFrom := bEnd.AddDate(0, 0, -(days - 1))
	whole := m.kind == Pause
	before, err := r.sum(ctx, ws, m, whole, bFrom, bEnd)
	if err != nil {
		return false, err
	}
	after, err := r.sum(ctx, ws, m, whole, m.from, end)
	if err != nil {
		return false, err
	}
	verdict, note := Verdict(m.kind, m.unit, before, after, m.target, m.floor())
	if early {
		verdict, note = DidNotWork, fmt.Sprintf("Stopped early: since the change, a %s has cost more than twice the target three days running. ", unitWord(m.unit, 1))+note
	}
	res, _ := json.Marshal(map[string]any{"before": before, "after": after, "early": early, "unit": m.unit, "lag_days": lagDays})
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ad_suggestions SET status = 'judged', verdict = $2, verdict_note = $3, result = $4, judged_at = now(), updated_at = now()
			WHERE id = $1 AND status = 'measuring'`, m.id, verdict, note, res)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		data := map[string]any{"suggestion_id": m.id, "campaign_id": m.entity, "connector": m.connector, "kind": m.kind, "verdict": verdict, "early": early, "unit": m.unit, "rule": m.rule}
		if m.brand != nil {
			data["brand_id"] = *m.brand
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_suggestion.judged",
			TargetType: "ad_suggestion", TargetID: m.id, Data: data, Props: map[string]any{"connector": m.connector, "kind": m.kind, "verdict": verdict, "early": early, "unit": m.unit}}); err != nil {
			return err
		}
		return r.brandFinding(ctx, tx, ws, m, verdict, note, before, after)
	})
	if err != nil {
		return false, err
	}
	metrics.AdSuggestionJudged(m.connector, m.kind, verdict)
	metrics.AdVerdict(m.kind, verdict)
	return true, nil
}

// maxBrandFindings is the most active findings a brand keeps (the
// findings API's limit); at it, a verdict is still recorded on its row
// but no finding is written.
const maxBrandFindings = 500

// brandFinding remembers the verdict on the brand: what the lever did on
// this campaign and network, with the numbers, reviewed in 90 days.
func (r *Runner) brandFinding(ctx context.Context, tx pgx.Tx, ws string, m measuring, verdict, note string, before, after numbers) error {
	if m.brand == nil {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM brand_findings WHERE brand_id = $1 AND status = 'active'`, *m.brand).Scan(&n); err != nil {
		return err
	}
	if n >= maxBrandFindings {
		return nil
	}
	word := map[string]string{Worked: "worked", DidNotWork: "did not work", NotSure: "was not clear"}[verdict]
	text := fmt.Sprintf("On %s, %s on %q %s: %s", networkName(m.connector), leverWord(m.kind), m.entityName, word, note)
	if len(text) > 1000 {
		text = text[:997] + "..."
	}
	confidence := "low"
	switch {
	case verdict == NotSure:
	case before.Conv >= minPayersROAS && after.Conv >= minPayersROAS:
		confidence = "high"
	case before.Conv >= m.floor() && after.Conv >= m.floor():
		confidence = "medium"
	}
	evidence, _ := json.Marshal([]map[string]any{
		{"kind": "action", "ref": m.id, "note": fmt.Sprintf("the %s finding on campaign %s (%s)", m.kind, m.entity, m.rule)},
		{"kind": "metric", "ref": "cost_per_" + unitRef(m.unit), "note": fmt.Sprintf("before: %s over %d days (%.0f); after: %s over %d days (%.0f)",
			costWord(before, m.currency), before.Days, before.Conv, costWord(after, m.currency), after.Days, after.Conv)},
	})
	scope, _ := json.Marshal(map[string]any{"channel": "paid_ads"})
	var id string
	if err := tx.QueryRow(ctx, `INSERT INTO brand_findings (workspace_id, brand_id, text, evidence, confidence, scope, review_at)
		VALUES ($1, $2, $3, $4, $5, $6, now() + interval '90 days') RETURNING id`, ws, *m.brand, text, evidence, confidence, scope).Scan(&id); err != nil {
		return err
	}
	return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "finding.recorded", TargetType: "finding", TargetID: id,
		Data:  map[string]any{"brand_id": *m.brand, "text": text, "confidence": confidence, "scope": map[string]any{"channel": "paid_ads"}, "evidence": 2, "suggestion_id": m.id, "verdict": verdict},
		Props: map[string]any{"brand_id": *m.brand, "confidence": confidence, "evidence": 2, "source": "ad_verdict", "kind": m.kind, "verdict": verdict}})
}

func unitRef(unit string) string {
	if unit == UnitPayers {
		return "new_payer"
	}
	return "conversion"
}

func costWord(n numbers, cur string) string {
	if n.Conv <= 0 {
		return "no " + "conversions for " + money(float64(n.Spend), cur)
	}
	return money(n.cost(), cur)
}

// leverWord says what a kind does, for a finding's text.
func leverWord(kind string) string {
	switch kind {
	case BudgetUp:
		return "raising the daily budget by a fifth"
	case BudgetDown:
		return "cutting the daily budget by a fifth"
	case Pause:
		return "pausing the campaign"
	case Resume:
		return "resuming the campaign"
	case NegativeAdd:
		return "adding a negative keyword"
	case KeywordAdd:
		return "promoting a search to a keyword"
	case KeywordSplit:
		return "giving a keyword its own ad group"
	case BiddingTarget:
		return "bidding to a target cost per conversion"
	case TargetTighten:
		return "tightening the bidding target"
	case TargetLoosen:
		return "loosening the bidding target"
	case KeywordPause:
		return "pausing a keyword"
	case AdPause:
		return "pausing an ad"
	case Consolidate:
		return "merging ad groups"
	case ExclusionAdd:
		return "adding an exclusion"
	}
	return strings.ReplaceAll(kind, "_", " ")
}

// networkName is a connector's name as a sentence says it.
func networkName(connector string) string {
	return map[string]string{"google_ads": "Google Ads", "meta_ads": "Meta", "microsoft_ads": "Microsoft Advertising", "linkedin_ads": "LinkedIn", "tiktok_ads": "TikTok", "reddit_ads": "Reddit", "x_ads": "X"}[connector] + ""
}

// Verdict compares the days after a change with as many days before it,
// in unit: worked at or under 1.1 times the target (a raise's marginal
// cost at or under 1.2 times; a saving when cheaper), did not work past
// 1.5 times (or when a saving lost volume without getting cheaper), not
// sure below the floor each side.
func Verdict(kind, unit string, before, after numbers, target *int64, floor float64) (string, string) {
	cost := func(v float64) string { return fmt.Sprintf("%.2f", v/1e6) }
	one, many := unitWord(unit, 1), unitWord(unit, 2)
	t := math.Inf(1)
	if target != nil {
		t = float64(*target)
	}
	switch kind {
	case Resume:
		// Nothing ran before: judged on its own days against the target.
		if after.Conv < floor {
			return NotSure, fmt.Sprintf("Too few %s to tell: %.0f since it restarted.", many, after.Conv)
		}
		switch {
		case target == nil:
			return NotSure, fmt.Sprintf("%.0f %s at %s each, with no target to judge by.", after.Conv, many, cost(after.cost()))
		case after.cost() <= workedWithin*t:
			return Worked, fmt.Sprintf("%.0f %s at %s each, within the target of %s.", after.Conv, many, cost(after.cost()), cost(t))
		case after.cost() > failedPast*t:
			return DidNotWork, fmt.Sprintf("A %s costs %s, well over the target of %s.", one, cost(after.cost()), cost(t))
		}
		return NotSure, fmt.Sprintf("%.0f %s at %s each against a target of %s.", after.Conv, many, cost(after.cost()), cost(t))
	case Pause:
		// The account without the campaign.
		if before.Conv == 0 && after.Conv == 0 {
			return NotSure, fmt.Sprintf("The account recorded no %s before or after.", many)
		}
		if after.Conv >= 0.9*before.Conv {
			return Worked, fmt.Sprintf("The account brought as many %s without it (%.0f before, %.0f after) and spent %s less.", many, before.Conv, after.Conv, cost(float64(before.Spend-after.Spend)))
		}
		return DidNotWork, fmt.Sprintf("The account's %s fell from %.0f to %.0f without it.", many, before.Conv, after.Conv)
	}
	if before.Conv < floor || after.Conv < floor {
		return NotSure, fmt.Sprintf("Too few %s to tell: %.0f before and %.0f after (at least %.0f each side).", many, before.Conv, after.Conv, floor)
	}
	switch kind {
	case BudgetUp:
		extraConv := after.Conv - before.Conv
		extraSpend := after.Spend - before.Spend
		if extraConv <= 0 {
			return DidNotWork, fmt.Sprintf("More was spent (%+.2f) but %s didn't rise (%.0f before, %.0f after).", float64(extraSpend)/1e6, many, before.Conv, after.Conv)
		}
		marginal := float64(extraSpend) / extraConv
		switch {
		case target != nil && marginal <= raiseMarginal*t:
			return Worked, fmt.Sprintf("%.0f more %s at about %s each, within the target of %s.", extraConv, many, cost(marginal), cost(t))
		case target != nil && marginal > failedPast*t:
			return DidNotWork, fmt.Sprintf("The extra %s cost about %s each, well over the target of %s.", many, cost(marginal), cost(t))
		case target == nil && marginal <= before.cost():
			return Worked, fmt.Sprintf("%.0f more %s at about %s each, no more than before (%s).", extraConv, many, cost(marginal), cost(before.cost()))
		}
		return NotSure, fmt.Sprintf("%.0f more %s at about %s each.", extraConv, many, cost(marginal))
	case BudgetDown, NegativeAdd, KeywordPause, AdPause, ExclusionAdd, TargetTighten:
		// A saving: cheaper per payer, or on target with the volume kept.
		switch {
		case after.cost() <= cheaperBy*before.cost():
			return Worked, fmt.Sprintf("A %s costs %s now, down from %s, with %.0f %s (%.0f before).", one, cost(after.cost()), cost(before.cost()), after.Conv, many, before.Conv)
		case target != nil && after.cost() <= workedWithin*t && after.Conv >= keptVolume*before.Conv:
			return Worked, fmt.Sprintf("A %s costs %s, within the target of %s, with %.0f %s (%.0f before).", one, cost(after.cost()), cost(t), after.Conv, many, before.Conv)
		case target != nil && after.cost() > failedPast*t:
			return DidNotWork, fmt.Sprintf("A %s costs %s now, well over the target of %s.", one, cost(after.cost()), cost(t))
		case after.Conv < keptVolume*before.Conv && after.cost() >= before.cost():
			return DidNotWork, fmt.Sprintf("%s fell from %.0f to %.0f and a %s costs %s (%s before): spending less didn't make them cheaper.", strings.ToUpper(many[:1])+many[1:], before.Conv, after.Conv, one, cost(after.cost()), cost(before.cost()))
		}
		return NotSure, fmt.Sprintf("A %s costs about the same: %s before, %s after, with %.0f %s (%.0f before).", one, cost(before.cost()), cost(after.cost()), after.Conv, many, before.Conv)
	case BiddingTarget:
		// A target should hold the cost without losing many payers.
		switch {
		case (after.cost() <= 1.05*before.cost() || (target != nil && after.cost() <= workedWithin*t)) && after.Conv >= 0.85*before.Conv:
			return Worked, fmt.Sprintf("A %s costs %s (%s before), with %.0f %s (%.0f before).", one, cost(after.cost()), cost(before.cost()), after.Conv, many, before.Conv)
		case after.Conv < 0.7*before.Conv || after.cost() > 1.2*before.cost() || (target != nil && after.cost() > failedPast*t):
			return DidNotWork, fmt.Sprintf("With the target the campaign got %.0f %s (%.0f before) at %s each (%s before).", after.Conv, many, before.Conv, cost(after.cost()), cost(before.cost()))
		}
		return NotSure, fmt.Sprintf("A %s costs %s (%s before), with %.0f %s (%.0f before).", one, cost(after.cost()), cost(before.cost()), after.Conv, many, before.Conv)
	default:
		// Spending more or restructuring (a keyword added, a target
		// loosened, ad groups merged): more payers at no worse a cost.
		limit := 1.05 * before.cost()
		if target != nil {
			limit = math.Max(limit, workedWithin*t)
		}
		switch {
		case after.Conv >= before.Conv && after.cost() <= limit:
			return Worked, fmt.Sprintf("The campaign brought %.0f %s (%.0f before) at %s each (%s before).", after.Conv, many, before.Conv, cost(after.cost()), cost(before.cost()))
		case after.cost() > 1.2*before.cost() || (target != nil && after.cost() > failedPast*t):
			return DidNotWork, fmt.Sprintf("A %s in the campaign costs %s now, up from %s.", one, cost(after.cost()), cost(before.cost()))
		}
		return NotSure, fmt.Sprintf("The campaign brought %.0f %s (%.0f before) at %s each (%s before).", after.Conv, many, before.Conv, cost(after.cost()), cost(before.cost()))
	}
}
