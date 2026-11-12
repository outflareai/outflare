// Package routeweights suggests the weights of a tracker list (the landers, or
// the offers and pages, that share a path's clicks) from how each one did
// (plans/ad-tracker.md, T9). For each one it works out the chance that it
// is the best on the chosen metric, from a posterior on its numbers, and
// gives the clicks out in that proportion, keeping a small share for each
// so the others go on being tested. Nothing here changes a weight: a
// person applies the suggestion (Cloud's auto mode does it through the
// same weights API).
//
// The numbers it is given leave out likely bots, suspect clicks and
// conversions (B7a) and declined conversions, so it never learns from them.
package routeweights

import (
	"cmp"
	"math"
	"math/rand/v2"
	"slices"
)

// The metrics a list can be weighed on.
const (
	// Conversions weighs on the conversion rate: conversions per click.
	Conversions = "conversions"
	// Revenue weighs on revenue per click: the conversion rate times the
	// revenue of a conversion.
	Revenue = "revenue"
)

// Metrics lists them, the default first.
var Metrics = []string{Revenue, Conversions}

const (
	// MinClicks is how many clicks each one needs before the list gets a
	// suggestion; MinConversions how many conversions the whole list needs.
	MinClicks      = 50
	MinConversions = 5
	// FloorShare is the share each one keeps at least, so a worse one is
	// still tested (at most half the clicks go to the floors).
	FloorShare = 0.05
	// draws is how many samples estimate each chance to be best.
	draws = 20_000
)

// Why a list has no suggestion yet.
const (
	TooFewArms           = "too_few_arms"
	NotEnoughClicks      = "not_enough_clicks"
	NotEnoughConversions = "not_enough_conversions"
)

// Arm is one of a list's landers, offers or pages.
type Arm struct {
	Clicks, Conversions int64
	// Revenue is its conversions' revenue, in one currency.
	Revenue float64
	// Weight is its weight now; one at 0 is stopped and stays so.
	Weight int
}

// Result is the suggestion for a list.
type Result struct {
	// Ready says there is enough data to suggest; otherwise Reason says
	// what's missing, Weights are the weights now and Best is nil.
	Ready  bool
	Reason string
	// Weights are the suggested weights, in the arms' order, adding up to
	// what the arms that take clicks add up to now.
	Weights []int
	// Best is each arm's chance to be the best, 0 for a stopped one.
	Best []float64
}

// Suggest suggests weights for arms on metric (Conversions or Revenue).
// seed fixes the sampling, so the same numbers give the same suggestion.
func Suggest(arms []Arm, metric string, seed uint64) Result {
	current := make([]int, len(arms))
	var live []int
	var conversions int64
	for i, a := range arms {
		current[i] = a.Weight
		if a.Weight > 0 {
			live = append(live, i)
			conversions += a.Conversions
		}
	}
	res := Result{Weights: current}
	switch {
	case len(live) < 2:
		res.Reason = TooFewArms
		return res
	case slices.ContainsFunc(live, func(i int) bool { return arms[i].Clicks < MinClicks }):
		res.Reason = NotEnoughClicks
		return res
	case conversions < MinConversions:
		res.Reason = NotEnoughConversions
		return res
	}

	// What a conversion is worth where an arm has few of its own: the
	// list's average, so an arm isn't judged on one or two payouts.
	var revenue float64
	for _, i := range live {
		revenue += arms[i].Revenue
	}
	pooled := revenue / float64(conversions)

	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	wins := make([]int, len(live))
	for range draws {
		best, bestAt := math.Inf(-1), 0
		for j, i := range live {
			a := arms[i]
			v := beta(r, float64(a.Conversions)+1, float64(a.Clicks-a.Conversions)+1)
			if metric == Revenue {
				v *= value(r, a, pooled)
			}
			if v > best {
				best, bestAt = v, j
			}
		}
		wins[bestAt]++
	}

	res.Ready, res.Best = true, make([]float64, len(arms))
	total := 0
	for _, i := range live {
		total += arms[i].Weight
	}
	floor := min(FloorShare, 0.5/float64(len(live)))
	shares := make([]float64, len(live))
	for j, i := range live {
		p := float64(wins[j]) / draws
		res.Best[i] = p
		shares[j] = floor + (1-floor*float64(len(live)))*p
	}
	res.Weights = slices.Clone(current)
	for j, w := range apportion(shares, total) {
		res.Weights[live[j]] = w
	}
	return res
}

// value samples what one of the arm's conversions is worth: a gamma
// around its average, shrunk toward the list's by one conversion's worth,
// and tighter the more conversions it has.
func value(r *rand.Rand, a Arm, pooled float64) float64 {
	if pooled <= 0 {
		return 0
	}
	k := float64(a.Conversions) + 1
	mean := (a.Revenue + pooled) / k
	return gamma(r, k) * mean / k
}

// apportion splits total in proportion to shares, in whole numbers that
// add up to it (largest remainders), each at least 1.
func apportion(shares []float64, total int) []int {
	n := len(shares)
	total = max(total, n)
	var sum float64
	for _, s := range shares {
		sum += s
	}
	out := make([]int, n)
	rest := make([]float64, n)
	used := 0
	for i, s := range shares {
		exact := s / sum * float64(total)
		out[i] = int(exact)
		rest[i] = exact - float64(out[i])
		used += out[i]
	}
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(rest[b], rest[a]) })
	for k := 0; used < total; k++ {
		out[order[k%n]]++
		used++
	}
	// A share too small for a whole click weight takes 1 from the largest.
	for i := range out {
		if out[i] == 0 {
			out[slices.Index(out, slices.Max(out))]--
			out[i] = 1
		}
	}
	return out
}

// beta samples a Beta(a, b) variable.
func beta(r *rand.Rand, a, b float64) float64 {
	x := gamma(r, a)
	return x / (x + gamma(r, b))
}

// gamma samples a Gamma(k, 1) variable (Marsaglia and Tsang, 2000).
func gamma(r *rand.Rand, k float64) float64 {
	if k < 1 {
		return gamma(r, k+1) * math.Pow(r.Float64(), 1/k)
	}
	d := k - 1.0/3
	c := 1 / math.Sqrt(9*d)
	for {
		x := r.NormFloat64()
		v := 1 + c*x
		if v <= 0 {
			continue
		}
		v = v * v * v
		u := r.Float64()
		if u < 1-0.0331*x*x*x*x || math.Log(u) < 0.5*x*x+d*(1-v+math.Log(v)) {
			return d * v
		}
	}
}
