package routeweights

import (
	"math"
	"math/rand/v2"
	"testing"
)

func TestNotReady(t *testing.T) {
	for _, c := range []struct {
		name string
		arms []Arm
		want string
	}{
		{"one arm", []Arm{{Clicks: 500, Conversions: 20, Weight: 100}, {Clicks: 500, Conversions: 20, Weight: 0}}, TooFewArms},
		{"few clicks", []Arm{{Clicks: 500, Conversions: 20, Weight: 100}, {Clicks: 49, Conversions: 2, Weight: 100}}, NotEnoughClicks},
		{"few conversions", []Arm{{Clicks: 500, Conversions: 2, Weight: 100}, {Clicks: 500, Conversions: 2, Weight: 100}}, NotEnoughConversions},
	} {
		r := Suggest(c.arms, Conversions, 1)
		if r.Ready || r.Reason != c.want || r.Best != nil || r.Weights[0] != c.arms[0].Weight || r.Weights[1] != c.arms[1].Weight {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
}

func TestClearWinner(t *testing.T) {
	arms := []Arm{
		{Clicks: 1000, Conversions: 10, Weight: 50},
		{Clicks: 1000, Conversions: 60, Weight: 50},
		{Clicks: 1000, Conversions: 0, Weight: 0}, // stopped: stays at 0
	}
	r := Suggest(arms, Conversions, 7)
	if !r.Ready || r.Best[1] < 0.99 || r.Best[2] != 0 {
		t.Fatalf("%+v", r)
	}
	// The winner gets all but the loser's floor (5% of 100).
	if r.Weights[0] != 5 || r.Weights[1] != 95 || r.Weights[2] != 0 {
		t.Fatalf("weights %v", r.Weights)
	}
	// The same numbers give the same suggestion.
	if again := Suggest(arms, Conversions, 7); again.Weights[0] != r.Weights[0] || again.Best[1] != r.Best[1] {
		t.Fatalf("not repeatable: %+v %+v", r, again)
	}
}

func TestEvenArmsStayEven(t *testing.T) {
	arms := []Arm{{Clicks: 2000, Conversions: 40, Weight: 100}, {Clicks: 2000, Conversions: 40, Weight: 100}}
	r := Suggest(arms, Conversions, 3)
	if !r.Ready || math.Abs(r.Best[0]-0.5) > 0.03 || r.Weights[0]+r.Weights[1] != 200 || abs(r.Weights[0]-r.Weights[1]) > 12 {
		t.Fatalf("%+v", r)
	}
}

// Revenue weighs the value of a conversion: fewer conversions worth much
// more win on revenue and lose on conversions.
func TestRevenue(t *testing.T) {
	arms := []Arm{
		{Clicks: 1000, Conversions: 50, Revenue: 50 * 2, Weight: 100},
		{Clicks: 1000, Conversions: 30, Revenue: 30 * 20, Weight: 100},
	}
	if r := Suggest(arms, Conversions, 5); r.Best[0] < 0.95 {
		t.Fatalf("conversions: %+v", r)
	}
	if r := Suggest(arms, Revenue, 5); r.Best[1] < 0.95 || r.Weights[1] <= r.Weights[0] {
		t.Fatalf("revenue: %+v", r)
	}
	// Without any revenue, nothing is better than anything.
	free := []Arm{{Clicks: 1000, Conversions: 50, Weight: 100}, {Clicks: 1000, Conversions: 30, Weight: 100}}
	if r := Suggest(free, Revenue, 5); !r.Ready || r.Weights[0]+r.Weights[1] != 200 {
		t.Fatalf("no revenue: %+v", r)
	}
}

func TestApportion(t *testing.T) {
	for _, c := range []struct {
		shares []float64
		total  int
	}{
		{[]float64{0.3333, 0.3333, 0.3334}, 100},
		{[]float64{0.05, 0.95}, 7},
		{[]float64{0.5, 0.5, 0.0}, 2}, // fewer than one each: each still gets 1
		{[]float64{0.1, 0.2, 0.7}, 10000},
	} {
		out := apportion(c.shares, c.total)
		sum := 0
		for _, w := range out {
			if w < 1 {
				t.Errorf("%v: %v has a 0", c.shares, out)
			}
			sum += w
		}
		if sum != max(c.total, len(c.shares)) {
			t.Errorf("%v of %d: %v adds to %d", c.shares, c.total, out, sum)
		}
	}
}

func TestNoArms(t *testing.T) {
	if r := Suggest(nil, Conversions, 0); r.Ready || r.Reason != TooFewArms {
		t.Fatalf("%+v", r)
	}
}

// The samplers have the right means: Gamma(k) has k, Beta(a, b) a/(a+b).
func TestSamplers(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for _, k := range []float64{0.5, 1, 3, 40} {
		var sum float64
		for range 50_000 {
			sum += gamma(r, k)
		}
		if m := sum / 50_000; math.Abs(m-k)/k > 0.03 {
			t.Errorf("gamma(%v) mean %v", k, m)
		}
	}
	var sum float64
	for range 50_000 {
		sum += beta(r, 3, 97)
	}
	if m := sum / 50_000; math.Abs(m-0.03) > 0.002 {
		t.Errorf("beta(3, 97) mean %v", m)
	}
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
