package api

import (
	"testing"

	"github.com/outflareai/outflare/engine/internal/connectors"
)

// share splits exactly: in proportion, by largest remainder, evenly when
// no weight is given, and never loses or adds a micro.
func TestShare(t *testing.T) {
	sum := func(v []int64) (n int64) {
		for _, x := range v {
			n += x
		}
		return n
	}
	for _, tc := range []struct {
		total   int64
		weights []int64
		want    []int64
	}{
		{100, []int64{1, 1, 1}, []int64{34, 33, 33}},
		{10, []int64{0, 0}, []int64{5, 5}},
		{7, []int64{3, 1}, []int64{5, 2}},          // 5.25 and 1.75: the bigger remainder gets the extra micro
		{1_000_000, []int64{2_500, 1_250, 0}, nil}, // a click that cost nothing gets nothing while others did
		{0, []int64{1, 2}, []int64{0, 0}},
		{5, nil, []int64{}},
	} {
		got := share(tc.total, tc.weights)
		if len(tc.weights) > 0 && sum(got) != tc.total {
			t.Errorf("share(%d, %v) = %v: sums to %d", tc.total, tc.weights, got, sum(got))
		}
		if tc.want != nil {
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("share(%d, %v) = %v, want %v", tc.total, tc.weights, got, tc.want)
					break
				}
			}
		}
	}
	if got := share(1_000_000, []int64{2_500, 1_250, 0}); got[2] != 0 || got[0] != 666_667 || got[1] != 333_333 {
		t.Errorf("proportional: %v", got)
	}
	// Large figures don't overflow.
	if got := share(9_000_000_000_000, []int64{9_000_000_000, 1}); sum(got) != 9_000_000_000_000 {
		t.Errorf("large: %v", got)
	}
}

// A campaign day: target A's spend goes on A's clicks by their own cost;
// target B's clicks had no cost, so they share B's evenly; target C's
// spend has no click naming it, so it goes evenly over the clicks that
// named no reported target; then nothing is unattributed. Without loose
// clicks it would be.
func TestPlanNetworkCost(t *testing.T) {
	targets := []connectors.AdTargetDay{
		{Keys: []string{"pub-a", "uuid-a"}, SpendMicros: 3_000_000},
		{Keys: []string{"pub-b"}, SpendMicros: 1_000_001},
		{Keys: []string{"pub-c"}, SpendMicros: 500_000},
	}
	clicks := []networkClick{
		{id: 1, cost: 2_000, target: "pub-a"},
		{id: 2, cost: 1_000, target: "UUID-A"},
		{id: 3, cost: 0, target: "pub-b"},
		{id: 4, cost: 0, target: "pub-b"},
		{id: 5, cost: 0, target: ""},
		{id: 6, cost: 0, target: "pub-unknown"},
	}
	p := planNetworkCost(clicks, targets)
	want := []int64{2_000_000, 1_000_000, 500_001, 500_000, 250_000, 250_000}
	for i, w := range want {
		if p.costs[i] != w || p.ids[i] != clicks[i].id {
			t.Fatalf("costs %v, want %v", p.costs, want)
		}
	}
	if p.spend != 4_500_001 || p.matched != 4_000_001 || p.spread != 500_000 || p.unattributed != 0 {
		t.Fatalf("plan %+v", p)
	}
	// Every click named a target, and C still has no click: C is
	// unattributed, and the totals still add up.
	p = planNetworkCost(clicks[:4], targets)
	if p.spread != 0 || p.unattributed != 500_000 || p.matched+p.spread+p.unattributed != p.spend {
		t.Fatalf("no loose clicks: %+v", p)
	}
	// No target data at all (the URL carries no {target}): everything is
	// spread over the day's clicks.
	noTargets := []networkClick{{id: 1, cost: 300}, {id: 2, cost: 100}}
	p = planNetworkCost(noTargets, targets)
	if p.costs[0] != 3_375_001 || p.costs[1] != 1_125_000 || p.spread != 4_500_001 {
		t.Fatalf("no targets on clicks: %+v", p)
	}
}
