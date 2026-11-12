package adsuggest

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

// The rules (plans/paid-ads/plan.md, "Rules (adsuggest, rebuilt)"), each a
// small function over one campaign's numbers in the brand's unit: cost
// per new paying customer where the brand has set a target, the network's
// own conversions where it hasn't. Every number a rule fires on is a
// named constant in the block below, so a brand's own thresholds can
// replace them later in one place. The research behind each is in
// plans/paid-ads/research/ (practice-bidding-budgets-scaling.md,
// practice-search-structure-keywords.md, practice-pmax-shopping-demandgen.md,
// practice-creative-landing-targeting.md).

// RuleVersion is written to every finding's rule_version; a re-fit of a
// rule's maths bumps it, so old rows keep saying which version made them.
const RuleVersion = 2

// Rule names, as written to ad_suggestions.rule.
const (
	RuleBudgetRaise   = "budget_raise"
	RuleBudgetCut     = "budget_cut"
	RulePause         = "pause"
	RuleResume        = "resume"
	RuleTargetTighten = "target_tighten"
	RuleTargetLoosen  = "target_loosen"
	RuleBiddingLadder = "bidding_ladder"
	RuleNegative      = "negative_keyword"
	RuleNegativeNgram = "negative_ngram"
	RuleNegativePayer = "negative_payment_quality"
	RulePromote       = "keyword_promote"
	RuleKeywordPause  = "keyword_pause"
	RuleAdPause       = "ad_pause"
	RuleConsolidate   = "consolidate"
	RuleExclusion     = "exclusion"
	RuleBrandLeak     = "brand_leak"
	RuleStaleTarget   = "stale_target"
)

// Rules lists every rule name this package writes, for the run's expiry
// of findings it no longer finds true (the alert job expires its own).
var Rules = []string{RuleBudgetRaise, RuleBudgetCut, RulePause, RuleResume, RuleTargetTighten, RuleTargetLoosen, RuleBiddingLadder,
	RuleNegative, RuleNegativeNgram, RuleNegativePayer, RulePromote, RuleKeywordPause, RuleAdPause, RuleConsolidate, RuleExclusion, RuleBrandLeak, RuleStaleTarget}

// Kinds this package writes beyond the lever catalogue's names.
const (
	// TargetTighten moves a tCPA down or a tROAS up toward what the
	// campaign actually does; TargetLoosen the other way.
	TargetTighten = "target_tighten"
	TargetLoosen  = "target_loosen"
	KeywordPause  = "keyword_pause"
	AdPause       = "ad_pause"
	Consolidate   = "consolidate"
	ExclusionAdd  = "exclusion_add"
	// AlertTargetStale: a target untouched for 90 days that the campaign
	// no longer runs near.
	AlertTargetStale = "alert.target_stale"
)

// Units a finding's evidence is in (ad_suggestions.unit).
const (
	UnitPayers   = "payers"
	UnitPlatform = "platform_conversions"
)

// The rules' numbers. Money thresholds are in micros of the account's
// currency unless they say otherwise; shares are fractions.
const (
	// Judgement discipline (all kinds).
	minPayers     = 5    // payers each side before a cost per payer means anything
	minPayersROAS = 30   // the same for a tROAS verdict
	lockDays      = 7    // no change-class finding within a week of any change to the campaign
	targetLock    = 14   // nor a target move within two weeks of one
	maxLagDays    = 14   // the most trailing days left out for late payments
	winnerShare   = 0.30 // the brand's top share of campaigns by new payers, never paused or cut
	pmaxFromDay   = 15   // Performance Max is judged on days 15 to 42 after a change ...
	pmaxToDay     = 42   // ... (Google reallocates for two weeks first)
	pmaxLearnDays = pmaxFromDay - 1

	// Budgets.
	budgetStep       = 0.20 // a fifth, up or down: within what the networks learn from fast
	raiseLostIS      = 0.10 // raise when at least this share of impressions is lost to the budget ...
	raiseCappedDays  = 4    // ... or, where the network doesn't say, the budget ran out on 4 of 7 days
	raiseMarginal    = 1.2  // a raise's extra payers may cost this times the target
	cutOverTarget    = 1.3  // cut when a payer costs more than this times the target ...
	cutZeroTargets   = 3.0  // ... or this many targets were spent with no payer ...
	cutZeroDays      = 14   // ... over these days
	cutMarginalCost  = 1.5  // the last fifth of a budget buys payers at about this times the average
	pauseSpendTarget = 3.0  // pause after this many targets spent with no payer ...
	pauseMinDays     = 14   // ... over at least two weeks ...
	pauseCycles      = 3    // ... and three conversion cycles
	resumePausedDays = 7    // a campaign paused at least a week ...
	resumeUnderShare = 0.8  // ... that ran at or under this share of the target ...
	resumeDays       = 90   // ... over these days ...
	resumeMinPayers  = 10   // ... with at least these payers is worth resuming

	// Targets.
	tightenDays      = 30   // tighten on the last 30 days ...
	tightenBeatBy    = 0.15 // ... when actual beats the target by this share ...
	tightenMinPayers = 30   // ... with these payers
	targetStepMin    = 0.10 // a target moves 10 to 20% toward actual ...
	targetStepMax    = 0.20 // ... never past it
	loosenLostISMax  = 0.10 // loosen only when the budget isn't the limit: under this lost to budget
	loosenPMaxMin    = 0.20 // Performance Max under-spending loosens 20 to 30%
	loosenPMaxMax    = 0.30
	pmaxUnderspend   = 0.70 // under-spending: a week's spend under this share of a week of budget
	ladderDays       = 30   // bid to a target on the last 30 days ...
	ladderMinPayers  = 30   // ... once they have this many payers ...
	ladderWithin     = 0.15 // ... at a cost within this share of the target
	staleDays        = 90   // a target untouched this long ...
	staleGap         = 0.25 // ... that actual misses by this share is stale

	// Keywords (Google search campaigns).
	termDays           = 30 // search terms are read over 30 days
	negTermClicks      = 10 // a negative: 10 clicks, no payer, and cost at least ...
	negTermMinCurrency = 50 // ... 50 of the currency, ...
	negTermSpendShare  = 0.02
	negTermTargets     = 2.0 // ... 2% of the campaign's spend or two targets, whichever is most
	negNgramClicks     = 150 // a word or phrase across the campaign's searches: 150 clicks, no payer
	negQualityDays     = 60  // payment quality: a search whose sign-ups never pay ...
	negQualitySignups  = 5   // ... over 60 days, at least 5 of them
	promoteClicks      = 50  // promote a search with 50 clicks (20 with payer evidence) ...
	promoteClicksPayer = 20
	promoteMinPayers   = 2   // ... and 2 payers at or under the target
	kwPauseTargets     = 3.0 // pause a keyword that spent 3 targets with no payer ...
	kwPauseExpected    = 3   // ... and clicked 3 payers' worth of clicks ...
	kwLookback1        = 30  // ... over 30, then 60, then 90 days
	kwLookback2        = 60
	kwLookback3        = 90
	adPauseMinAds      = 2   // two enabled ads in a group ...
	adPauseRatio       = 0.5 // ... one with under half the other's payers per thousand impressions ...
	adPauseMinPayers   = 100 // ... over 100 payers between them
	consolidateDays    = 30  // ad groups each under 30 payers in 30 days with the same intent merge
	consolidateMax     = 30

	// Exclusions.
	exclDays        = 60  // a segment with no payer over 60 days ...
	exclClicks      = 300 // ... and 300 clicks (100 on a brand campaign)
	exclBrandClicks = 100
	exclNetworkCost = 1.5  // search partners or Display expansion cost this times the campaign's per payer
	brandLeakDays   = 30   // brand leak: over 30 days ...
	brandLeakShare  = 0.20 // ... this share of a non-brand campaign's search clicks name the brand

	// Verdicts.
	workedWithin = 1.1 // worked at or under this times the target
	failedPast   = 1.5 // did not work past this
	cheaperBy    = 0.97
	keptVolume   = 0.8
	stopFactor   = 2.0 // a changed campaign over twice the target ...
	stopRun      = 3   // ... three days running is judged early
	maxWindow    = 42  // six weeks: not sure after that
)

// numbers are a campaign's (or a slice of it) over some days, in the
// finding's unit: Conv is new payers, or the network's conversions.
type numbers struct {
	Spend       int64   `json:"spend_micros"`
	Clicks      int64   `json:"clicks"`
	Impressions int64   `json:"impressions"`
	Conv        float64 `json:"conversions"`
	Signups     int     `json:"signups"`
	Revenue     int64   `json:"new_revenue_minor"`
	Days        int     `json:"days"`
}

// cost is what one conversion cost, +Inf with none.
func (n numbers) cost() float64 {
	if n.Conv <= 0 {
		return math.Inf(1)
	}
	return float64(n.Spend) / n.Conv
}

func (n numbers) add(o numbers) numbers {
	return numbers{Spend: n.Spend + o.Spend, Clicks: n.Clicks + o.Clicks, Impressions: n.Impressions + o.Impressions, Conv: n.Conv + o.Conv,
		Signups: n.Signups + o.Signups, Revenue: n.Revenue + o.Revenue, Days: n.Days + o.Days}
}

// dayNum is one day's numbers.
type dayNum struct {
	spend, clicks, impressions int64
	conv                       float64 // the network's
	payers, signups            int
	revenue                    int64
}

// campaign is one campaign as the rules read it.
type campaign struct {
	id, externalID, name, accountID, acctExternalID, connector, currency, status, objective string
	workspaceID, integrationID, acctName, acctTZ                                            string
	acctMeta                                                                                map[string]string
	brand                                                                                   *string
	brandName                                                                               string
	budget                                                                                  *int64
	// busy: a change on the campaign is still being measured or tested,
	// so no other lever is pulled this cycle.
	busy bool
	// How the network bids and its own target.
	bidding        string
	platformTCPA   *int64
	platformTROAS  *float64
	platformTarget *int64 // the campaign's, account's or network's cost per conversion
	// The unit the campaign is judged in, and the target in it.
	unit   string
	target *int64
	// days ending on last (the last whole day minus the lag), by date.
	days map[string]dayNum
	last time.Time
	// The brand's judgement window and lag, and whether a cycle is known.
	windowDays, lagDays, cycleDays int
	// top: among the brand's winners (never paused or cut).
	top bool
	// Changes on the campaign: the last of any kind, and the last target move.
	lastChange, lastTargetChange *time.Time
	// pausedSince: the last pause, for a paused campaign (nil: unknown).
	pausedSince *time.Time
	// The network's own report, when read (nil: not available).
	signals *connectors.AdCampaignSignals
	caps    paidads.Capabilities
	// brandPayers is the brand's payers over the window, for spreading a
	// saving over them; brandCost the brand's cost per payer then.
	brandPayers int
	brandCost   float64
	// capped: days of the last 7 that spent nine tenths of the budget.
	capped int
}

func (c campaign) pmax() bool { return c.objective == "performance_max" }

func (c campaign) ws() string { return c.workspaceID }

// segConv is a segment's conversions in the unit; a segment whose payers
// the ledger can't tell apart (a placement, a network) keeps the
// network's count.
func (c campaign) segConv(s segment) float64 {
	if s.payers < 0 {
		return s.Conversions
	}
	return c.convOf(s.Conversions, s.payers)
}

// brandCampaign says whether a campaign is the brand's own (named after it).
func (c campaign) brandCampaign() bool {
	return c.brandName != "" && strings.Contains(strings.ToLower(c.name), strings.ToLower(c.brandName))
}

// over sums the last n days ending on last.
func (c campaign) over(n int) numbers {
	var out numbers
	out.Days = n
	for i := 0; i < n; i++ {
		d := c.days[c.last.AddDate(0, 0, -i).Format(time.DateOnly)]
		out.Spend += d.spend
		out.Clicks += d.clicks
		out.Impressions += d.impressions
		out.Signups += d.signups
		out.Revenue += d.revenue
		if c.unit == UnitPayers {
			out.Conv += float64(d.payers)
		} else {
			out.Conv += d.conv
		}
	}
	return out
}

// window is the judgement window's numbers.
func (c campaign) window() numbers { return c.over(c.windowDays) }

// floor is the data floor for this campaign: 30 payers under a tROAS,
// 5 otherwise; the network's conversions take the same floors.
func (c campaign) floor() float64 {
	if c.platformTROAS != nil {
		return minPayersROAS
	}
	return minPayers
}

// proposal is one finding a rule wants.
type proposal struct {
	kind, rule, reason string
	evidence, proposed map[string]any
	impact             *int64
	// entity is the campaign unless the finding is pinned elsewhere.
	entity string
}

// money writes micros as a person reads them: 12.50 USD.
func money(micros float64, cur string) string { return fmt.Sprintf("%.2f %s", micros/1e6, cur) }

// unitWord is how a reason names a conversion in the campaign's unit.
func unitWord(unit string, n float64) string {
	one, many := "new paying customer", "new paying customers"
	if unit != UnitPayers {
		one, many = "conversion", "conversions"
	}
	if n == 1 {
		return one
	}
	return many
}

// fallbackNote says a finding is judged on the network's numbers.
func fallbackNote(c campaign) string {
	if c.unit == UnitPayers {
		return ""
	}
	return " Judged on the network's own conversions until a target cost per new paying customer is set."
}

// evidence is what every finding carries: the unit, the window and the
// campaign's numbers over it.
func (c campaign) evidence(n numbers) map[string]any {
	ev := map[string]any{"unit": c.unit, "days": n.Days, "window_from": c.last.AddDate(0, 0, -(n.Days - 1)).Format(time.DateOnly), "window_to": c.last.Format(time.DateOnly),
		"lag_days": c.lagDays, "spend_micros": n.Spend, "clicks": n.Clicks, "conversions": n.Conv, "signups": n.Signups, "currency": c.currency}
	if c.unit == UnitPayers {
		ev["payers"] = n.Conv
	}
	if n.Conv > 0 {
		ev["cost_per_conversion_micros"] = int64(math.Round(n.cost()))
		if c.unit == UnitPayers {
			ev["cost_per_payer_micros"] = int64(math.Round(n.cost()))
		}
	}
	if c.target != nil {
		ev["target_micros"] = *c.target
	}
	if c.budget != nil {
		ev["daily_budget_micros"] = *c.budget
	}
	if c.objective != "" {
		ev["campaign_type"] = c.objective
	}
	return ev
}

// perPayer spreads a saving over the brand's payers in the window: the
// change in cost per new paying customer, negative for a saving. Nil
// when the brand has no payers to spread it over.
func (c campaign) perPayer(savedMicros int64) *int64 {
	if c.brandPayers <= 0 || savedMicros == 0 {
		return nil
	}
	v := -int64(math.Round(float64(savedMicros) / float64(c.brandPayers)))
	return &v
}

func ptrInt(v int64) *int64 { return &v }

// impactMicros rounds an expected change in cost per payer to micros, nil when
// the maths had nothing to go on (no payers: an infinite cost).
func impactMicros(v float64) *int64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return ptrInt(int64(math.Round(v)))
}

// step moves a budget by a fraction, to whole cents, never under a cent.
func step(budget int64, by float64) int64 {
	v := int64(math.Round(float64(budget)*(1+by)/10_000)) * 10_000
	if v < 10_000 {
		v = 10_000
	}
	return v
}

// changeAllowed says whether a change-class finding may be made on the
// campaign now: no change in the last week (a target move in two).
func (c campaign) changeAllowed(now time.Time, targetMove bool) bool {
	if c.lastChange != nil && now.Sub(*c.lastChange) < lockDays*24*time.Hour {
		return false
	}
	if targetMove && c.lastTargetChange != nil && now.Sub(*c.lastTargetChange) < targetLock*24*time.Hour {
		return false
	}
	return true
}

// budgetRaise: lost impression share (budget) of a tenth or more over 7
// days (or, without the network's word, the budget run out on 4 of the
// last 7 days) and a cost per payer at or under the target.
func budgetRaise(c campaign) *proposal {
	if c.status != "active" || c.budget == nil || *c.budget <= 0 || c.target == nil {
		return nil
	}
	w := c.window()
	if w.Conv < c.floor() || w.cost() > float64(*c.target) {
		return nil
	}
	limited := false
	ev := c.evidence(w)
	if c.signals != nil && c.caps.Network == "google_ads" && !c.pmax() {
		ev["budget_lost_impression_share"] = c.signals.BudgetLostImpressionShare
		limited = c.signals.BudgetLostImpressionShare >= raiseLostIS
	} else {
		ev["days_budget_spent"] = c.capped
		limited = c.capped >= raiseCappedDays
	}
	if !limited {
		return nil
	}
	nb := step(*c.budget, budgetStep)
	// The extra fifth buys payers at about raiseMarginal times the target,
	// so the blended cost rises by a share of that.
	impact := impactMicros(w.cost() * budgetStep * (raiseMarginal - 1))
	how := fmt.Sprintf("it lost %.0f%% of its impressions to the budget", budgetLostPercentOf(c.signals))
	if _, ok := ev["days_budget_spent"]; ok {
		how = fmt.Sprintf("the budget ran out on %d of the last 7 days", c.capped)
	}
	return &proposal{kind: BudgetUp, rule: RuleBudgetRaise, evidence: ev, impact: impact, proposed: map[string]any{"daily_budget_micros": nb},
		reason: fmt.Sprintf("Raise %s's daily budget by a fifth to %s? A %s cost %s over the last %d days, within the target of %s, and %s.%s",
			c.name, money(float64(nb), c.currency), unitWord(c.unit, 1), money(w.cost(), c.currency), w.Days, money(float64(*c.target), c.currency), how, fallbackNote(c))}
}

// budgetCut: a payer costs more than 1.3 times the target over the window
// with the floor met, or three targets were spent with no payer in 14
// days; a fifth off, never on a winner.
func budgetCut(c campaign) *proposal {
	if c.status != "active" || c.budget == nil || *c.budget <= 0 || c.target == nil || c.top {
		return nil
	}
	t := float64(*c.target)
	w := c.window()
	z := c.over(cutZeroDays)
	ev := c.evidence(w)
	var why string
	var impact *int64
	switch {
	case w.Conv >= c.floor() && w.cost() > cutOverTarget*t:
		why = fmt.Sprintf("A %s cost %s over the last %d days against a target of %s (%.0f %s).", unitWord(c.unit, 1), money(w.cost(), c.currency), w.Days, money(t, c.currency), w.Conv, unitWord(c.unit, w.Conv))
		// The last fifth of the budget buys payers at about 1.5 times the
		// average: without it, the rest costs less per payer.
		saved := float64(w.Spend) * budgetStep
		lost := saved / (cutMarginalCost * w.cost())
		if w.Conv-lost > 0 {
			after := (float64(w.Spend) - saved) / (w.Conv - lost)
			impact = impactMicros(after - w.cost())
		}
	case z.Conv == 0 && float64(z.Spend) >= cutZeroTargets*t:
		ev = c.evidence(z)
		why = fmt.Sprintf("It spent %s in %d days, more than three targets' worth (%s), with no %s.", money(float64(z.Spend), c.currency), z.Days, money(t, c.currency), unitWord(c.unit, 1))
		impact = c.perPayer(int64(float64(z.Spend) * budgetStep))
	default:
		return nil
	}
	nb := step(*c.budget, -budgetStep)
	return &proposal{kind: BudgetDown, rule: RuleBudgetCut, evidence: ev, impact: impact, proposed: map[string]any{"daily_budget_micros": nb},
		reason: fmt.Sprintf("Cut %s's daily budget by a fifth to %s? %s%s", c.name, money(float64(nb), c.currency), why, fallbackNote(c))}
}

// pause: three targets spent with no payer over max(14 days, 3 cycles),
// never a winner.
func pause(c campaign) *proposal {
	if c.status != "active" || c.target == nil || c.top {
		return nil
	}
	days := max(pauseMinDays, pauseCycles*c.cycleDays)
	n := c.over(days)
	if n.Conv > 0 || float64(n.Spend) < pauseSpendTarget*float64(*c.target) {
		return nil
	}
	return &proposal{kind: Pause, rule: RulePause, evidence: c.evidence(n), impact: c.perPayer(n.Spend), proposed: map[string]any{"status": "paused"},
		reason: fmt.Sprintf("Pause %s? It spent %s in %d days, more than three times what a %s may cost (%s), and brought none.%s",
			c.name, money(float64(n.Spend), c.currency), days, unitWord(c.unit, 1), money(float64(*c.target), c.currency), fallbackNote(c))}
}

// resume: paused at least a week, and while it ran in the last 90 days a
// payer cost at most eight tenths of the target, with at least 10.
func resume(c campaign, now time.Time) *proposal {
	if c.status != "paused" || c.target == nil {
		return nil
	}
	if c.pausedSince != nil && now.Sub(*c.pausedSince) < resumePausedDays*24*time.Hour {
		return nil
	}
	n := c.over(resumeDays)
	if n.Conv < resumeMinPayers || n.cost() > resumeUnderShare*float64(*c.target) {
		return nil
	}
	var impact *int64
	if c.brandCost > 0 {
		impact = impactMicros(n.cost() - c.brandCost)
	}
	return &proposal{kind: Resume, rule: RuleResume, evidence: c.evidence(n), impact: impact, proposed: map[string]any{"status": "active"},
		reason: fmt.Sprintf("Resume %s? While it ran in the last %d days a %s cost %s, well under the target of %s (%.0f %s).%s",
			c.name, resumeDays, unitWord(c.unit, 1), money(n.cost(), c.currency), money(float64(*c.target), c.currency), n.Conv, unitWord(c.unit, n.Conv), fallbackNote(c))}
}

// targetStep is how far a target moves toward actual: half the gap,
// between the min and max steps.
func targetStep(gap, lo, hi float64) float64 {
	return math.Min(hi, math.Max(lo, gap/2))
}

// targetTighten: a tCPA or tROAS set, and the campaign beats it by 15%
// over 30 days with 30 payers: move the target 10 to 20% toward actual,
// never past it. The network's target is in its own conversions, so it is
// compared with the campaign's own cost per conversion (or return on
// spend); the payers are the floor.
func targetTighten(c campaign, now time.Time) *proposal {
	if c.status != "active" || !c.caps.SupportsTargets || !c.changeAllowed(now, true) {
		return nil
	}
	n := c.over(tightenDays)
	if n.Conv < tightenMinPayers {
		return nil
	}
	plat := c.platformNumbers(tightenDays)
	ev := c.evidence(n)
	ev["platform_conversions"], ev["platform_spend_micros"] = plat.Conv, plat.Spend
	switch {
	case c.platformTCPA != nil && plat.Conv > 0:
		actual := plat.cost()
		t := float64(*c.platformTCPA)
		if actual > t*(1-tightenBeatBy) {
			return nil
		}
		gap := (t - actual) / t
		nt := int64(math.Max(actual, t*(1-targetStep(gap, targetStepMin, targetStepMax))))
		nt = nt / 10_000 * 10_000
		if nt <= 0 || nt >= *c.platformTCPA {
			return nil
		}
		ev["platform_target_cpa_micros"], ev["platform_cost_per_conversion_micros"] = *c.platformTCPA, int64(actual)
		shareDown := 1 - float64(nt)/t
		return &proposal{kind: TargetTighten, rule: RuleTargetTighten, evidence: ev, impact: impactMicros(-n.cost() * shareDown),
			proposed: map[string]any{"target_cpa_micros": nt},
			reason: fmt.Sprintf("Lower %s's target cost per conversion from %s to %s? Over the last %d days a conversion cost it %s, %.0f%% under its target, with %.0f %s.%s",
				c.name, money(t, c.currency), money(float64(nt), c.currency), tightenDays, money(actual, c.currency), gap*100, n.Conv, unitWord(c.unit, n.Conv), fallbackNote(c))}
	case c.platformTROAS != nil && plat.Spend > 0 && n.Revenue > 0:
		if n.Conv < minPayersROAS {
			return nil
		}
		// Revenue is in minor units of the brand's currency; spend in micros.
		actual := float64(n.Revenue) * 10_000 / float64(plat.Spend)
		t := *c.platformTROAS
		if actual < t*(1+tightenBeatBy) {
			return nil
		}
		gap := (actual - t) / t
		nt := math.Min(actual, t*(1+targetStep(gap, targetStepMin, targetStepMax)))
		nt = math.Round(nt*100) / 100
		if nt <= t {
			return nil
		}
		ev["platform_target_roas"], ev["return_on_spend"] = t, math.Round(actual*100)/100
		shareUp := nt/t - 1
		return &proposal{kind: TargetTighten, rule: RuleTargetTighten, evidence: ev, impact: impactMicros(-n.cost() * shareUp / (1 + shareUp)),
			proposed: map[string]any{"target_roas": nt},
			reason: fmt.Sprintf("Raise %s's target return on spend from %.0f%% to %.0f%%? Over the last %d days its new customers' revenue returned %.0f%% of spend, with %.0f %s.%s",
				c.name, t*100, nt*100, tightenDays, actual*100, n.Conv, unitWord(c.unit, n.Conv), fallbackNote(c))}
	}
	return nil
}

// targetLoosen: the target holds the campaign back (actual over the
// target and the budget not the limit), or Performance Max under-spends:
// loosen 10 to 20% (PMax 20 to 30%). Waits for a person; the network's
// direction says which way volume is expected to move.
func targetLoosen(c campaign, now time.Time) *proposal {
	if c.status != "active" || !c.caps.SupportsTargets || !c.changeAllowed(now, true) || (c.platformTCPA == nil && c.platformTROAS == nil) {
		return nil
	}
	n := c.over(tightenDays)
	plat := c.platformNumbers(tightenDays)
	if n.Conv < c.floor() {
		return nil
	}
	lo, hi := targetStepMin, targetStepMax
	why := ""
	under := false
	if c.pmax() && c.budget != nil && *c.budget > 0 {
		week := c.over(7)
		if float64(week.Spend) < pmaxUnderspend*float64(*c.budget)*7 {
			under = true
			lo, hi = loosenPMaxMin, loosenPMaxMax
			why = fmt.Sprintf("It spent %s in the last 7 days against a budget of %s a day: the target holds it back.", money(float64(week.Spend), c.currency), money(float64(*c.budget), c.currency))
		}
	}
	if !under {
		if c.signals == nil || c.signals.BudgetLostImpressionShare >= loosenLostISMax || plat.Conv < c.floor() {
			return nil
		}
		why = fmt.Sprintf("It loses only %.0f%% of its impressions to the budget, so the target is the limit.", budgetLostPercentOf(c.signals))
	}
	ev := c.evidence(n)
	ev["platform_conversions"], ev["platform_spend_micros"] = plat.Conv, plat.Spend
	if c.signals != nil {
		ev["budget_lost_impression_share"] = c.signals.BudgetLostImpressionShare
	}
	dir := c.caps.TargetDirectionByType[c.objective]
	if dir == 0 {
		dir = 1
	}
	ev["expected_volume_direction"] = dir
	expect := "volume should rise"
	if dir < 0 {
		expect = "on this campaign type the network may lower volume instead"
	}
	switch {
	case c.platformTCPA != nil && plat.Conv > 0:
		actual, t := plat.cost(), float64(*c.platformTCPA)
		if !under && actual <= t {
			return nil
		}
		gap := math.Max(0, (actual-t)/t)
		if under && gap == 0 {
			gap = lo * 2
		}
		nt := int64(t*(1+targetStep(gap, lo, hi))) / 10_000 * 10_000
		ev["platform_target_cpa_micros"], ev["platform_cost_per_conversion_micros"] = *c.platformTCPA, int64(actual)
		return &proposal{kind: TargetLoosen, rule: RuleTargetLoosen, evidence: ev, impact: impactMicros(n.cost() * (float64(nt)/t - 1)),
			proposed: map[string]any{"target_cpa_micros": nt},
			reason: fmt.Sprintf("Raise %s's target cost per conversion from %s to %s? %s A conversion cost it %s over the last %d days; %s.%s",
				c.name, money(t, c.currency), money(float64(nt), c.currency), why, money(actual, c.currency), tightenDays, expect, fallbackNote(c))}
	case c.platformTROAS != nil && plat.Spend > 0:
		t := *c.platformTROAS
		actual := float64(n.Revenue) * 10_000 / float64(plat.Spend)
		if !under && actual >= t {
			return nil
		}
		gap := 0.0
		if actual < t {
			gap = (t - actual) / t
		}
		if under && gap == 0 {
			gap = lo * 2
		}
		nt := math.Round(t*(1-targetStep(gap, lo, hi))*100) / 100
		if nt <= 0 || nt >= t {
			return nil
		}
		ev["platform_target_roas"], ev["return_on_spend"] = t, math.Round(actual*100)/100
		return &proposal{kind: TargetLoosen, rule: RuleTargetLoosen, evidence: ev, impact: impactMicros(n.cost() * (t/nt - 1)),
			proposed: map[string]any{"target_roas": nt},
			reason: fmt.Sprintf("Lower %s's target return on spend from %.0f%% to %.0f%%? %s Its new customers' revenue returned %.0f%% of spend over the last %d days; %s.%s",
				c.name, t*100, nt*100, why, actual*100, tightenDays, expect, fallbackNote(c))}
	}
	return nil
}

// biddingLadder: maximise conversions with no target, 30 payers in 30
// days at a cost within 15% of the target (or no target to compare): set
// the network's target at what a conversion actually cost, never above.
func biddingLadder(c campaign, now time.Time) *proposal {
	if c.status != "active" || !c.caps.SupportsTargets || c.bidding != connectors.BidMaxConversions || c.platformTCPA != nil || c.platformTROAS != nil || !c.changeAllowed(now, true) {
		return nil
	}
	n := c.over(ladderDays)
	plat := c.platformNumbers(ladderDays)
	if n.Conv < ladderMinPayers || plat.Conv <= 0 {
		return nil
	}
	if c.target != nil && n.cost() > float64(*c.target)*(1+ladderWithin) {
		return nil
	}
	cpa := int64(plat.cost()) / 10_000 * 10_000
	if cpa < 10_000 {
		return nil
	}
	ev := c.evidence(n)
	ev["platform_conversions"], ev["platform_spend_micros"], ev["platform_cost_per_conversion_micros"], ev["bidding_strategy"] = plat.Conv, plat.Spend, cpa, c.bidding
	return &proposal{kind: BiddingTarget, rule: RuleBiddingLadder, evidence: ev, impact: ptrInt(0),
		proposed: map[string]any{"strategy": connectors.BidMaxConversions, "target_cpa_micros": cpa},
		reason: fmt.Sprintf("Bid %s to a target cost per conversion of %s? It bids for as many conversions as it can with no target, and brought %.0f %s in the last %d days at a conversion cost of %s. A target holds that cost while it learns.%s",
			c.name, money(float64(cpa), c.currency), n.Conv, unitWord(c.unit, n.Conv), ladderDays, money(float64(cpa), c.currency), fallbackNote(c))}
}

// staleTarget: a target untouched for 90 days that the campaign misses
// (or beats) by more than a quarter over 30 days. An alert: a person looks.
func staleTarget(c campaign, now time.Time) *proposal {
	if c.status != "active" || (c.platformTCPA == nil && c.platformTROAS == nil) {
		return nil
	}
	if c.lastTargetChange != nil && now.Sub(*c.lastTargetChange) < staleDays*24*time.Hour {
		return nil
	}
	n := c.over(tightenDays)
	plat := c.platformNumbers(tightenDays)
	if plat.Conv < c.floor() {
		return nil
	}
	ev := c.evidence(n)
	ev["platform_conversions"], ev["platform_spend_micros"], ev["target_age_days"] = plat.Conv, plat.Spend, staleDays
	switch {
	case c.platformTCPA != nil:
		actual, t := plat.cost(), float64(*c.platformTCPA)
		if math.Abs(actual-t)/t <= staleGap {
			return nil
		}
		ev["platform_target_cpa_micros"], ev["platform_cost_per_conversion_micros"] = *c.platformTCPA, int64(actual)
		return &proposal{kind: AlertTargetStale, rule: RuleStaleTarget, evidence: ev,
			reason: fmt.Sprintf("%s's target cost per conversion (%s) hasn't changed in %d days, and a conversion actually cost %s over the last %d days: is the target still right?",
				c.name, money(t, c.currency), staleDays, money(actual, c.currency), tightenDays)}
	case plat.Spend > 0:
		actual, t := float64(n.Revenue)*10_000/float64(plat.Spend), *c.platformTROAS
		if n.Revenue <= 0 || math.Abs(actual-t)/t <= staleGap {
			return nil
		}
		ev["platform_target_roas"], ev["return_on_spend"] = t, math.Round(actual*100)/100
		return &proposal{kind: AlertTargetStale, rule: RuleStaleTarget, evidence: ev,
			reason: fmt.Sprintf("%s's target return on spend (%.0f%%) hasn't changed in %d days, and new customers' revenue actually returned %.0f%% over the last %d days: is the target still right?",
				c.name, t*100, staleDays, actual*100, tightenDays)}
	}
	return nil
}

// platformNumbers are the network's own numbers over n days, whatever the
// unit: the network's targets are compared in them.
func (c campaign) platformNumbers(n int) numbers {
	var out numbers
	out.Days = n
	for i := 0; i < n; i++ {
		d := c.days[c.last.AddDate(0, 0, -i).Format(time.DateOnly)]
		out.Spend += d.spend
		out.Clicks += d.clicks
		out.Impressions += d.impressions
		out.Conv += d.conv
	}
	return out
}

// signalsView helps a nil-safe read of the network's report.
type signalsView = connectors.AdCampaignSignals

func budgetLostPercentOf(s *signalsView) float64 {
	if s == nil {
		return 0
	}
	return s.BudgetLostImpressionShare * 100
}

// ---- keywords, search terms and ads ----

// term is one search over the days read, with its payers where the
// ledger ties a keyword to the search.
type term struct {
	text, groupID, groupExternal, groupName string
	spend, clicks                           int64
	conv                                    float64 // the network's
	payers, signups                         int
	exists                                  bool // already a keyword in its ad group
}

// keyword is one keyword over the days read.
type keyword struct {
	id, externalID, groupExternal, groupName, text, matchType string
	spend, clicks                                             int64
	conv                                                      float64
	payers                                                    int
}

// ad is one ad over the days read.
type ad struct {
	id, externalID, groupID, groupExternal, groupName, name string
	impressions                                             int64
	conv                                                    float64
	payers                                                  int
}

// convOf is a slice's conversions in the campaign's unit.
func (c campaign) convOf(platform float64, payers int) float64 {
	if c.unit == UnitPayers {
		return float64(payers)
	}
	return platform
}

// negativeKeyword: the worst search by spend among those with 10 clicks,
// no payer (and none of the network's conversions either, so a search
// that converts through a broad keyword is never negated on a missing
// keyword match) and cost at least 50 of the currency, 2% of the
// campaign's spend or two targets. Then words and phrases across the
// campaign's searches with 150 clicks and no payer, and searches whose
// sign-ups never pay.
func negativeKeyword(c campaign, terms []term) []*proposal {
	if c.status != "active" || !c.caps.HasKeywords || c.pmax() || len(terms) == 0 {
		return nil
	}
	w := c.over(termDays)
	floorMicros := math.Max(float64(negTermMinCurrency)*1e6, negTermSpendShare*float64(w.Spend))
	if c.target != nil {
		floorMicros = math.Max(floorMicros, negTermTargets*float64(*c.target))
	}
	var out []*proposal
	var worst *term
	var examples []map[string]any
	var wasted int64
	for i := range terms {
		t := &terms[i]
		if t.payers > 0 || t.conv > 0 || t.clicks < negTermClicks || float64(t.spend) < floorMicros {
			continue
		}
		wasted += t.spend
		if len(examples) < 5 {
			examples = append(examples, map[string]any{"search": t.text, "clicks": t.clicks, "spend_micros": t.spend, "ad_group": t.groupName})
		}
		if worst == nil || t.spend > worst.spend {
			worst = t
		}
	}
	if worst != nil {
		ev := c.evidence(w)
		ev["search"], ev["ad_group"], ev["search_clicks"], ev["search_spend_micros"], ev["examples"], ev["wasted_spend_micros"], ev["cost_floor_micros"] = worst.text, worst.groupName, worst.clicks, worst.spend, examples, wasted, int64(floorMicros)
		out = append(out, &proposal{kind: NegativeAdd, rule: RuleNegative, evidence: ev, impact: c.perPayer(wasted),
			proposed: map[string]any{"keyword": worst.text, "match_type": "exact"},
			reason: fmt.Sprintf("Add %q as an exact negative keyword to %s? People searching it clicked %d times and cost %s in %d days with no %s.%s",
				worst.text, c.name, worst.clicks, money(float64(worst.spend), c.currency), termDays, unitWord(c.unit, 1), fallbackNote(c))})
	}
	// N-grams: every word and two- or three-word phrase in the searches.
	type gram struct {
		clicks, spend int64
		conv          float64
		payers        int
		terms         int
	}
	grams := map[string]*gram{}
	for _, t := range terms {
		words := strings.Fields(strings.ToLower(t.text))
		seen := map[string]bool{}
		for n := 1; n <= 3; n++ {
			for i := 0; i+n <= len(words); i++ {
				g := strings.Join(words[i:i+n], " ")
				if seen[g] || g == strings.ToLower(t.text) {
					continue
				}
				seen[g] = true
				x := grams[g]
				if x == nil {
					x = &gram{}
					grams[g] = x
				}
				x.clicks += t.clicks
				x.spend += t.spend
				x.conv += t.conv
				x.payers += t.payers
				x.terms++
			}
		}
	}
	var bestGram string
	var best *gram
	for g, x := range grams {
		if x.payers > 0 || x.conv > 0 || x.clicks <= negNgramClicks || x.terms < 2 || len(g) < 3 {
			continue
		}
		if best == nil || x.spend > best.spend || (x.spend == best.spend && g < bestGram) {
			bestGram, best = g, x
		}
	}
	if best != nil && (worst == nil || !strings.EqualFold(worst.text, bestGram)) {
		ev := c.evidence(w)
		ev["ngram"], ev["ngram_clicks"], ev["ngram_spend_micros"], ev["ngram_searches"] = bestGram, best.clicks, best.spend, best.terms
		out = append(out, &proposal{kind: NegativeAdd, rule: RuleNegativeNgram, evidence: ev, impact: c.perPayer(best.spend),
			proposed: map[string]any{"keyword": bestGram, "match_type": "phrase"},
			reason: fmt.Sprintf("Add %q as a phrase negative keyword to %s? %d different searches containing it clicked %d times and cost %s in %d days with no %s.%s",
				bestGram, c.name, best.terms, best.clicks, money(float64(best.spend), c.currency), termDays, unitWord(c.unit, 1), fallbackNote(c))})
	}
	return out
}

// paymentQualityNegative: a search whose sign-ups never pay (5 sign-ups,
// no payer over 60 days), in payers only: it is what the ledger knows and
// the network can't.
func paymentQualityNegative(c campaign, terms []term) *proposal {
	if c.status != "active" || !c.caps.HasKeywords || c.pmax() || c.unit != UnitPayers {
		return nil
	}
	var worst *term
	for i := range terms {
		t := &terms[i]
		if t.payers == 0 && t.signups >= negQualitySignups && (worst == nil || t.signups > worst.signups) {
			worst = t
		}
	}
	if worst == nil {
		return nil
	}
	ev := c.evidence(c.over(negQualityDays))
	ev["search"], ev["ad_group"], ev["search_signups"], ev["search_clicks"], ev["search_spend_micros"] = worst.text, worst.groupName, worst.signups, worst.clicks, worst.spend
	return &proposal{kind: NegativeAdd, rule: RuleNegativePayer, evidence: ev, impact: c.perPayer(worst.spend),
		proposed: map[string]any{"keyword": worst.text, "match_type": "exact"},
		reason: fmt.Sprintf("Add %q as an exact negative keyword to %s? It brought %d sign-ups in %d days and not one of them has paid.",
			worst.text, c.name, worst.signups, negQualityDays)}
}

// keywordPromote: a search with 50 clicks (20 with payer evidence) and 2
// payers at or under the target that isn't a keyword in its ad group yet
// becomes an exact keyword there.
func keywordPromote(c campaign, terms []term) *proposal {
	if c.status != "active" || !c.caps.HasKeywords || c.pmax() {
		return nil
	}
	var best *term
	for i := range terms {
		t := &terms[i]
		conv := c.convOf(t.conv, t.payers)
		if t.exists || conv < promoteMinPayers || len(t.text) > 80 || len(strings.Fields(t.text)) > 10 {
			continue
		}
		need := int64(promoteClicks)
		if t.payers > 0 {
			need = promoteClicksPayer
		}
		if t.clicks < need {
			continue
		}
		if c.target != nil && float64(t.spend)/conv > float64(*c.target) {
			continue
		}
		if best == nil || conv > c.convOf(best.conv, best.payers) {
			best = t
		}
	}
	if best == nil {
		return nil
	}
	conv := c.convOf(best.conv, best.payers)
	ev := c.evidence(c.over(termDays))
	ev["search"], ev["ad_group"], ev["search_clicks"], ev["search_spend_micros"], ev["search_conversions"], ev["search_cost_per_conversion_micros"] = best.text, best.groupName, best.clicks, best.spend, conv, int64(float64(best.spend)/conv)
	return &proposal{kind: KeywordAdd, rule: RulePromote, evidence: ev, impact: ptrInt(0),
		proposed: map[string]any{"keyword": best.text, "match_type": "exact", "ad_group_id": best.groupExternal},
		reason: fmt.Sprintf("Add %q as an exact keyword in %s? People searching it clicked %d times and brought %.0f %s in %d days at %s each, but it isn't a keyword yet.%s",
			best.text, best.groupName, best.clicks, conv, unitWord(c.unit, conv), termDays, money(float64(best.spend)/conv, c.currency), fallbackNote(c))}
}

// keywordPause: a keyword that spent three targets with no payer and
// clicked three payers' worth of clicks, over 30, then 60, then 90 days;
// never on a winner. kws is by lookback (30, 60, 90 days).
func keywordPause(c campaign, kws map[int][]keyword) *proposal {
	if c.status != "active" || !c.caps.HasKeywords || c.pmax() || c.top || c.target == nil {
		return nil
	}
	w := c.over(kwLookback3)
	if w.Conv <= 0 {
		return nil
	}
	clicksPerConv := float64(w.Clicks) / w.Conv
	for _, days := range []int{kwLookback1, kwLookback2, kwLookback3} {
		var worst *keyword
		for i := range kws[days] {
			k := &kws[days][i]
			if k.payers > 0 || k.conv > 0 || float64(k.spend) < kwPauseTargets*float64(*c.target) || float64(k.clicks) < kwPauseExpected*clicksPerConv {
				continue
			}
			if worst == nil || k.spend > worst.spend {
				worst = k
			}
		}
		if worst == nil {
			continue
		}
		ev := c.evidence(c.over(days))
		ev["keyword"], ev["match_type"], ev["ad_group"], ev["keyword_clicks"], ev["keyword_spend_micros"], ev["clicks_per_conversion"] = worst.text, worst.matchType, worst.groupName, worst.clicks, worst.spend, math.Round(clicksPerConv)
		return &proposal{kind: KeywordPause, rule: RuleKeywordPause, evidence: ev, impact: c.perPayer(worst.spend),
			proposed: map[string]any{"keyword": worst.text, "match_type": worst.matchType, "keyword_id": worst.externalID, "ad_group_id": worst.groupExternal},
			reason: fmt.Sprintf("Pause the %s keyword %q in %s? It spent %s in %d days, over three targets' worth, on %d clicks with no %s.%s",
				worst.matchType, worst.text, worst.groupName, money(float64(worst.spend), c.currency), days, worst.clicks, unitWord(c.unit, 1), fallbackNote(c))}
	}
	return nil
}

// adPause: in an ad group with two or more enabled ads, one with under
// half the other's payers per thousand impressions, over 100 payers
// between them.
func adPause(c campaign, ads []ad) *proposal {
	if c.status != "active" || !c.caps.Manageable {
		return nil
	}
	byGroup := map[string][]ad{}
	for _, a := range ads {
		byGroup[a.groupID] = append(byGroup[a.groupID], a)
	}
	var loser, winner *ad
	for _, list := range byGroup {
		if len(list) < adPauseMinAds {
			continue
		}
		sort.Slice(list, func(i, j int) bool { return c.cpi(list[i]) > c.cpi(list[j]) })
		best, worst := list[0], list[len(list)-1]
		if best.impressions == 0 || worst.impressions == 0 || c.convOf(best.conv, best.payers)+c.convOf(worst.conv, worst.payers) < adPauseMinPayers {
			continue
		}
		if c.cpi(worst) < adPauseRatio*c.cpi(best) {
			loser, winner = &worst, &best
			break
		}
	}
	if loser == nil {
		return nil
	}
	ev := c.evidence(c.over(c.windowDays))
	ev["ad"], ev["ad_group"], ev["ad_conversions_per_thousand"], ev["other_ad"], ev["other_conversions_per_thousand"], ev["ad_impressions"], ev["ad_conversions"] = loser.name, loser.groupName, math.Round(c.cpi(*loser)*100)/100, winner.name, math.Round(c.cpi(*winner)*100)/100, loser.impressions, c.convOf(loser.conv, loser.payers)
	return &proposal{kind: AdPause, rule: RuleAdPause, evidence: ev, impact: ptrInt(0), entity: loser.id,
		proposed: map[string]any{"ad_id": loser.externalID, "ad_group_id": loser.groupExternal},
		reason: fmt.Sprintf("Pause the ad %q in %s? It brings %.2f %s per thousand impressions, under half the %.2f of %q beside it.%s",
			loser.name, loser.groupName, c.cpi(*loser), unitWord(c.unit, 2), c.cpi(*winner), winner.name, fallbackNote(c))}
}

// cpi is an ad's conversions per thousand impressions.
func (c campaign) cpi(a ad) float64 {
	if a.impressions <= 0 {
		return 0
	}
	return c.convOf(a.conv, a.payers) * 1000 / float64(a.impressions)
}

// group is one ad group over 30 days, with its keywords' texts and match
// types.
type group struct {
	id, externalID, name string
	payers               float64
	keywords             []keyword
}

// consolidate: ad groups in a campaign each under 30 payers in 30 days
// that share their intent (a keyword text, or the same keyword texts
// across match types) merge into the one with the most payers.
func consolidate(c campaign, groups []group) *proposal {
	if c.status != "active" || !c.caps.HasKeywords || c.pmax() || len(groups) < 2 {
		return nil
	}
	texts := func(g group) map[string]bool {
		out := map[string]bool{}
		for _, k := range g.keywords {
			out[strings.ToLower(strings.Trim(k.text, `"[]+`))] = true
		}
		return out
	}
	var a, b *group
	var shared []string
	for i := range groups {
		for j := i + 1; j < len(groups); j++ {
			gi, gj := &groups[i], &groups[j]
			if gi.payers >= consolidateMax || gj.payers >= consolidateMax {
				continue
			}
			ti, tj := texts(*gi), texts(*gj)
			var common []string
			for t := range ti {
				if tj[t] {
					common = append(common, t)
				}
			}
			if len(common) == 0 {
				continue
			}
			sort.Strings(common)
			if a == nil || len(common) > len(shared) {
				a, b, shared = gi, gj, common
			}
		}
	}
	if a == nil {
		return nil
	}
	into, from := a, b
	if b.payers > a.payers {
		into, from = b, a
	}
	ev := c.evidence(c.over(consolidateDays))
	ev["ad_groups"], ev["shared_keywords"], ev["into_ad_group"], ev["into_conversions"], ev["from_conversions"] = []string{a.name, b.name}, shared, into.name, into.payers, from.payers
	return &proposal{kind: Consolidate, rule: RuleConsolidate, evidence: ev, impact: ptrInt(0),
		proposed: map[string]any{"into_ad_group_id": into.externalID, "from_ad_group_id": from.externalID, "keywords": shared},
		reason: fmt.Sprintf("Merge the ad group %q into %q? They bid on the same searches (%s) and each brought under %d %s in %d days, so the network learns from two halves instead of one whole.%s",
			from.name, into.name, strings.Join(shared, ", "), consolidateMax, unitWord(c.unit, 2), consolidateDays, fallbackNote(c))}
}

// segment is one slice of a campaign over the exclusion days: the
// network's numbers and the ledger's payers for it.
type segment struct {
	connectors.AdSegment
	payers int
}

// exclusion: a device, place, placement or hour window with 300 clicks
// (100 on a brand campaign) and no payer over 60 days; or search partners
// or Display expansion costing one and a half times the campaign's cost
// per payer.
func exclusion(c campaign, segs []segment) *proposal {
	if c.status != "active" || c.signals == nil || !c.caps.Allows(ExclusionAdd) {
		return nil
	}
	allowed := map[string]bool{}
	for _, k := range []string{connectors.ExcludeDevice, connectors.ExcludeLocation, connectors.ExcludePlacement, connectors.ExcludeNetwork, connectors.ExcludeSchedule} {
		allowed[k] = true
	}
	if !c.caps.SupportsScheduleWindows {
		delete(allowed, connectors.ExcludeSchedule)
	}
	need := int64(exclClicks)
	if c.brandCampaign() {
		need = exclBrandClicks
	}
	w := c.over(exclDays)
	var worst *segment
	for i := range segs {
		s := &segs[i]
		if !allowed[s.Kind] && s.Kind != "hour" {
			continue
		}
		conv := c.segConv(*s)
		switch s.Kind {
		case connectors.ExcludeNetwork:
			if s.Value != "search_partners" && s.Value != "display" {
				continue
			}
			if (s.Value == "search_partners" && !c.signals.SearchPartners) || (s.Value == "display" && !c.signals.DisplayExpansion) {
				continue
			}
			rest := w.Spend - s.SpendMicros
			restConv := w.Conv - conv
			if restConv < c.floor() || s.Clicks < need/3 {
				continue
			}
			restCost := float64(rest) / restConv
			segCost := math.Inf(1)
			if conv > 0 {
				segCost = float64(s.SpendMicros) / conv
			}
			if segCost <= exclNetworkCost*restCost {
				continue
			}
		default:
			if conv > 0 || s.Clicks < need {
				continue
			}
		}
		if worst == nil || s.SpendMicros > worst.SpendMicros {
			worst = s
		}
	}
	if worst == nil {
		return nil
	}
	x := connectors.AdExclusion{Kind: worst.Kind, Value: worst.Value, Detail: worst.Detail, Label: worst.Label}
	what := worst.Label
	if what == "" {
		what = worst.Value
	}
	if worst.Kind == "hour" {
		// One hour with the floor met: exclude that hour of every day.
		h := worst.Value
		end := fmt.Sprintf("%02d", (atoiHour(h)+1)%25)
		x = connectors.AdExclusion{Kind: connectors.ExcludeSchedule, Value: h + "-" + end, Label: h + ":00 to " + end + ":00"}
		what = x.Label
	}
	ev := c.evidence(w)
	ev["segment"], ev["segment_clicks"], ev["segment_spend_micros"], ev["segment_conversions"], ev["exclusion"] = worst.Kind+" "+what, worst.Clicks, worst.SpendMicros, c.segConv(*worst), x
	kindWord := map[string]string{connectors.ExcludeDevice: "on " + what + " devices", connectors.ExcludeLocation: "in " + what, connectors.ExcludePlacement: "on " + what,
		connectors.ExcludeNetwork: "on " + strings.ReplaceAll(what, "_", " "), connectors.ExcludeSchedule: "from " + what}[x.Kind]
	why := fmt.Sprintf("it clicked %d times and cost %s there in %d days with no %s", worst.Clicks, money(float64(worst.SpendMicros), c.currency), exclDays, unitWord(c.unit, 1))
	if x.Kind == connectors.ExcludeNetwork {
		why = fmt.Sprintf("a %s cost %s there against %s on the rest of the campaign over %d days", unitWord(c.unit, 1), money(float64(worst.SpendMicros)/math.Max(1, c.segConv(*worst)), c.currency),
			money(float64(w.Spend-worst.SpendMicros)/math.Max(1, w.Conv-c.segConv(*worst)), c.currency), exclDays)
	}
	return &proposal{kind: ExclusionAdd, rule: RuleExclusion, evidence: ev, impact: c.perPayer(worst.SpendMicros),
		proposed: map[string]any{"exclusion": x},
		reason:   fmt.Sprintf("Stop %s showing %s? %s.%s", c.name, kindWord, strings.ToUpper(why[:1])+why[1:], fallbackNote(c))}
}

func atoiHour(s string) int {
	var h int
	fmt.Sscanf(s, "%d", &h)
	return h
}

// brandLeak: a non-brand Search or Performance Max campaign where a fifth
// of the search clicks name the brand. On Search the brand becomes a
// phrase negative (a saving, auto); on Performance Max a brand exclusion
// that waits for a person.
func brandLeak(c campaign, terms []term) *proposal {
	if c.status != "active" || c.brandName == "" || c.brandCampaign() || len(terms) == 0 || !(c.caps.HasKeywords || c.pmax()) {
		return nil
	}
	brand := strings.ToLower(strings.Join(strings.Fields(c.brandName), " "))
	if len(brand) < 3 {
		return nil
	}
	var all, leak int64
	var spend int64
	for _, t := range terms {
		all += t.clicks
		if strings.Contains(strings.ToLower(t.text), brand) {
			leak += t.clicks
			spend += t.spend
		}
	}
	if all == 0 || float64(leak)/float64(all) < brandLeakShare {
		return nil
	}
	ev := c.evidence(c.over(brandLeakDays))
	ev["brand"], ev["brand_clicks"], ev["search_clicks"], ev["brand_share"], ev["brand_spend_micros"] = c.brandName, leak, all, math.Round(float64(leak)/float64(all)*100)/100, spend
	if c.pmax() {
		x := connectors.AdExclusion{Kind: connectors.ExcludeBrand, Value: c.brandName, Label: c.brandName}
		ev["exclusion"] = x
		return &proposal{kind: ExclusionAdd, rule: RuleBrandLeak, evidence: ev, impact: ptrInt(0), proposed: map[string]any{"exclusion": x},
			reason: fmt.Sprintf("Exclude searches for %s from %s? %.0f%% of its search clicks in the last %d days named the brand, which your brand campaign would catch for less.",
				c.brandName, c.name, float64(leak)/float64(all)*100, brandLeakDays)}
	}
	return &proposal{kind: NegativeAdd, rule: RuleBrandLeak, evidence: ev, impact: c.perPayer(spend),
		proposed: map[string]any{"keyword": brand, "match_type": "phrase"},
		reason: fmt.Sprintf("Add %q as a phrase negative keyword to %s? %.0f%% of its search clicks in the last %d days named the brand, which your brand campaign would catch for less.",
			brand, c.name, float64(leak)/float64(all)*100, brandLeakDays)}
}
