package adsuggest

import (
	"fmt"
	"math"
)

// Tests inside the networks (step 4 of the paid ads plan): the network
// splits a campaign's traffic or audience between the campaign as it is
// (control) and a trial, on the same days. SplitWindow plans how long one
// runs and SplitVerdict compares the arms.

// Arm is one arm's numbers over the days compared.
type Arm struct {
	Spend       int64   `json:"spend_micros"`
	Conv        float64 `json:"conversions"`
	Clicks      int64   `json:"clicks"`
	Impressions int64   `json:"impressions"`
	Days        int     `json:"days"`
}

func (a Arm) cpa() float64 { return numbers{Spend: a.Spend, Conv: a.Conv}.cost() }

// testSettle is how long a trial takes to settle before its days count:
// a new bid target makes the bidding learn again, a budget much less.
var testSettle = map[string]int{"bidding": 7, "budget": 3}

// A test's window aims for wantConv conversions in the smaller arm and
// is judged splitLagDays after it ends, for conversions reported late.
const (
	wantConv     = 30.0
	splitLagDays = 3
)

// SplitWindow is how long a test runs: its settling days, then long enough
// for wantConv conversions in the smaller arm at the campaign's rate,
// at least two weeks, at most six; then the conversion lag before it is
// judged.
func SplitWindow(kind string, convPerDay float64, splitPercent int) (settle, days int, note string) {
	settle = testSettle[kind]
	share := float64(min(splitPercent, 100-splitPercent)) / 100
	need := maxWindow - settle
	if convPerDay > 0 && share > 0 {
		need = int(math.Ceil(wantConv / (convPerDay * share)))
	}
	need = max(need, 14)
	days = min(settle+need, maxWindow)
	note = fmt.Sprintf("Runs %d days: %d to settle, then compared until the smaller side has about %d conversions (at least two weeks). Judged %d days after it ends, for late conversions.",
		days, settle, int(wantConv), splitLagDays)
	return settle, days, note
}

// SplitLag is how many days after a test ends it is judged.
const SplitLag = splitLagDays

// z is how many standard errors apart the arms' conversions per unit of
// spend are. Given the conversions both arms got, each one is the trial's
// with the chance of its share of the spend if both convert alike: a
// binomial test, positive when the trial converts more for its money.
func z(control, trial Arm) float64 {
	n := control.Conv + trial.Conv
	if n <= 0 || control.Spend <= 0 || trial.Spend <= 0 {
		return 0
	}
	p := float64(trial.Spend) / float64(control.Spend+trial.Spend)
	return (trial.Conv - n*p) / math.Sqrt(n*p*(1-p))
}

// SplitVerdict compares the arms. kind is what the trial changes; target
// the campaign's target cost per conversion, if it has one.
func SplitVerdict(kind string, control, trial Arm, target *int64) (verdict, note string, score float64) {
	if control.Conv < minPayers || trial.Conv < minPayers {
		return NotSure, fmt.Sprintf("Too few conversions to tell: %.0f in the campaign, %.0f in the trial.", control.Conv, trial.Conv), 0
	}
	score = z(control, trial)
	cc, tc := control.cpa()/1e6, trial.cpa()/1e6
	switch kind {
	case "budget":
		// More budget is worth it when the trial's extra conversions, per
		// unit of its share, cost no more than the target allows.
		if target != nil && tc <= 1.1*float64(*target)/1e6 && score > -1.96 {
			return Worked, fmt.Sprintf("The trial's conversions cost %.2f each (the campaign's %.2f), within the target of %.2f.", tc, cc, float64(*target)/1e6), score
		}
		if score <= -1.96 || (target != nil && tc > 1.5*float64(*target)/1e6) {
			return DidNotWork, fmt.Sprintf("The trial's conversions cost %.2f each against the campaign's %.2f.", tc, cc), score
		}
		return NotSure, fmt.Sprintf("The trial's conversions cost %.2f each, the campaign's %.2f.", tc, cc), score
	default:
		// A bid target is judged on conversions for the money, at 95%.
		switch {
		case score >= 1.96:
			return Worked, fmt.Sprintf("The trial converts more for its money: %.2f a conversion against the campaign's %.2f, and the difference is clear (95%%).", tc, cc), score
		case score <= -1.96:
			return DidNotWork, fmt.Sprintf("The trial converts less for its money: %.2f a conversion against the campaign's %.2f, and the difference is clear (95%%).", tc, cc), score
		}
		return NotSure, fmt.Sprintf("No clear difference yet: %.2f a conversion in the trial, %.2f in the campaign.", tc, cc), score
	}
}

// SplitOverTarget reports whether the trial's last days each cost more than
// twice the target per conversion, counted from the first day compared:
// the test is then judged early.
func SplitOverTarget(cumulative []Arm, target *int64) bool {
	if target == nil || len(cumulative) < stopRun {
		return false
	}
	for _, a := range cumulative[len(cumulative)-stopRun:] {
		if a.Spend == 0 || a.cpa() <= stopFactor*float64(*target) {
			return false
		}
	}
	return true
}
