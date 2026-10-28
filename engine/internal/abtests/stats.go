package abtests

import (
	"math"
)

// Arm is what one version of a test saw: visitors who were shown it and
// how many of them went on to reach the goal.
type Arm struct {
	Variant     string `json:"variant"`
	Visitors    int64  `json:"visitors"`
	Conversions int64  `json:"conversions"`
}

// Rate is the share of the arm's visitors who converted.
func (a Arm) Rate() float64 {
	if a.Visitors == 0 {
		return 0
	}
	return float64(a.Conversions) / float64(a.Visitors)
}

// Comparison is one version against the control.
type Comparison struct {
	Variant string `json:"variant"`
	// Chance this version's true rate is above the control's (Bayesian,
	// with a flat Beta(1,1) prior on each).
	ChanceToBeat float64 `json:"chance_to_beat"`
	// Relative lift of the observed rate over the control's, and an
	// always-valid 95% range for it: the same sequential test as PValue,
	// inverted, so the range excludes 0 exactly when PValue is under 0.05.
	Lift     float64 `json:"lift"`
	LiftLow  float64 `json:"lift_low"`
	LiftHigh float64 `json:"lift_high"`
	// An always-valid p-value (mSPRT): it stays honest however often the
	// results are looked at, so people may check every day and stop when
	// it is low, unlike a classic p-value. It is the lowest over the
	// test's daily looks, so it never rises again.
	PValue float64 `json:"p_value"`
}

// Alpha is the false-winner rate the always-valid test and range keep to.
const Alpha = 0.05

// DefaultMinEffect is the smallest relative change a test looks for when
// it doesn't say: +25% of the control's rate. MinEffectLow and
// MinEffectHigh bound what a test may choose.
const (
	DefaultMinEffect = 0.25
	MinEffectLow     = 0.01
	MinEffectHigh    = 1.0
)

// Compare sets each arm against the first (the control) at one look.
// minEffect is the smallest relative change worth detecting (SequentialP);
// 0 means DefaultMinEffect.
func Compare(arms []Arm, minEffect float64) []Comparison {
	return CompareSeries([][]Arm{arms}, minEffect, 0)
}

// CompareSeries sets each version against the control over a test's looks:
// the cumulative arms at the end of each day, control first, the last one
// being now. The chance to beat and the lift come from the last look; the
// p-value is the lowest over the looks and the range their intersection,
// as an always-valid test allows (Johari et al. 2017), so neither moves
// back once the evidence is in.
//
// baseline is the control's expected rate, recorded when the test started,
// which fixes the mixing spread (minEffect × baseline) for the whole test.
// 0 means none was recorded: each look then uses its own control rate,
// which is only approximately valid at small counts.
func CompareSeries(looks [][]Arm, minEffect, baseline float64) []Comparison {
	if len(looks) == 0 || len(looks[len(looks)-1]) < 2 {
		return nil
	}
	last := looks[len(looks)-1]
	c := last[0]
	out := make([]Comparison, 0, len(last)-1)
	for i, a := range last[1:] {
		cmp := Comparison{Variant: a.Variant, ChanceToBeat: ChanceToBeat(c, a), PValue: 1}
		cmp.Lift, _, _ = Lift(c, a)
		lo, hi := math.Inf(-1), math.Inf(1)
		for _, look := range looks {
			if len(look) <= i+1 {
				continue
			}
			p, h, ok := sequential(look[0], look[i+1], tau(minEffect, baseline, look[0]))
			if !ok {
				continue
			}
			cmp.PValue = math.Min(cmp.PValue, p)
			d := look[i+1].Rate() - look[0].Rate()
			if nl, nh := math.Max(lo, d-h), math.Min(hi, d+h); nl <= nh {
				lo, hi = nl, nh
			}
		}
		if pa := c.Rate(); pa > 0 && !math.IsInf(lo, 0) && !math.IsInf(hi, 0) {
			cmp.LiftLow, cmp.LiftHigh = lo/pa, hi/pa
		}
		out = append(out, cmp)
	}
	return out
}

// tau is the mixing spread of the sequential test: minEffect times the
// recorded baseline, or the look's own control rate without one.
func tau(minEffect, baseline float64, control Arm) float64 {
	if minEffect <= 0 {
		minEffect = DefaultMinEffect
	}
	if baseline <= 0 {
		baseline = control.Rate()
	}
	return math.Max(minEffect*baseline, 0.001)
}

// ChanceToBeat is P(rate_b > rate_a) with Beta(1+conversions,
// 1+non-conversions) posteriors, computed exactly (Evan Miller's sum),
// so it needs no sampling and gives the same answer every time.
func ChanceToBeat(a, b Arm) float64 {
	if a.Visitors == 0 || b.Visitors == 0 {
		return 0.5
	}
	alphaA, betaA := float64(a.Conversions+1), float64(a.Visitors-a.Conversions+1)
	alphaB, betaB := float64(b.Conversions+1), float64(b.Visitors-b.Conversions+1)
	// The sum has alphaB terms; past a few hundred thousand, the normal
	// approximation of the two posteriors is just as accurate and faster.
	if alphaB > 200_000 {
		return normalBeat(alphaA, betaA, alphaB, betaB)
	}
	total := 0.0
	base := lbeta(alphaA, betaA)
	for i := 0.0; i < alphaB; i++ {
		total += math.Exp(lbeta(alphaA+i, betaA+betaB) - math.Log(betaB+i) - lbeta(1+i, betaB) - base)
	}
	return clamp01(total)
}

func normalBeat(alphaA, betaA, alphaB, betaB float64) float64 {
	m := func(a, b float64) (float64, float64) {
		n := a + b
		return a / n, a * b / (n * n * (n + 1))
	}
	ma, va := m(alphaA, betaA)
	mb, vb := m(alphaB, betaB)
	return clamp01(normCDF((mb - ma) / math.Sqrt(va+vb)))
}

func lbeta(a, b float64) float64 {
	la, _ := math.Lgamma(a)
	lb, _ := math.Lgamma(b)
	lab, _ := math.Lgamma(a + b)
	return la + lb - lab
}

// Lift is (rate_b - rate_a) / rate_a with a 95% range from the delta
// method on the log of the ratio. With no control conversions there is no
// ratio, and all three are 0.
func Lift(a, b Arm) (lift, low, high float64) {
	pa, pb := a.Rate(), b.Rate()
	if pa == 0 || pb == 0 || a.Visitors == 0 || b.Visitors == 0 {
		return 0, 0, 0
	}
	lift = pb/pa - 1
	se := math.Sqrt((1-pa)/(pa*float64(a.Visitors)) + (1-pb)/(pb*float64(b.Visitors)))
	r := math.Log(pb / pa)
	return lift, math.Exp(r-1.96*se) - 1, math.Exp(r+1.96*se) - 1
}

// SequentialP is the always-valid p-value of the mixture sequential
// probability ratio test (Johari, Pekelis and Walsh, "Always valid
// inference", 2017) for a difference in two rates, with a normal mixing
// distribution whose spread is minEffect times the control's rate: the
// test is most sensitive to changes of about that relative size (0 means
// DefaultMinEffect). This is one look; CompareSeries keeps the lowest
// over a test's daily looks.
func SequentialP(a, b Arm, minEffect float64) float64 {
	p, _, _ := sequential(a, b, tau(minEffect, 0, a))
	return p
}

// sequential is the mSPRT at one look with mixing spread t: its p-value,
// and the half-width h of the always-valid 95% range for the difference in
// rates, from inverting the same test (the differences it doesn't reject).
// ok is false when the look carries no evidence yet.
func sequential(a, b Arm, t float64) (p, h float64, ok bool) {
	if a.Visitors < 2 || b.Visitors < 2 {
		return 1, 0, false
	}
	pa, pb := a.Rate(), b.Rate()
	v := pa*(1-pa)/float64(a.Visitors) + pb*(1-pb)/float64(b.Visitors)
	if v == 0 {
		return 1, 0, false
	}
	t2 := t * t
	d := pb - pa
	logL := 0.5*math.Log(v/(v+t2)) + t2*d*d/(2*v*(v+t2))
	h = math.Sqrt((math.Log(1/Alpha) + 0.5*math.Log((v+t2)/v)) * 2 * v * (v + t2) / t2)
	return clamp01(math.Exp(-logL)), h, true
}

// SampleRatioP is the chance of a split at least this uneven if visitors
// really were assigned by the weights (a chi-square test). Below 0.001,
// something is losing or double-counting visitors (a redirect, a cache, a
// bot), and the results shouldn't be trusted.
func SampleRatioP(arms []Arm, weights []int) float64 {
	if len(arms) != len(weights) || len(arms) < 2 {
		return 1
	}
	var n, w float64
	for i, a := range arms {
		n += float64(a.Visitors)
		w += float64(weights[i])
	}
	if n == 0 || w == 0 {
		return 1
	}
	chi, df := 0.0, -1.0
	for i, a := range arms {
		if weights[i] == 0 {
			continue
		}
		e := n * float64(weights[i]) / w
		d := float64(a.Visitors) - e
		chi += d * d / e
		df++
	}
	if df < 1 {
		return 1
	}
	return clamp01(gammaQ(df/2, chi/2))
}

// gammaQ is the regularised upper incomplete gamma function Q(a, x)
// (Numerical Recipes 6.2): a series below a+1, a continued fraction above.
func gammaQ(a, x float64) float64 {
	if x <= 0 {
		return 1
	}
	lg, _ := math.Lgamma(a)
	if x < a+1 {
		sum, del, ap := 1/a, 1/a, a
		for range 500 {
			ap++
			del *= x / ap
			sum += del
			if math.Abs(del) < math.Abs(sum)*1e-14 {
				break
			}
		}
		return 1 - sum*math.Exp(-x+a*math.Log(x)-lg)
	}
	const tiny = 1e-300
	b := x + 1 - a
	c, d := 1/tiny, 1/b
	h := d
	for i := 1.0; i < 500; i++ {
		an := -i * (i - a)
		b += 2
		d = an*d + b
		if math.Abs(d) < tiny {
			d = tiny
		}
		c = b + an/c
		if math.Abs(c) < tiny {
			c = tiny
		}
		d = 1 / d
		del := d * c
		h *= del
		if math.Abs(del-1) < 1e-14 {
			break
		}
	}
	return math.Exp(-x+a*math.Log(x)-lg) * h
}

func normCDF(z float64) float64 { return 0.5 * math.Erfc(-z/math.Sqrt2) }

func clamp01(x float64) float64 {
	if math.IsNaN(x) {
		return 0.5
	}
	return math.Min(1, math.Max(0, x))
}
