package adsuggest

import (
	"strings"
	"testing"
	"time"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/paidads"
)

func ptr(v int64) *int64 { return &v }

// mk is a Google search campaign on a brand judged in payers with a
// target of 10, a 14-day window, no lag, and n days of spend, network
// conversions and payers ending yesterday.
func mk(n int, spend int64, conv float64, payers int) campaign {
	c := campaign{id: "c1", externalID: "7", name: "Spring search", connector: "google_ads", currency: "USD", status: "active", objective: "search",
		brandName: "Northpeak", budget: ptr(30_000_000), unit: UnitPayers, target: ptr(10_000_000), caps: paidads.Caps("google_ads"),
		windowDays: 14, cycleDays: 1, last: time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC), days: map[string]dayNum{}, brandPayers: 20, brandCost: 12_000_000}
	b := "b1"
	c.brand = &b
	fill(&c, n, spend, conv, payers)
	return c
}

func fill(c *campaign, n int, spend int64, conv float64, payers int) {
	for i := 0; i < n; i++ {
		c.days[c.last.AddDate(0, 0, -i).Format(time.DateOnly)] = dayNum{spend: spend, clicks: 20, impressions: 400, conv: conv, payers: payers}
	}
}

var now = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

func TestBudgetRules(t *testing.T) {
	// Under target with the budget run out 7 of 7 days: a raise, a fifth up.
	c := mk(14, 30_000_000, 5, 5)
	c.capped = 7
	p := budgetRaise(c)
	if p == nil || p.kind != BudgetUp || p.proposed["daily_budget_micros"] != int64(36_000_000) || p.rule != RuleBudgetRaise || p.evidence["unit"] != UnitPayers || !strings.HasSuffix(p.reason, "days.") {
		t.Fatalf("raise: %+v", p)
	}
	if p.impact == nil || *p.impact <= 0 {
		t.Fatalf("a raise costs a little per payer: %v", p.impact)
	}
	// With the network's word, lost impression share decides.
	c.signals = &connectors.AdCampaignSignals{BudgetLostImpressionShare: 0.05}
	if budgetRaise(c) != nil {
		t.Fatal("5% lost to budget is no raise")
	}
	c.signals.BudgetLostImpressionShare = 0.12
	if budgetRaise(c) == nil {
		t.Fatal("12% lost to budget is a raise")
	}
	c.capped = 2
	c.signals = nil
	if budgetRaise(c) != nil {
		t.Fatal("budget left on 5 of 7 days is no raise")
	}
	// Over 1.3 times the target with the floor met: a cut, a fifth down,
	// with a negative impact; never on a winner.
	c = mk(14, 30_000_000, 2, 2) // 28 payers at 15 each
	p = budgetCut(c)
	if p == nil || p.kind != BudgetDown || p.proposed["daily_budget_micros"] != int64(24_000_000) || p.impact == nil || *p.impact >= 0 {
		t.Fatalf("cut: %+v", p)
	}
	c.top = true
	if budgetCut(c) != nil {
		t.Fatal("a winner is never cut")
	}
	// Too few payers to say: nothing (3 payers at 140 each).
	c = mk(14, 30_000_000, 0, 0)
	c.days[c.last.Format(time.DateOnly)] = dayNum{spend: 30_000_000, payers: 3}
	if budgetCut(c) != nil {
		t.Fatal("under the floor is no cut")
	}
	// Three targets with no payer in 14 days: a cut; six targets over
	// three cycles: a pause.
	c = mk(14, 3_000_000, 0, 0) // 42 spent, no payer
	if p = budgetCut(c); p == nil || p.evidence["days"] != cutZeroDays {
		t.Fatalf("zero cut: %+v", p)
	}
	if p = pause(c); p == nil || p.kind != Pause || p.impact == nil || *p.impact != -2_100_000 {
		t.Fatalf("pause: %+v", p)
	}
	c.top = true
	if pause(c) != nil {
		t.Fatal("a winner is never paused")
	}
	// The network's unit when the brand has no target.
	c = mk(14, 30_000_000, 2, 0)
	c.unit, c.target = UnitPlatform, ptr(10_000_000)
	if p = budgetCut(c); p == nil || !strings.Contains(p.reason, "network's own conversions") || p.evidence["unit"] != UnitPlatform {
		t.Fatalf("fallback: %+v", p)
	}
	// Resume: paused a week, ran under eight tenths of the target with 10 payers.
	c = mk(90, 1_000_000, 1, 1)
	c.status = "paused"
	since := now.AddDate(0, 0, -10)
	c.pausedSince = &since
	if p = resume(c, now); p == nil || p.kind != Resume || p.impact == nil || *p.impact != -11_000_000 {
		t.Fatalf("resume: %+v", p)
	}
	since = now.AddDate(0, 0, -3)
	if resume(c, now) != nil {
		t.Fatal("paused three days is too soon")
	}
}

func TestTargetRules(t *testing.T) {
	// A tCPA of 10 the campaign beats by 40% (conversions cost 6): tighten
	// 20% toward actual, never past it, to 8.
	c := mk(30, 30_000_000, 5, 2) // 60 payers; 150 network conversions at 6
	c.platformTCPA = ptr(10_000_000)
	c.bidding = "maximize_conversions"
	p := targetTighten(c, now)
	if p == nil || p.kind != TargetTighten || p.proposed["target_cpa_micros"] != int64(8_000_000) || p.impact == nil || *p.impact >= 0 {
		t.Fatalf("tighten: %+v", p)
	}
	// Within two weeks of a target move: nothing.
	moved := now.AddDate(0, 0, -5)
	c.lastTargetChange = &moved
	if targetTighten(c, now) != nil {
		t.Fatal("a target moved five days ago is left alone")
	}
	c.lastTargetChange = nil
	// Beating it by only a tenth: nothing.
	c = mk(30, 30_000_000, 3.3, 2)
	c.platformTCPA = ptr(10_000_000)
	if targetTighten(c, now) != nil {
		t.Fatal("a tenth under is no tighten")
	}
	// Under the payer floor: nothing.
	c = mk(30, 30_000_000, 5, 0)
	c.days[c.last.Format(time.DateOnly)] = dayNum{spend: 30_000_000, conv: 5, payers: 10}
	c.platformTCPA = ptr(10_000_000)
	if targetTighten(c, now) != nil {
		t.Fatal("10 payers are under the floor of 30")
	}
	// Loosen: conversions cost 15 against a target of 10 with 4% lost to
	// budget: raise the target 20% to 12; volume should rise on Search.
	c = mk(30, 30_000_000, 2, 1)
	c.platformTCPA = ptr(10_000_000)
	c.signals = &connectors.AdCampaignSignals{BudgetLostImpressionShare: 0.04}
	p = targetLoosen(c, now)
	if p == nil || p.kind != TargetLoosen || p.proposed["target_cpa_micros"] != int64(12_000_000) || p.evidence["expected_volume_direction"] != 1 || !strings.Contains(p.reason, "volume should rise") {
		t.Fatalf("loosen: %+v", p)
	}
	c.signals.BudgetLostImpressionShare = 0.3
	if targetLoosen(c, now) != nil {
		t.Fatal("budget-limited: raise the budget, not the target")
	}
	// Performance Max under-spending: loosen 20 to 30%, and the network
	// may lower volume on a tROAS.
	c = mk(30, 5_000_000, 2, 1)
	c.objective = "performance_max"
	c.platformTROAS = func() *float64 { v := 4.0; return &v }()
	for d := range c.days {
		x := c.days[d]
		x.revenue = 2000 // 20.00 a day on 5.00 spend: 400%
		c.days[d] = x
	}
	c.signals = &connectors.AdCampaignSignals{}
	p = targetLoosen(c, now)
	if p == nil || p.proposed["target_roas"] != 3.2 || p.evidence["expected_volume_direction"] != -1 || !strings.Contains(p.reason, "lower volume") {
		t.Fatalf("pmax loosen: %+v", p)
	}
	// The ladder: maximise conversions, no target, 30 payers in 30 days
	// at a cost within 15% of the target: bid to actual, floored to cents,
	// never above it.
	c = mk(30, 20_000_000, 1.5, 1)
	c.bidding = "maximize_conversions"
	c.target = ptr(20_000_000)
	p = biddingLadder(c, now)
	if p == nil || p.kind != BiddingTarget || p.proposed["target_cpa_micros"] != int64(13_330_000) {
		t.Fatalf("ladder: %+v", p)
	}
	c.target = ptr(15_000_000) // payers cost 20: over 15% off
	if biddingLadder(c, now) != nil {
		t.Fatal("too far from the target to lock in")
	}
	c.platformTCPA = ptr(1)
	c.target = nil
	if biddingLadder(c, now) != nil {
		t.Fatal("a target is already set")
	}
	// Stale: a target untouched 90 days that actual misses by a third.
	c = mk(30, 30_000_000, 2, 2)
	c.platformTCPA = ptr(10_000_000)
	if p = staleTarget(c, now); p == nil || p.kind != AlertTargetStale || !strings.HasSuffix(p.reason, "right?") {
		t.Fatalf("stale: %+v", p)
	}
	c.platformTCPA = ptr(14_000_000)
	if staleTarget(c, now) != nil {
		t.Fatal("within a quarter is not stale")
	}
}

func TestKeywordRules(t *testing.T) {
	c := mk(30, 30_000_000, 2, 2)
	terms := []term{
		{text: "free crm download", groupExternal: "71", groupName: "Intent", spend: 60_000_000, clicks: 40},                         // waste
		{text: "cheap crm", groupExternal: "71", groupName: "Intent", spend: 90_000_000, clicks: 60},                                 // more waste
		{text: "crm for agencies", groupExternal: "71", groupName: "Intent", spend: 25_000_000, clicks: 70, conv: 4, payers: 3},      // a keyword
		{text: "crm pricing", groupExternal: "71", groupName: "Intent", spend: 20_000_000, clicks: 25, conv: 1},                      // converts: never a negative
		{text: "crm trial", groupExternal: "71", groupName: "Intent", spend: 10_000_000, clicks: 30, conv: 0, payers: 0, signups: 6}, // sign-ups that never pay
	}
	negs := negativeKeyword(c, terms)
	if len(negs) < 1 || negs[0].kind != NegativeAdd || negs[0].proposed["keyword"] != "cheap crm" || negs[0].rule != RuleNegative || negs[0].impact == nil || *negs[0].impact != -7_500_000 {
		t.Fatalf("negative: %+v", negs)
	}
	if len(negs) != 1 {
		t.Fatalf("no n-gram reaches 150 clicks with no conversion: %+v", negs[1])
	}
	// An n-gram across many searches: "crm" has every click, but it
	// converts; "cheap" alone is one search. Make "download" waste.
	more := append(terms, term{text: "crm download free", spend: 50_000_000, clicks: 120}, term{text: "download crm", spend: 10_000_000, clicks: 5})
	negs = negativeKeyword(c, more)
	if len(negs) != 2 || negs[1].rule != RuleNegativeNgram || negs[1].proposed["keyword"] != "download" || negs[1].proposed["match_type"] != "phrase" {
		t.Fatalf("n-gram: %+v", negs)
	}
	if p := paymentQualityNegative(c, terms); p == nil || p.proposed["keyword"] != "crm trial" || p.rule != RuleNegativePayer {
		t.Fatalf("payment quality: %+v", p)
	}
	c.unit = UnitPlatform
	if paymentQualityNegative(c, terms) != nil {
		t.Fatal("payment quality needs the ledger")
	}
	c.unit = UnitPayers
	if p := keywordPromote(c, terms); p == nil || p.kind != KeywordAdd || p.proposed["keyword"] != "crm for agencies" || p.proposed["ad_group_id"] != "71" {
		t.Fatalf("promote: %+v", p)
	}
	// Performance Max gets no keyword findings.
	c.objective = "performance_max"
	if negativeKeyword(c, terms) != nil || keywordPromote(c, terms) != nil || paymentQualityNegative(c, terms) != nil {
		t.Fatal("pmax has no keywords")
	}
	c.objective = "search"
	// Brand leak: a fifth of the clicks name the brand on a non-brand
	// campaign: a phrase negative on Search, a brand exclusion on PMax.
	leaky := append(terms, term{text: "northpeak login", clicks: 100, spend: 5_000_000})
	p := brandLeak(c, leaky)
	if p == nil || p.kind != NegativeAdd || p.rule != RuleBrandLeak || p.proposed["keyword"] != "northpeak" || p.proposed["match_type"] != "phrase" {
		t.Fatalf("brand leak: %+v", p)
	}
	c.objective = "performance_max"
	if p = brandLeak(c, leaky); p == nil || p.kind != ExclusionAdd || p.proposed["exclusion"].(connectors.AdExclusion).Kind != connectors.ExcludeBrand {
		t.Fatalf("pmax brand leak: %+v", p)
	}
	c.name = "Northpeak brand"
	if brandLeak(c, leaky) != nil {
		t.Fatal("the brand campaign may show for the brand")
	}
	c.objective, c.name = "search", "Spring search"
	// Keyword pause: three targets with no payer and three payers' worth
	// of clicks (the campaign clicks 10 a payer), 30 days first.
	kws := map[int][]keyword{
		30: {{externalID: "91", groupExternal: "71", groupName: "Intent", text: "crm software", matchType: "broad", spend: 35_000_000, clicks: 40}},
		60: {{externalID: "91", groupExternal: "71", groupName: "Intent", text: "crm software", matchType: "broad", spend: 70_000_000, clicks: 80}},
		90: {{externalID: "91", groupExternal: "71", groupName: "Intent", text: "crm software", matchType: "broad", spend: 90_000_000, clicks: 120}},
	}
	if p = keywordPause(c, kws); p == nil || p.kind != KeywordPause || p.proposed["keyword_id"] != "91" || p.evidence["days"] != 30 {
		t.Fatalf("keyword pause: %+v", p)
	}
	kws[30][0].clicks = 5 // too few clicks in 30 days: the 60-day look fires
	if p = keywordPause(c, kws); p == nil || p.evidence["days"] != 60 {
		t.Fatalf("keyword pause at 60 days: %+v", p)
	}
	c.top = true
	if keywordPause(c, kws) != nil {
		t.Fatal("a winner's keywords are left alone")
	}
	c.top = false
	// Ad pause: two ads in a group, one at under half the other's payers
	// per thousand impressions, 100 payers between them.
	ads := []ad{{id: "a1", externalID: "81", groupID: "g1", groupExternal: "71", groupName: "Intent", name: "RSA one", impressions: 10_000, payers: 80},
		{id: "a2", externalID: "82", groupID: "g1", groupExternal: "71", groupName: "Intent", name: "RSA two", impressions: 10_000, payers: 30}}
	if p = adPause(c, ads); p == nil || p.kind != AdPause || p.proposed["ad_id"] != "82" {
		t.Fatalf("ad pause: %+v", p)
	}
	ads[1].payers = 50
	if adPause(c, ads) != nil {
		t.Fatal("half or more is not paused")
	}
	// Consolidate: two small ad groups on the same keyword texts.
	groups := []group{{id: "g1", externalID: "71", name: "CRM exact", payers: 12, keywords: []keyword{{text: "[crm software]", matchType: "exact"}}},
		{id: "g2", externalID: "72", name: "CRM phrase", payers: 8, keywords: []keyword{{text: `"crm software"`, matchType: "phrase"}}},
		{id: "g3", externalID: "73", name: "Agencies", payers: 50, keywords: []keyword{{text: "crm for agencies", matchType: "exact"}}}}
	if p = consolidate(c, groups); p == nil || p.kind != Consolidate || p.proposed["into_ad_group_id"] != "71" || p.proposed["from_ad_group_id"] != "72" {
		t.Fatalf("consolidate: %+v", p)
	}
}

func TestExclusionRule(t *testing.T) {
	c := mk(60, 10_000_000, 1, 1)
	c.signals = &connectors.AdCampaignSignals{SearchPartners: true}
	segs := []segment{
		{AdSegment: connectors.AdSegment{Kind: connectors.ExcludeDevice, Value: "tablet", Label: "tablet", Clicks: 350, SpendMicros: 70_000_000}, payers: 0},
		{AdSegment: connectors.AdSegment{Kind: connectors.ExcludeDevice, Value: "mobile", Label: "mobile", Clicks: 900, SpendMicros: 300_000_000, Conversions: 20}, payers: 30},
		{AdSegment: connectors.AdSegment{Kind: connectors.ExcludeLocation, Value: "geoTargetConstants/2036", Label: "AU", Clicks: 200, SpendMicros: 40_000_000}, payers: 0},
	}
	p := exclusion(c, segs)
	if p == nil || p.kind != ExclusionAdd || p.proposed["exclusion"].(connectors.AdExclusion).Value != "tablet" || p.impact == nil || *p.impact != -3_500_000 {
		t.Fatalf("device exclusion: %+v", p)
	}
	// A brand campaign needs only 100 clicks: the place fires too, and
	// the one wasting most wins.
	c.name = "Northpeak brand"
	if p = exclusion(c, segs); p == nil || p.proposed["exclusion"].(connectors.AdExclusion).Value != "tablet" {
		t.Fatalf("brand: %+v", p)
	}
	segs[0].Clicks = 50
	if p = exclusion(c, segs); p == nil || p.proposed["exclusion"].(connectors.AdExclusion).Kind != connectors.ExcludeLocation {
		t.Fatalf("place: %+v", p)
	}
	// Search partners cost twice the rest per payer: off.
	c.name = "Spring search"
	partners := []segment{{AdSegment: connectors.AdSegment{Kind: connectors.ExcludeNetwork, Value: "search_partners", Clicks: 200, SpendMicros: 200_000_000, Conversions: 4}, payers: -1}}
	p = exclusion(c, partners)
	if p == nil || p.proposed["exclusion"].(connectors.AdExclusion).Value != "search_partners" || !strings.Contains(p.reason, "search partners") {
		t.Fatalf("partners: %+v", p)
	}
	c.signals.SearchPartners = false
	if exclusion(c, partners) != nil {
		t.Fatal("partners already off")
	}
	// A quiet hour becomes a schedule window.
	c.signals.SearchPartners = true
	hours := []segment{{AdSegment: connectors.AdSegment{Kind: "hour", Value: "03", Label: "03:00", Clicks: 400, SpendMicros: 20_000_000}, payers: 0}}
	if p = exclusion(c, hours); p == nil || p.proposed["exclusion"].(connectors.AdExclusion).Value != "03-04" {
		t.Fatalf("hour: %+v", p)
	}
	// Without the network's report nothing fires.
	c.signals = nil
	if exclusion(c, segs) != nil {
		t.Fatal("no signals, no exclusion")
	}
}

func TestWindowAndVerdict(t *testing.T) {
	if learn, days, _ := Window(BudgetUp, 14, 2, false); learn != 3 || days != 16 {
		t.Fatalf("budget window: %d+%d", learn, days)
	}
	if learn, days, note := Window(BiddingTarget, 21, 3, false); learn != 14 || days != 24 || !strings.Contains(note, "14 days to settle") {
		t.Fatalf("target window: %d+%d %s", learn, days, note)
	}
	if learn, days, _ := Window(BudgetDown, 40, 10, false); learn != 3 || days != 39 {
		t.Fatalf("capped: %d+%d", learn, days)
	}
	if learn, days, note := Window(BudgetDown, 14, 0, true); learn != 14 || days != 28 || !strings.Contains(note, "days 15 to 42") {
		t.Fatalf("pmax: %d+%d %s", learn, days, note)
	}
	if w, l := windowOf(paidads.Lag{Median: 10 * 24 * time.Hour}); w != 20 || l != 10 {
		t.Fatalf("lag of ten days: %d %d", w, l)
	}
	if w, l := windowOf(paidads.Lag{Median: 20 * 24 * time.Hour}); w != 40 || l != 14 {
		t.Fatalf("lag capped: %d %d", w, l)
	}
	target := ptr(10_000_000)
	for _, tc := range []struct {
		kind          string
		before, after numbers
		want          string
	}{
		{BudgetUp, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 360e6, Conv: 60}, Worked},
		{BudgetUp, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 360e6, Conv: 50}, DidNotWork},
		{BudgetUp, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 360e6, Conv: 52}, DidNotWork},
		{BudgetUp, numbers{Spend: 30e6, Conv: 3}, numbers{Spend: 36e6, Conv: 4}, NotSure},
		{BudgetDown, numbers{Spend: 300e6, Conv: 20}, numbers{Spend: 240e6, Conv: 20}, Worked},
		{BudgetDown, numbers{Spend: 300e6, Conv: 20}, numbers{Spend: 240e6, Conv: 12}, DidNotWork},
		{BudgetDown, numbers{Spend: 300e6, Conv: 20}, numbers{Spend: 298e6, Conv: 20}, NotSure},
		{Pause, numbers{Spend: 500e6, Conv: 50}, numbers{Spend: 400e6, Conv: 49}, Worked},
		{Pause, numbers{Spend: 500e6, Conv: 50}, numbers{Spend: 400e6, Conv: 30}, DidNotWork},
		{Resume, numbers{}, numbers{Spend: 80e6, Conv: 10}, Worked},
		{Resume, numbers{}, numbers{Spend: 200e6, Conv: 10}, DidNotWork},
		{Resume, numbers{}, numbers{Spend: 80e6, Conv: 2}, NotSure},
		{KeywordAdd, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 300e6, Conv: 55}, Worked},
		{KeywordAdd, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 300e6, Conv: 35}, DidNotWork},
		{NegativeAdd, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 270e6, Conv: 50}, Worked},
		{NegativeAdd, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 250e6, Conv: 35}, DidNotWork},
		{TargetTighten, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 250e6, Conv: 48}, Worked},
		{TargetLoosen, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 400e6, Conv: 60}, Worked},
		{TargetLoosen, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 800e6, Conv: 50}, DidNotWork},
		{ExclusionAdd, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 280e6, Conv: 50}, Worked},
		{BiddingTarget, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 300e6, Conv: 48}, Worked},
		{BiddingTarget, numbers{Spend: 300e6, Conv: 50}, numbers{Spend: 300e6, Conv: 30}, DidNotWork},
	} {
		if got, note := Verdict(tc.kind, UnitPayers, tc.before, tc.after, target, minPayers); got != tc.want || note == "" {
			t.Errorf("%s %+v → %+v: %s (%s), want %s", tc.kind, tc.before, tc.after, got, note, tc.want)
		}
	}
	// The tROAS floor: 20 payers each side is not enough.
	if got, _ := Verdict(TargetTighten, UnitPayers, numbers{Spend: 300e6, Conv: 20}, numbers{Spend: 200e6, Conv: 20}, target, minPayersROAS); got != NotSure {
		t.Fatalf("troas floor: %s", got)
	}
	if _, note := Verdict(BudgetDown, UnitPlatform, numbers{Spend: 300e6, Conv: 20}, numbers{Spend: 240e6, Conv: 20}, target, minPayers); !strings.Contains(note, "conversion") {
		t.Fatalf("platform words: %s", note)
	}
}

func TestChangeForAndInverse(t *testing.T) {
	cur := Current{Status: "active", DailyBudgetMicros: ptr(30_000_000), Bidding: "maximize_conversions", TargetCPAMicros: ptr(10_000_000)}
	kind, before, after, err := ChangeFor(BudgetDown, map[string]any{"daily_budget_micros": 24_000_000.0}, cur)
	if err != nil || kind != ChangeBudget || *after.DailyBudgetMicros != 24_000_000 || *before.DailyBudgetMicros != 30_000_000 {
		t.Fatalf("budget: %s %+v %+v %v", kind, before, after, err)
	}
	if k, inv, err := Inverse(kind, before, after); err != nil || k != ChangeBudget || *inv.DailyBudgetMicros != 30_000_000 {
		t.Fatalf("budget back: %s %+v %v", k, inv, err)
	}
	kind, before, after, err = ChangeFor(TargetTighten, map[string]any{"target_cpa_micros": int64(8_000_000)}, cur)
	if err != nil || kind != ChangeTarget || *after.Target.TargetCPAMicros != 8_000_000 || *before.Target.TargetCPAMicros != 10_000_000 {
		t.Fatalf("target: %s %+v %+v %v", kind, before, after, err)
	}
	if k, inv, err := Inverse(kind, before, after); err != nil || k != ChangeTarget || *inv.Target.TargetCPAMicros != 10_000_000 {
		t.Fatalf("target back: %s %+v %v", k, inv, err)
	}
	kind, _, after, err = ChangeFor(ExclusionAdd, map[string]any{"exclusion": map[string]any{"kind": "device", "value": "tablet"}}, cur)
	if err != nil || kind != ChangeExclusionAdd || after.Exclusion.Value != "tablet" {
		t.Fatalf("exclusion: %s %+v %v", kind, after, err)
	}
	if k, inv, err := Inverse(kind, Change{}, after); err != nil || k != ChangeExclusionRemove || inv.Exclusion.Kind != "device" {
		t.Fatalf("exclusion back: %s %+v %v", k, inv, err)
	}
	kind, before, after, err = ChangeFor(KeywordPause, map[string]any{"keyword_id": "91", "ad_group_id": "71", "keyword": "crm software", "match_type": "broad"}, cur)
	if err != nil || kind != ChangeKeywordPause || after.KeywordID != "91" || before.Status != "active" {
		t.Fatalf("keyword pause: %s %+v %v", kind, after, err)
	}
	if k, inv, _ := Inverse(kind, before, after); k != ChangeKeywordResume || inv.KeywordID != "91" || inv.Status != "active" {
		t.Fatalf("keyword back: %s %+v", k, inv)
	}
	kind, _, after, _ = ChangeFor(BiddingTarget, map[string]any{"target_cpa_micros": 13_330_000.0}, Current{Bidding: "maximize_conversions"})
	if k, inv, err := Inverse(kind, Change{Bidding: &connectors.AdBidding{}}, after); err != nil || k != ChangeBidding || inv.Bidding.TargetCPAMicros != nil || inv.Bidding.Strategy != "maximize_conversions" {
		t.Fatalf("ladder back drops the target: %s %+v %v", k, inv, err)
	}
	if _, _, _, err := ChangeFor(Consolidate, nil, cur); err != ErrNotApplicable {
		t.Fatalf("consolidate: %v", err)
	}
	if _, _, err := Inverse(ChangeKeywordAdd+"x", Change{}, Change{}); err != ErrNotApplicable {
		t.Fatalf("unknown: %v", err)
	}
	if !Saves(BudgetDown) || !Saves(ExclusionAdd) || !Saves(TargetTighten) || Saves(BudgetUp) || Saves(TargetLoosen) || Saves("overspend_pause") {
		t.Fatalf("savings: %v", SavingKinds())
	}
}
