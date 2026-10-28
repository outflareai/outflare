package abtests

import (
	"math"
)

// Arm is what one version of a test saw: visitors who were shown it, how
// many of them went on to reach the goal, and for a count or value goal
// the total of their values (each visitor's capped at the goal's cap).
type Arm struct {
	Variant     string `json:"variant"`
	Visitors    int64  `json:"visitors"`
	Conversions int64  `json:"conversions"`
	// Sum is the total of the visitors' values for a count or value goal;
	// Mean is Sum over Visitors. Both are 0 for a conversion goal.
	Sum  float64 `json:"sum,omitempty"`
	Mean float64 `json:"mean,omitempty"`
	// SumSq is the total of the squared values, for the variance.
	SumSq float64 `json:"-"`
}

// Rate is the share of the arm's visitors who converted.
func (a Arm) Rate() float64 {
	if a.Visitors == 0 {
		return 0
	}
	return float64(a.Conversions) / float64(a.Visitors)
}

// Moments is what the statistics need of one visitor's value: its mean
// and variance. For a conversion goal the value is 1 or 0, so the mean is
// the rate p and the variance p(1-p).
type Moments struct {
	Mean float64 `json:"mean"`
	Var  float64 `json:"var"`
}

// Moments is the arm's mean value per visitor and the variance of one
// visitor's value, for metric (MetricConversion, MetricCount or
// MetricValue).
func (a Arm) Moments(metric string) Moments {
	if a.Visitors == 0 {
		return Moments{}
	}
	if metric == "" || metric == MetricConversion {
		p := a.Rate()
		return Moments{p, p * (1 - p)}
	}
	n := float64(a.Visitors)
	m := a.Sum / n
	return Moments{m, math.Max(0, a.SumSq/n-m*m)}
}

// Comparison is one version against the control.
type Comparison struct {
	Variant string `json:"variant"`
	// Chance this version's true mean (the rate, for a conversion goal) is
	// above the control's: Bayesian with a flat Beta(1,1) prior for a
	// rate, and the normal approximation for a count or value.
	ChanceToBeat float64 `json:"chance_to_beat"`
	// Relative lift of the observed mean over the control's, and an
	// always-valid 95% range for it: the same sequential test as PValue,
	// inverted (at the corrected level when several versions compete), so
	// the range excludes 0 exactly when PValue is under 0.05.
	Lift     float64 `json:"lift"`
	LiftLow  float64 `json:"lift_low"`
	LiftHigh float64 `json:"lift_high"`
	// An always-valid p-value (mSPRT): it stays honest however often the
	// results are looked at, so people may check every day and stop when
	// it is low, unlike a classic p-value. It is the lowest over the
	// test's daily looks, so it never rises again. When several versions
	// compete with the control it is corrected for that (Bonferroni: the
	// raw p-value times the number of versions), and RawPValue is the
	// uncorrected one.
	PValue    float64 `json:"p_value"`
	RawPValue float64 `json:"raw_p_value"`
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

// Compare sets each arm against the first (the control) at one look, for
// a conversion goal. minEffect is the smallest relative change worth
// detecting (SequentialP); 0 means DefaultMinEffect.
func Compare(arms []Arm, minEffect float64) []Comparison {
	return CompareSeries([][]Arm{arms}, MetricConversion, minEffect, 0)
}

// CompareSeries sets each version against the control over a test's looks:
// the cumulative arms at the end of each day, control first, the last one
// being now. The chance to beat and the lift come from the last look; the
// p-value is the lowest over the looks and the range their intersection,
// as an always-valid test allows (Johari et al. 2017), so neither moves
// back once the evidence is in. metric says what an arm's numbers measure
// (MetricConversion, MetricCount or MetricValue).
//
// baseline is the control's expected mean (its rate, for a conversion
// goal), recorded when the test started, which fixes the mixing spread
// (minEffect × baseline) for the whole test. 0 means none was recorded:
// each look then uses its own control mean, which is only approximately
// valid at small counts.
//
// With more than one version against the control, each comparison is a
// separate chance of a false winner, so the p-values and ranges are
// corrected for their number (Bonferroni): together they keep to Alpha.
func CompareSeries(looks [][]Arm, metric string, minEffect, baseline float64) []Comparison {
	if len(looks) == 0 || len(looks[len(looks)-1]) < 2 {
		return nil
	}
	last := looks[len(looks)-1]
	c := last[0]
	versions := float64(len(last) - 1)
	alpha := Alpha / versions
	out := make([]Comparison, 0, len(last)-1)
	for i, a := range last[1:] {
		cmp := Comparison{Variant: a.Variant, ChanceToBeat: chanceToBeat(c, a, metric), RawPValue: 1}
		cm, am := c.Moments(metric), a.Moments(metric)
		if cm.Mean > 0 && am.Mean > 0 {
			cmp.Lift = am.Mean/cm.Mean - 1
		}
		lo, hi := math.Inf(-1), math.Inf(1)
		for _, look := range looks {
			if len(look) <= i+1 {
				continue
			}
			lc, la := look[0], look[i+1]
			lcm, lam := lc.Moments(metric), la.Moments(metric)
			p, h, ok := sequential(lcm, lc.Visitors, lam, la.Visitors, tau(minEffect, baseline, lcm.Mean), alpha)
			if !ok {
				continue
			}
			cmp.RawPValue = math.Min(cmp.RawPValue, p)
			d := lam.Mean - lcm.Mean
			if nl, nh := math.Max(lo, d-h), math.Min(hi, d+h); nl <= nh {
				lo, hi = nl, nh
			}
		}
		cmp.PValue = math.Min(1, cmp.RawPValue*versions)
		if cm.Mean > 0 && !math.IsInf(lo, 0) && !math.IsInf(hi, 0) {
			cmp.LiftLow, cmp.LiftHigh = lo/cm.Mean, hi/cm.Mean
		}
		out = append(out, cmp)
	}
	return out
}

// tau is the mixing spread of the sequential test: minEffect times the
// recorded baseline mean, or the look's own control mean without one.
func tau(minEffect, baseline, controlMean float64) float64 {
	if minEffect <= 0 {
		minEffect = DefaultMinEffect
	}
	if baseline <= 0 {
		baseline = controlMean
	}
	return math.Max(minEffect*baseline, 0.001)
}

// chanceToBeat is P(mean_b > mean_a): exact for a rate (ChanceToBeat),
// and from the normal approximation of the two means otherwise.
func chanceToBeat(a, b Arm, metric string) float64 {
	if metric == "" || metric == MetricConversion {
		return ChanceToBeat(a, b)
	}
	if a.Visitors < 2 || b.Visitors < 2 {
		return 0.5
	}
	am, bm := a.Moments(metric), b.Moments(metric)
	se := math.Sqrt(am.Var/float64(a.Visitors) + bm.Var/float64(b.Visitors))
	if se == 0 {
		switch {
		case bm.Mean > am.Mean:
			return 1
		case bm.Mean < am.Mean:
			return 0
		}
		return 0.5
	}
	return clamp01(normCDF((bm.Mean - am.Mean) / se))
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
	am, bm := a.Moments(MetricConversion), b.Moments(MetricConversion)
	p, _, _ := sequential(am, a.Visitors, bm, b.Visitors, tau(minEffect, 0, am.Mean), Alpha)
	return p
}

// sequential is the mSPRT at one look with mixing spread t, for the
// difference in two means from na and nb visitors: its p-value, and the
// half-width h of the always-valid range at level alpha for the
// difference, from inverting the same test (the differences it doesn't
// reject). ok is false when the look carries no evidence yet.
func sequential(a Moments, na int64, b Moments, nb int64, t, alpha float64) (p, h float64, ok bool) {
	if na < 2 || nb < 2 {
		return 1, 0, false
	}
	v := a.Var/float64(na) + b.Var/float64(nb)
	if v == 0 {
		return 1, 0, false
	}
	t2 := t * t
	d := b.Mean - a.Mean
	logL := 0.5*math.Log(v/(v+t2)) + t2*d*d/(2*v*(v+t2))
	h = math.Sqrt((math.Log(1/alpha) + 0.5*math.Log((v+t2)/v)) * 2 * v * (v + t2) / t2)
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
