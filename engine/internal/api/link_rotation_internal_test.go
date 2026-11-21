package api

import (
	"math"
	"strconv"
	"testing"
)

// A list with landers and offers is two stages, each split by its own
// weights: 25 landers and 31 offers at the same weight get 1/25 and 1/31 of
// the clicks each, and every lander leads to every offer.

func rotationList(landers, offers int) []linkDestination {
	var list []linkDestination
	for i := range landers {
		list = append(list, linkDestination{id: "l" + strconv.Itoa(i), landerID: "lander" + strconv.Itoa(i), url: "https://northpeak.example/l/" + strconv.Itoa(i), weight: 100})
	}
	for i := range offers {
		list = append(list, linkDestination{id: "o" + strconv.Itoa(i), url: "https://northpeak.example/o/" + strconv.Itoa(i), weight: 100})
	}
	return list
}

// evenly fails unless every one of want got its share of n picks, within
// six standard deviations: wide enough that chance never fails it, narrow
// enough that a stage mixed with the other, or one left out, does.
func evenly(t *testing.T, what string, got map[string]int, want []linkDestination, n int) {
	t.Helper()
	total := 0
	for _, d := range want {
		total += d.weight
	}
	if len(got) > len(want) {
		t.Errorf("%s: %d got clicks, want at most %d", what, len(got), len(want))
	}
	for _, d := range want {
		p := float64(d.weight) / float64(total)
		share, band := float64(got[d.id])/float64(n), 6*math.Sqrt(p*(1-p)/float64(n))
		if math.Abs(share-p) > band {
			t.Errorf("%s: %s got %.4f of the clicks, want %.4f within %.4f", what, d.id, share, p, band)
		}
	}
}

func TestRotationSplitsEachStageOnItsOwn(t *testing.T) {
	s := &Server{}
	const day = "2026-01-05"
	list := rotationList(25, 31)
	first, next := entry(list), onward(list)
	if len(first) != 25 || len(next) != 31 {
		t.Fatalf("stages: %d landers, %d offers", len(first), len(next))
	}

	const n = 400_000
	landers, offers, pairs := map[string]int{}, map[string]int{}, map[string]int{}
	for range n {
		l := s.route(first, day, false)
		// The offers a lander's call to action picks from are the list's,
		// whichever lander the click went to.
		o := s.route(next, day, false)
		if l == nil || o == nil || l.landerID == "" || o.landerID != "" {
			t.Fatalf("a click went to %v then %v", l, o)
		}
		landers[l.id]++
		offers[o.id]++
		pairs[l.id+">"+o.id]++
	}
	evenly(t, "landers", landers, first, n)
	evenly(t, "offers", offers, next, n)
	// Each of the 775 pairs expects about 516 clicks: one with none means
	// the stages aren't picked apart.
	if len(pairs) != 25*31 {
		t.Errorf("%d lander and offer pairs were reached, want %d", len(pairs), 25*31)
	}
}

func TestRotationWeightsCapsAndStops(t *testing.T) {
	s := &Server{}
	const day, n = "2026-01-05", 200_000
	pick := func(list []linkDestination, bot bool) map[string]int {
		got := map[string]int{}
		for range n {
			d := s.route(list, day, bot)
			if d == nil {
				t.Fatal("no destination was open")
			}
			got[d.id]++
		}
		return got
	}

	// A heavier lander gets its weight's share of the landers alone.
	list := rotationList(25, 31)
	list[0].weight = 200
	evenly(t, "one lander at twice the weight", pick(entry(list), false), entry(list), n)

	// A stopped one (weight 0, or a cap of 0) gets none, and a capped one
	// its cap; the others share the rest.
	list = rotationList(25, 31)
	zero, ten := 0, 10
	list[1].weight, list[2].cap, list[3].cap = 0, &zero, &ten
	s.caps.seed("l2", day, 0)
	s.caps.seed("l3", day, 0)
	got := pick(entry(list), false)
	if got["l1"] != 0 || got["l2"] != 0 || got["l3"] != 10 {
		t.Fatalf("stopped and capped landers got %d, %d and %d clicks, want 0, 0 and 10", got["l1"], got["l2"], got["l3"])
	}
	delete(got, "l3")
	var open []linkDestination
	for _, d := range entry(list) {
		if d.id != "l1" && d.id != "l2" && d.id != "l3" {
			open = append(open, d)
		}
	}
	evenly(t, "the 22 open landers", got, open, n-10)

	// A likely bot goes where a person would, against no cap.
	bots := rotationList(2, 0)
	one := 1
	bots[0].cap, bots[1].cap = &one, &one
	if got := pick(bots, true); got["l0"] == 0 || got["l1"] == 0 {
		t.Fatalf("bots went to %v", got)
	}

	// Every lander closed: no destination, so the click goes to the next
	// list or the link's own page, never straight to an offer.
	closed := rotationList(3, 31)
	for i := range 3 {
		closed[i].weight = 0
	}
	if d := s.route(entry(closed), day, false); d != nil {
		t.Fatalf("a click went to %s with every lander stopped", d.id)
	}
	// Landers with nothing after them: a call to action has nowhere to go.
	if d := s.route(onward(rotationList(3, 0)), day, false); d != nil {
		t.Fatalf("a lander click went to %s with no offers", d.id)
	}
}

// A pick at a list's limits, half of it capped.
func BenchmarkRouteFullList(b *testing.B) {
	s := &Server{}
	const day = "2026-01-05"
	list := rotationList(maxListLanders, maxListOnward)
	big := 1 << 40
	for i := range list {
		if i%2 == 0 {
			list[i].cap = &big
		}
	}
	first := entry(list)
	b.ResetTimer()
	for range b.N {
		if s.route(first, day, true) == nil {
			b.Fatal("no destination")
		}
	}
}
