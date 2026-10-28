package abtests

import (
	"math"
	"math/rand/v2"
	"testing"
)

func near(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.5f, want %.5f ± %g", name, got, want, tol)
	}
}

func TestChanceToBeat(t *testing.T) {
	a := Arm{Visitors: 1000, Conversions: 100}
	near(t, "equal arms", ChanceToBeat(a, a), 0.5, 0.01)
	b := Arm{Visitors: 1000, Conversions: 120}
	// The normal approximation gives about 0.924 here.
	near(t, "120 vs 100", ChanceToBeat(a, b), 0.924, 0.01)
	near(t, "symmetry", ChanceToBeat(a, b)+ChanceToBeat(b, a), 1, 0.001)
	near(t, "no visitors", ChanceToBeat(Arm{}, b), 0.5, 0)
	// The exact sum and the large-count approximation agree.
	exact := ChanceToBeat(Arm{Visitors: 400_000, Conversions: 40_000}, Arm{Visitors: 400_000, Conversions: 40_400})
	approx := normalBeat(40_001, 360_001, 40_401, 359_601)
	near(t, "exact vs normal", exact, approx, 0.005)
	big := ChanceToBeat(Arm{Visitors: 3_000_000, Conversions: 300_000}, Arm{Visitors: 3_000_000, Conversions: 301_000})
	if big <= 0.5 || big >= 1 {
		t.Errorf("large counts = %v", big)
	}
}

func TestLift(t *testing.T) {
	l, lo, hi := Lift(Arm{Visitors: 1000, Conversions: 100}, Arm{Visitors: 1000, Conversions: 120})
	near(t, "lift", l, 0.2, 1e-9)
	if !(lo < l && l < hi) || lo > 0 {
		t.Errorf("range = %v..%v around %v; 120 vs 100 of 1000 isn't certain", lo, hi, l)
	}
	if l, lo, hi := Lift(Arm{Visitors: 10}, Arm{Visitors: 10, Conversions: 1}); l != 0 || lo != 0 || hi != 0 {
		t.Error("no control conversions gives no lift")
	}
}

func TestSequentialP(t *testing.T) {
	a := Arm{Visitors: 1000, Conversions: 100}
	if p := SequentialP(a, a, 0); p != 1 {
		t.Errorf("no difference p = %v", p)
	}
	small := SequentialP(a, Arm{Visitors: 1000, Conversions: 120}, 0)
	if small < 0.05 {
		t.Errorf("120 vs 100 of 1000 is not proof yet; p = %v", small)
	}
	large := SequentialP(Arm{Visitors: 50_000, Conversions: 5_000}, Arm{Visitors: 50_000, Conversions: 6_000}, 0)
	if large > 0.001 {
		t.Errorf("a 20%% lift on 50,000 each is clear; p = %v", large)
	}
	// It is stricter than a classic test at the same data.
	classic := 2 * (1 - normCDF(0.02/math.Sqrt(0.1*0.9/1000+0.12*0.88/1000)))
	if small <= classic {
		t.Errorf("always-valid p %v should be above the classic %v", small, classic)
	}
}

func TestSampleRatioP(t *testing.T) {
	near(t, "even", SampleRatioP([]Arm{{Visitors: 5000}, {Visitors: 5000}}, []int{50, 50}), 1, 1e-9)
	// chi-square 3.841 with one degree of freedom is p = 0.05.
	n := 10_000.0
	d := math.Sqrt(3.841 * n / 4)
	p := SampleRatioP([]Arm{{Visitors: int64(n/2 + d)}, {Visitors: int64(n/2 - d)}}, []int{50, 50})
	near(t, "chi 3.84", p, 0.05, 0.003)
	if p := SampleRatioP([]Arm{{Visitors: 5300}, {Visitors: 4700}}, []int{50, 50}); p > 0.001 {
		t.Errorf("5300/4700 on a 50/50 split is off; p = %v", p)
	}
	near(t, "weighted", SampleRatioP([]Arm{{Visitors: 7000}, {Visitors: 3000}}, []int{70, 30}), 1, 1e-9)
	// Three arms: two degrees of freedom, chi 5.991 is p = 0.05.
	near(t, "gammaQ df2", gammaQ(1, 5.991/2), 0.05, 0.001)
	// For whole a, Q(a, x) = e^-x * sum of x^k/k! for k < a.
	want, term := 0.0, 1.0
	for k := range 10 {
		if k > 0 {
			term *= 40 / float64(k)
		}
		want += term
	}
	want *= math.Exp(-40)
	near(t, "gammaQ large", gammaQ(10, 40)/want, 1, 1e-6)
}

func TestCompare(t *testing.T) {
	out := Compare([]Arm{{Variant: "control", Visitors: 1000, Conversions: 100}, {Variant: "b", Visitors: 1000, Conversions: 120}, {Variant: "c", Visitors: 1000, Conversions: 80}}, 0)
	if len(out) != 2 || out[0].Variant != "b" || out[1].Variant != "c" {
		t.Fatalf("compare = %+v", out)
	}
	if out[0].ChanceToBeat < 0.9 || out[1].ChanceToBeat > 0.1 {
		t.Errorf("chances = %v, %v", out[0].ChanceToBeat, out[1].ChanceToBeat)
	}
}

// A test tuned for bigger changes stops sooner on a big one and later on a
// small one.
func TestSequentialPMinEffect(t *testing.T) {
	ctl := Arm{Visitors: 3000, Conversions: 120}
	big := Arm{Visitors: 3000, Conversions: 180}
	if p10, p25 := SequentialP(ctl, big, 0.1), SequentialP(ctl, big, 0); p25 >= p10 {
		t.Errorf("+50%%: tuned for +25%% p = %v, should be under +10%%'s %v", p25, p10)
	}
	small := Arm{Visitors: 50_000, Conversions: 5_500}
	ctl = Arm{Visitors: 50_000, Conversions: 5_000}
	if p10, p50 := SequentialP(ctl, small, 0.1), SequentialP(ctl, small, 0.5); p50 <= p10 {
		t.Errorf("+10%%: tuned for +50%% p = %v, should be over +10%%'s %v", p50, p10)
	}
}

// The range and the p-value come from the same test, so the range leaves
// out zero exactly when the result is safe to stop, at every look.
func TestRangeMatchesPValue(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	for range 300 {
		var looks [][]Arm
		ctl, b := Arm{Variant: "control"}, Arm{Variant: "b"}
		rate := 0.02 + rng.Float64()*0.08
		lift := (rng.Float64() - 0.3) * 0.6
		for range 1 + rng.IntN(20) {
			n := int64(50 + rng.IntN(400))
			ctl.Visitors, b.Visitors = ctl.Visitors+n, b.Visitors+n
			ctl.Conversions += binomial(rng, n, rate)
			b.Conversions += binomial(rng, n, rate*(1+lift))
			looks = append(looks, []Arm{ctl, b})
		}
		c := CompareSeries(looks, 0.25, rate)[0]
		if ctl.Conversions == 0 || c.LiftLow == 0 && c.LiftHigh == 0 {
			continue
		}
		excludes := c.LiftLow > 0 || c.LiftHigh < 0
		if excludes != (c.PValue < Alpha) {
			t.Fatalf("range %.3f..%.3f but p = %.4f (%+v)", c.LiftLow, c.LiftHigh, c.PValue, looks[len(looks)-1])
		}
	}
}

// The p-value is the lowest over the looks: it never rises again.
func TestPValueNeverRises(t *testing.T) {
	looks := [][]Arm{
		{{Visitors: 2000, Conversions: 80}, {Visitors: 2000, Conversions: 150}},
		{{Visitors: 4000, Conversions: 200}, {Visitors: 4000, Conversions: 215}},
	}
	first := CompareSeries(looks[:1], 0.25, 0.04)[0].PValue
	both := CompareSeries(looks, 0.25, 0.04)[0].PValue
	if both != first || first >= Alpha {
		t.Fatalf("first look p = %v, after the second = %v", first, both)
	}
}

// A/A simulation: both versions are the same, people look every day and
// stop at the first winner. The full rule (chance to beat and the
// always-valid p, either direction) must call a false winner at most about
// 5% of the time, at the small counts a marketing site sees.
func TestAAFalseWinners(t *testing.T) {
	if testing.Short() {
		t.Skip("simulation")
	}
	rng := rand.New(rand.NewPCG(2026, 10))
	// live: no baseline recorded, so each look tunes with its own control
	// rate (tests started before baselines were kept).
	for _, tc := range []struct {
		rate  float64
		daily int64
		live  bool
	}{{0.01, 200, false}, {0.03, 200, false}, {0.05, 1000, false}, {0.02, 5000, false}, {0.01, 200, true}, {0.03, 200, true}} {
		const sims, days = 200, 35
		base := tc.rate
		if tc.live {
			base = 0
		}
		wins := 0
		for range sims {
			ctl, b := Arm{Variant: "control"}, Arm{Variant: "b"}
			var looks [][]Arm
			for range days {
				ctl.Visitors, b.Visitors = ctl.Visitors+tc.daily, b.Visitors+tc.daily
				ctl.Conversions += binomial(rng, tc.daily, tc.rate)
				b.Conversions += binomial(rng, tc.daily, tc.rate)
				looks = append(looks, []Arm{ctl, b})
				c := CompareSeries(looks, 0.25, base)[0]
				if c.PValue < Alpha && (c.ChanceToBeat >= 0.95 || c.ChanceToBeat <= 0.05) {
					wins++
					break
				}
			}
		}
		if got := float64(wins) / sims; got > 0.07 {
			t.Errorf("rate %.2f, %d a day, live %v: false winners %.1f%%, want about 5%% or less", tc.rate, tc.daily, tc.live, got*100)
		} else {
			t.Logf("rate %.2f, %d a day, live %v: false winners %.1f%%", tc.rate, tc.daily, tc.live, got*100)
		}
	}
}

func binomial(rng *rand.Rand, n int64, p float64) int64 {
	var k int64
	for range n {
		if rng.Float64() < p {
			k++
		}
	}
	return k
}
