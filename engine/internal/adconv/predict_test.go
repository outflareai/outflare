package adconv

import (
	"context"
	"testing"
)

// A group's share is pulled toward the brand's, a throwaway address is
// never qualified, and too little history predicts nothing.
func TestQualityPredict(t *testing.T) {
	ctx := context.Background()
	var q Quality
	q.fill(ctx, []qualityRow{
		{domain: "gmail.com", country: "US", signups: 60, paid: 12, valueMinor: 60000},
		{domain: "gmail.com", country: "BR", signups: 60, paid: 0},
	})
	if !q.Enough || q.Signups != 120 || q.Paid != 12 || q.PaidShare != 0.1 || q.ValuePerPayerMicros != 50_000_000 {
		t.Fatalf("cohort: %+v", q)
	}
	share, v, ok := q.Predict("us", EmailFree)
	if want := 14.5 / 85; !ok || v == nil || share < want-1e-9 || share > want+1e-9 || *v != 8_529_412 {
		t.Errorf("US free: %v %v %v", share, v, ok)
	}
	if share, _, ok := q.Predict("BR", EmailFree); ok || share >= 0.1 {
		t.Errorf("BR free: %v %v", share, ok)
	}
	// A group never seen gets the brand's own share, and is qualified.
	if share, v, ok := q.Predict("FR", EmailWork); !ok || share != 0.1 || *v != 5_000_000 {
		t.Errorf("FR work: %v %v %v", share, v, ok)
	}
	if _, _, ok := q.Predict("FR", EmailThrowaway); ok {
		t.Error("a throwaway address is qualified")
	}
	if len(q.SegmentRows) != 2 || q.SegmentRows[0].EmailKind != EmailFree {
		t.Errorf("segments: %+v", q.SegmentRows)
	}

	var thin Quality
	thin.fill(ctx, []qualityRow{{domain: "gmail.com", country: "US", signups: 20, paid: 5, valueMinor: 5000}})
	if _, v, ok := thin.Predict("US", EmailFree); thin.Enough || v != nil || !ok {
		t.Errorf("thin history: %+v %v %v", thin, v, ok)
	}
	if EmailKind(ctx, "mailinator.com") != EmailThrowaway || EmailKind(ctx, "") != EmailUnknown {
		t.Error("email kinds")
	}
}
