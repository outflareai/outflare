package abtests

import (
	"context"
	"slices"
)

// Interactions: when two tests run at once on different flags, a visitor
// can be in both, and one change can alter the other's effect (a bolder
// button helps only with the new headline). Each version's effect is
// measured within each version of the other test, and a heterogeneity test
// says whether those effects differ more than chance allows.

// InteractionAlpha is how sure the check must be before it calls an
// interaction: stricter than a test's own 0.05, since it is run for every
// other test and every version.
const InteractionAlpha = 0.01

// VersionInteraction is one version's effect within each version of the
// other test.
type VersionInteraction struct {
	Variant string `json:"variant"`
	// Effects are the version's difference from the control (in the goal's
	// mean per visitor), by the other test's version.
	Effects []SegmentEffect `json:"effects"`
	// PValue is the chance of effects this different if they were really
	// the same (Cochran's Q); 1 when there is too little to tell.
	PValue    float64 `json:"p_value"`
	Interacts bool    `json:"interacts"`
}

// SegmentEffect is a version's difference from the control among the
// visitors who saw one version of the other test.
type SegmentEffect struct {
	OtherVariant string  `json:"other_variant"`
	Visitors     int64   `json:"visitors"`
	Difference   float64 `json:"difference"`
}

// InteractionResult is this test's first goal split by another flag's
// versions.
type InteractionResult struct {
	OtherFlag string `json:"other_flag"`
	// Overlap is how many of this test's visitors also saw the other flag.
	Overlap  int64                `json:"overlap"`
	Versions []VersionInteraction `json:"versions"`
}

// Interaction measures the test's first goal within each version of
// another flag the same visitors saw while it ran.
func Interaction(ctx context.Context, q Querier, ws string, s Spec, otherFlag string) (InteractionResult, error) {
	out := InteractionResult{OtherFlag: otherFlag, Versions: []VersionInteraction{}}
	if len(s.Goals) == 0 || !flagKeyRe.MatchString(otherFlag) {
		return out, nil
	}
	g := s.Goals[0]
	order, _ := controlFirst(s)
	by, err := tally(ctx, q, ws, s, g, order, "flag:"+otherFlag)
	if err != nil {
		return out, err
	}
	keys := make([]string, 0, len(by))
	for k := range by {
		if k != "none" && len(by[k]) > 0 {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	metric := g.MetricOf()
	for i, v := range order[1:] {
		vi := VersionInteraction{Variant: v, Effects: []SegmentEffect{}, PValue: 1}
		var ds, ws []float64
		for _, k := range keys {
			days := by[k]
			arms := days[len(days)-1].Arms
			c, a := arms[0], arms[i+1]
			if i == 0 {
				out.Overlap += c.Visitors
				for _, x := range arms[1:] {
					out.Overlap += x.Visitors
				}
			}
			cm, am := c.Moments(metric), a.Moments(metric)
			vi.Effects = append(vi.Effects, SegmentEffect{OtherVariant: k, Visitors: c.Visitors + a.Visitors, Difference: am.Mean - cm.Mean})
			if c.Visitors < 2 || a.Visitors < 2 {
				continue
			}
			v := cm.Var/float64(c.Visitors) + am.Var/float64(a.Visitors)
			if v <= 0 {
				continue
			}
			ds, ws = append(ds, am.Mean-cm.Mean), append(ws, 1/v)
		}
		vi.PValue = cochranQ(ds, ws)
		vi.Interacts = vi.PValue < InteractionAlpha
		out.Versions = append(out.Versions, vi)
	}
	return out, nil
}

// cochranQ is the p-value of Cochran's Q test that effects ds with
// weights ws (one over each one's variance) are all the same; 1 with
// fewer than two.
func cochranQ(ds, ws []float64) float64 {
	if len(ds) < 2 {
		return 1
	}
	var sw, swd float64
	for i, d := range ds {
		sw += ws[i]
		swd += ws[i] * d
	}
	mean := swd / sw
	q := 0.0
	for i, d := range ds {
		q += ws[i] * (d - mean) * (d - mean)
	}
	// Q follows chi-square with k-1 degrees of freedom when the effects
	// are the same.
	return clamp01(gammaQ(float64(len(ds)-1)/2, q/2))
}
