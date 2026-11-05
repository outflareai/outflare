package adsuggest

import (
	"testing"
)

func ptr(v int64) *int64 { return &v }

func TestRules(t *testing.T) {
	base := campaign{status: "active", currency: "USD", budget: ptr(30_000_000), target: ptr(10_000_000)}
	for name, tc := range map[string]struct {
		c    campaign
		want string
	}{
		"under target, budget spent": {func() campaign { c := base; c.spend14, c.conv14, c.capped = 420_000_000, 70, 7; return c }(), BudgetUp},
		"under target, budget left":  {func() campaign { c := base; c.spend14, c.conv14, c.capped = 420_000_000, 70, 2; return c }(), ""},
		"over target":                {func() campaign { c := base; c.spend14, c.conv14 = 420_000_000, 20; return c }(), BudgetDown},
		"too few conversions":        {func() campaign { c := base; c.spend14, c.conv14 = 420_000_000, 3; return c }(), ""},
		"spends, never converts":     {func() campaign { c := base; c.spend14 = 31_000_000; return c }(), Pause},
		"no target, account's cost":  {func() campaign { c := base; c.target, c.acctCPA, c.spend14 = nil, ptr(5_000_000), 16_000_000; return c }(), Pause},
		"paused, did well":           {func() campaign { c := base; c.status, c.spend90, c.conv90 = "paused", 200_000_000, 60; return c }(), Resume},
		"paused, did badly":          {func() campaign { c := base; c.status, c.spend90, c.conv90 = "paused", 900_000_000, 60; return c }(), ""},
	} {
		p := rules(tc.c)
		got := ""
		if p != nil {
			got = p.kind
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
	if p := rules(func() campaign { c := base; c.spend14, c.conv14, c.capped = 420_000_000, 70, 7; return c }()); p.proposed["daily_budget_micros"] != int64(36_000_000) {
		t.Errorf("a fifth more: %v", p.proposed)
	}
}

func TestWindow(t *testing.T) {
	for _, tc := range []struct {
		kind                string
		rate                float64
		wantLearn, wantDays int
	}{
		{BudgetUp, 10, 3, minWindow + lagDays}, // 30 conversions in 3 days: a week
		{BudgetUp, 1, 3, 30 + lagDays},         // a month for 30
		{BudgetDown, 0.1, 3, maxWindow - 3},    // capped at six weeks
		{Resume, 5, 7, minWindow + lagDays},
		{Pause, 0, 0, minWindow + lagDays},
	} {
		learn, days, note := Window(tc.kind, tc.rate)
		if learn != tc.wantLearn || days != tc.wantDays || note == "" {
			t.Errorf("%s at %.1f a day: %d+%d, want %d+%d", tc.kind, tc.rate, learn, days, tc.wantLearn, tc.wantDays)
		}
	}
}

func TestVerdict(t *testing.T) {
	target := ptr(10_000_000)
	for _, tc := range []struct {
		kind          string
		before, after totals
		want          string
	}{
		{BudgetUp, totals{Spend: 300e6, Conv: 50}, totals{Spend: 360e6, Conv: 60}, Worked},
		{BudgetUp, totals{Spend: 300e6, Conv: 50}, totals{Spend: 360e6, Conv: 50}, DidNotWork},
		{BudgetUp, totals{Spend: 300e6, Conv: 50}, totals{Spend: 360e6, Conv: 52}, DidNotWork},
		{BudgetUp, totals{Spend: 30e6, Conv: 3}, totals{Spend: 36e6, Conv: 4}, NotSure},
		{BudgetDown, totals{Spend: 300e6, Conv: 20}, totals{Spend: 240e6, Conv: 20}, Worked},
		{BudgetDown, totals{Spend: 300e6, Conv: 20}, totals{Spend: 240e6, Conv: 12}, DidNotWork},
		{Pause, totals{Spend: 500e6, Conv: 50}, totals{Spend: 400e6, Conv: 49}, Worked},
		{Pause, totals{Spend: 500e6, Conv: 50}, totals{Spend: 400e6, Conv: 30}, DidNotWork},
		{Resume, totals{}, totals{Spend: 80e6, Conv: 10}, Worked}, // judged on its own days
		{Resume, totals{}, totals{Spend: 80e6, Conv: 2}, NotSure},
		{KeywordAdd, totals{Spend: 300e6, Conv: 50}, totals{Spend: 300e6, Conv: 55}, Worked},
		{KeywordAdd, totals{Spend: 300e6, Conv: 50}, totals{Spend: 300e6, Conv: 35}, DidNotWork},
		{NegativeAdd, totals{Spend: 300e6, Conv: 50}, totals{Spend: 270e6, Conv: 50}, Worked},
		{NegativeAdd, totals{Spend: 300e6, Conv: 50}, totals{Spend: 250e6, Conv: 35}, DidNotWork},
		{KeywordSplit, totals{Spend: 300e6, Conv: 50}, totals{Spend: 300e6, Conv: 50}, Worked},
	} {
		if got, note := Verdict(tc.kind, tc.before, tc.after, target); got != tc.want || note == "" {
			t.Errorf("%s %+v → %+v: %s (%s), want %s", tc.kind, tc.before, tc.after, got, note, tc.want)
		}
	}
}

func TestSplitVerdicts(t *testing.T) {
	target := ptr(10_000_000)
	for _, tc := range []struct {
		kind           string
		control, trial Arm
		want           string
	}{
		{"bidding", Arm{Spend: 500e6, Conv: 50}, Arm{Spend: 500e6, Conv: 80}, Worked},
		{"bidding", Arm{Spend: 500e6, Conv: 80}, Arm{Spend: 500e6, Conv: 50}, DidNotWork},
		{"bidding", Arm{Spend: 500e6, Conv: 50}, Arm{Spend: 500e6, Conv: 55}, NotSure},
		{"bidding", Arm{Spend: 50e6, Conv: 3}, Arm{Spend: 50e6, Conv: 8}, NotSure},
		{"budget", Arm{Spend: 300e6, Conv: 40}, Arm{Spend: 400e6, Conv: 45}, Worked},
		{"budget", Arm{Spend: 300e6, Conv: 40}, Arm{Spend: 600e6, Conv: 20}, DidNotWork},
	} {
		if got, note, _ := SplitVerdict(tc.kind, tc.control, tc.trial, target); got != tc.want || note == "" {
			t.Errorf("%s %+v vs %+v: %s (%s), want %s", tc.kind, tc.control, tc.trial, got, note, tc.want)
		}
	}
	if s, d, note := SplitWindow("bidding", 10, 50); s != 7 || d != 21 || note == "" {
		t.Errorf("window at 10 a day: %d+%d", s, d)
	}
	if _, d, _ := SplitWindow("budget", 0.5, 20); d != maxWindow {
		t.Errorf("slow campaign: %d", d)
	}
	over := []Arm{{Spend: 30e6, Conv: 1}, {Spend: 60e6, Conv: 2}, {Spend: 90e6, Conv: 4}}
	if !SplitOverTarget(over, target) || SplitOverTarget(over[:2], target) {
		t.Error("over target three days running")
	}
}
