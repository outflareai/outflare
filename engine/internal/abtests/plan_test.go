package abtests

import (
	"testing"
	"time"
)

// The planner reproduces the owner's table (4% base rate, tuned for +25%)
// and its estimate agrees with the test itself.
func TestVisitorsNeeded(t *testing.T) {
	for _, tc := range []struct {
		lift     float64
		min, max int64
	}{{0.10, 50_000, 54_000}, {0.25, 7_700, 8_300}, {0.50, 2_250, 2_550}} {
		n := VisitorsNeeded(rate(0.04), MetricConversion, tc.lift, 0.25, Alpha)
		if n < tc.min || n > tc.max {
			t.Errorf("+%.0f%%: %d visitors per version, want %d–%d", tc.lift*100, n, tc.min, tc.max)
		}
		a := Arm{Visitors: n, Conversions: int64(0.04*float64(n) + 0.5)}
		b := Arm{Visitors: n, Conversions: int64(0.04*(1+tc.lift)*float64(n) + 0.5)}
		if c := CompareSeries([][]Arm{{a, b}}, MetricConversion, 0.25, 0.04)[0]; c.PValue >= Alpha {
			t.Errorf("+%.0f%%: at %d the test isn't safe to stop (p = %v)", tc.lift*100, n, c.PValue)
		}
	}
	if VisitorsNeeded(rate(0), MetricConversion, 0.1, 0.25, Alpha) != 0 || VisitorsNeeded(rate(0.6), MetricConversion, 1, 0.25, Alpha) != 0 {
		t.Error("impossible plans should be 0")
	}
}

func TestMakePlan(t *testing.T) {
	// 10,000 visitors a week split 50/50: 5,000 per version.
	p := MakePlan(rate(0.04), MetricConversion, 10_000, []int{50, 50}, 0)
	if p.MinEffect != DefaultMinEffect || p.WeeklyPerVersion != 5_000 || len(p.Rows) != len(PlanLifts) {
		t.Fatalf("plan = %+v", p)
	}
	// +25% needs about 8,000 per version: about 1.6 weeks.
	if p.Weeks < 1.4 || p.Weeks > 1.8 {
		t.Errorf("weeks for +25%% = %v", p.Weeks)
	}
	// In 8 weeks a smaller change is findable than in 4.
	if !(p.Detectable8Weeks > 0 && p.Detectable8Weeks < p.Detectable4Weeks) {
		t.Errorf("detectable: 4 weeks %v, 8 weeks %v", p.Detectable4Weeks, p.Detectable8Weeks)
	}
	if n := VisitorsNeeded(rate(0.04), MetricConversion, p.Detectable4Weeks, DefaultMinEffect, Alpha); n > 20_000 {
		t.Errorf("the 4-week lift needs %d visitors, more than 4 weeks hold", n)
	}
	// An uneven split waits on the smaller version.
	if q := MakePlan(rate(0.04), MetricConversion, 10_000, []int{80, 20}, 0); q.WeeklyPerVersion != 2_000 || q.Weeks <= p.Weeks {
		t.Errorf("80/20 plan = %+v", q)
	}
	// No traffic or no conversions: nothing to plan.
	if q := MakePlan(rate(0), MetricConversion, 10_000, []int{50, 50}, 0); q.Weeks != 0 || len(q.Rows) != 0 {
		t.Errorf("no baseline: %+v", q)
	}
}

func TestBaseline(t *testing.T) {
	if Baseline(Traffic{Visitors: 150, Converted: 10}, MetricConversion) != 0 || Baseline(Traffic{Visitors: 5000}, MetricConversion) != 0 {
		t.Error("too little history should record no baseline")
	}
	if b := Baseline(Traffic{Visitors: 1000, Converted: 30}, MetricConversion); b != 0.03 {
		t.Errorf("baseline = %v", b)
	}
}

func TestRecommend(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	result := func(ctl, b Arm, srm bool) Result {
		ctl.Variant, b.Variant = "control", "b"
		return Result{Arms: []Arm{ctl, b}, SampleRatio: srm, CorrectedFor: 1, Goals: []GoalResult{{Metric: MetricConversion, Arms: []Arm{ctl, b}, Baseline: 0.04,
			Comparisons: CompareSeries([][]Arm{{ctl, b}}, MetricConversion, 0.25, 0.04)}}}
	}
	for _, tc := range []struct {
		name string
		r    Result
		days int
		want string
	}{
		{"winner", result(Arm{Visitors: 4000, Conversions: 160}, Arm{Visitors: 4000, Conversions: 260}, false), 14, RecommendShip},
		{"control wins", result(Arm{Visitors: 4000, Conversions: 260}, Arm{Visitors: 4000, Conversions: 160}, false), 14, RecommendKeepControl},
		{"broken split", result(Arm{Visitors: 4000, Conversions: 160}, Arm{Visitors: 4000, Conversions: 260}, true), 14, RecommendFixSplit},
		{"too early", result(Arm{Visitors: 40, Conversions: 2}, Arm{Visitors: 40, Conversions: 2}, false), 1, RecommendWait},
		// 2,000 a week per version; +25% needs about 8,000: two more weeks.
		{"keep going", result(Arm{Visitors: 4000, Conversions: 160}, Arm{Visitors: 4000, Conversions: 170}, false), 14, RecommendExtend},
		// 50 a week per version: hopeless.
		{"stop", result(Arm{Visitors: 400, Conversions: 16}, Arm{Visitors: 400, Conversions: 17}, false), 56, RecommendStop},
	} {
		got, why := Recommend(tc.r, "control", 0.25, start, start.AddDate(0, 0, tc.days))
		if got != tc.want || why == "" {
			t.Errorf("%s: %s (%s), want %s", tc.name, got, why, tc.want)
		}
	}
}

// rate is the moments of a conversion goal with rate p.
func rate(p float64) Moments { return Moments{p, p * (1 - p)} }
