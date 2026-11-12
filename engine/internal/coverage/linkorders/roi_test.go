package linkorders

import (
	"testing"
	"time"
)

func TestOrderCostPrefersDonorAmount(t *testing.T) {
	donor, cur := int64(1500), "USD"
	quoted, qcur := int64(2000), "EUR"
	got, gotCur := orderCost(roiOrderRow{DonorAmountMinor: &donor, Currency: &cur, QuotedAmountMinor: &quoted, QuotedCurrency: &qcur})
	if got == nil || *got != 1500 || gotCur == nil || *gotCur != "USD" {
		t.Fatalf("donor amount: %v %v", got, gotCur)
	}
	got, gotCur = orderCost(roiOrderRow{QuotedAmountMinor: &quoted, QuotedCurrency: &qcur})
	if got == nil || *got != 2000 || gotCur == nil || *gotCur != "EUR" {
		t.Fatalf("quoted fallback: %v %v", got, gotCur)
	}
	got, gotCur = orderCost(roiOrderRow{})
	if got != nil || gotCur != nil {
		t.Fatalf("empty: %v %v", got, gotCur)
	}
}

func TestCurrencyTotalsStaySeparate(t *testing.T) {
	got := currencyTotals(map[string]int64{"EUR": 200, "USD": 100})
	if len(got) != 2 || got[0].Currency != "EUR" || got[0].AmountMinor != 200 || got[1].Currency != "USD" {
		t.Fatalf("%v", got)
	}
	if got := currencyTotals(nil); len(got) != 0 {
		t.Fatalf("empty: %v", got)
	}
}

func TestCountRefsUsesFirstSeenAndLost(t *testing.T) {
	t0 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	lost := t0.AddDate(0, 0, 5)
	refs := map[string][]rdEvent{
		"https://northpeak.example/page": {
			{Domain: "a.example", First: t0.AddDate(0, 0, -10)},
			{Domain: "b.example", First: t0.AddDate(0, 0, 2)},
			{Domain: "c.example", First: t0.AddDate(0, 0, -2), Lost: &lost},
		},
	}
	pages := []string{"https://northpeak.example/page"}
	before, ok := countRefs(pages, refs, t0.Add(-time.Second))
	if !ok || before != 2 { // a and c (c still live just before pub)
		t.Fatalf("before: %d ok=%v", before, ok)
	}
	after, ok := countRefs(pages, refs, t0.AddDate(0, 0, 30))
	if !ok || after != 2 { // a and b; c already lost
		t.Fatalf("after: %d ok=%v", after, ok)
	}
}

func TestBuildROIWindowTrafficAndCost(t *testing.T) {
	pub := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	now := pub.AddDate(0, 0, 40)
	page := "https://northpeak.example/page"
	pos8, pos5 := 8.0, 5.0
	gsc := map[string][]ImpactDayPoint{
		page: {
			{Day: "2026-05-10", Clicks: 10, Impressions: 100, Position: &pos8},
			{Day: "2026-06-10", Clicks: 25, Impressions: 200, Position: &pos5},
		},
	}
	amount := int64(3000)
	w := buildROIWindow(now, &pub, 30, []string{page}, gsc, nil, nil, &amount)
	if !w.Elapsed || w.Traffic == nil || !w.Traffic.Available || w.Traffic.ClicksChange != 15 {
		t.Fatalf("traffic: %+v", w.Traffic)
	}
	if w.CostPerClickGained == nil || *w.CostPerClickGained != 200 {
		t.Fatalf("cost per click: %v", w.CostPerClickGained)
	}
	if w.Traffic.PositionChange == nil || *w.Traffic.PositionChange != -3 {
		t.Fatalf("position change: %v", w.Traffic.PositionChange)
	}
	// 90-day window has not elapsed.
	w90 := buildROIWindow(now, &pub, 90, []string{page}, gsc, nil, nil, &amount)
	if w90.Elapsed {
		t.Fatalf("90 should still be open: %+v", w90)
	}
}
