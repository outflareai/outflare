package abtests

import (
	"context"
	"slices"
	"strings"
)

// Multivariate tests: two or three flags (factors) tested at once, each
// splitting visitors on its own, so every combination of their versions
// (a cell) gets visitors. A visitor counts when they saw a version of
// every factor during the test. Each factor's main effect pools over the
// others' versions; the best cell is compared with the one where every
// factor shows its control; and each other factor is checked for changing
// the first one's effect.

// Factor is one flag of a multivariate test beyond the test's own: its key
// and control version.
type Factor struct {
	FlagKey string `json:"flag_key"`
	Control string `json:"control"`
}

// MaxFactors is how many flags a multivariate test may add to its own.
const MaxFactors = 2

// Cell is one combination of versions, by flag key.
type Cell struct {
	Versions map[string]string `json:"versions"`
	Arm      Arm               `json:"arm"`
	// Against the cell where every factor shows its control; nil for that
	// cell. Corrected for every other cell.
	Comparison *Comparison `json:"comparison"`
}

// MainEffect is one factor's versions against its control, pooled over the
// other factors' versions.
type MainEffect struct {
	FlagKey     string       `json:"flag_key"`
	Arms        []Arm        `json:"arms"`
	Comparisons []Comparison `json:"comparisons"`
}

// FactorialResult is a multivariate test's first goal, cell by cell.
type FactorialResult struct {
	Goal        Goal         `json:"goal"`
	Metric      string       `json:"metric"`
	Factors     []string     `json:"factors"`
	Cells       []Cell       `json:"cells"`
	MainEffects []MainEffect `json:"main_effects"`
	// Best is the cell with the highest mean, if any cell beats the
	// all-controls one.
	Best *Cell `json:"best"`
	// Interactions say whether each other factor changes the first
	// factor's effect (Cochran's Q over the other factor's versions).
	Interactions []VersionInteraction `json:"interactions"`
	CorrectedFor int                  `json:"corrected_for"`
}

// Factorial measures s's first goal over every cell of s's flag and the
// factors.
func Factorial(ctx context.Context, q Querier, ws string, s Spec, factors []Factor) (FactorialResult, error) {
	out := FactorialResult{Cells: []Cell{}, MainEffects: []MainEffect{}, Interactions: []VersionInteraction{}}
	if len(s.Goals) == 0 || len(factors) == 0 {
		return out, nil
	}
	g := s.Goals[0]
	out.Goal, out.Metric = g, g.MetricOf()
	keys := []string{s.FlagKey}
	other := make([]string, len(factors))
	for i, f := range factors {
		other[i] = f.FlagKey
		keys = append(keys, f.FlagKey)
	}
	out.Factors = keys
	order, _ := controlFirst(s)
	by, err := tally(ctx, q, ws, s, g, order, "flag:"+strings.Join(other, ","))
	if err != nil {
		return out, err
	}
	// Cells: the test's own version by the factors' versions, leaving out
	// visitors who didn't see every factor.
	segs := make([]string, 0, len(by))
	for k, days := range by {
		if len(days) > 0 && !slices.Contains(strings.Split(k, "+"), "none") {
			segs = append(segs, k)
		}
	}
	slices.Sort(segs)
	controls := strings.Join(func() []string {
		c := make([]string, len(factors))
		for i, f := range factors {
			c[i] = f.Control
		}
		return c
	}(), "+")
	base := -1
	for _, sg := range segs {
		arms := by[sg][len(by[sg])-1].Arms
		vs := strings.Split(sg, "+")
		for i, a := range arms {
			c := Cell{Versions: map[string]string{s.FlagKey: order[i]}, Arm: a}
			for j, f := range factors {
				c.Versions[f.FlagKey] = vs[j]
			}
			if i == 0 && sg == controls {
				base = len(out.Cells)
			}
			out.Cells = append(out.Cells, c)
		}
	}
	out.CorrectedFor = max(1, len(out.Cells)-1)

	// Every cell against the all-controls one, corrected for every cell.
	if base >= 0 {
		baseArm := out.Cells[base].Arm
		bestMean := -1.0
		for i := range out.Cells {
			c := &out.Cells[i]
			if i == base {
				continue
			}
			if cmp := compareSeries([][]Arm{{baseArm, c.Arm}}, out.Metric, s.MinEffect, 0, out.CorrectedFor); len(cmp) == 1 {
				c.Comparison = &cmp[0]
			}
			if m := c.Arm.Moments(out.Metric).Mean; c.Comparison != nil && c.Comparison.ChanceToBeat > 0.5 && m > bestMean {
				bestMean = m
				best := *c
				out.Best = &best
			}
		}
	}

	// Main effects: each factor's versions, pooled over the rest.
	for fi, key := range keys {
		versions := order
		control := s.Control
		if fi > 0 {
			control = factors[fi-1].Control
			versions = []string{control}
			for _, c := range out.Cells {
				if v := c.Versions[key]; !slices.Contains(versions, v) {
					versions = append(versions, v)
				}
			}
		}
		arms := make([]Arm, len(versions))
		for i, v := range versions {
			arms[i].Variant = v
		}
		for _, c := range out.Cells {
			i := slices.Index(versions, c.Versions[key])
			if i < 0 {
				continue
			}
			addArm(&arms[i], c.Arm)
		}
		finishArms(arms, out.Metric)
		out.MainEffects = append(out.MainEffects, MainEffect{FlagKey: key, Arms: arms,
			Comparisons: compareSeries([][]Arm{arms}, out.Metric, s.MinEffect, 0, max(1, len(arms)-1)*len(keys))})
	}

	// Interactions: does each other factor change the test's own effect?
	for _, f := range factors {
		for _, v := range order[1:] {
			var ds, ws []float64
			var effects []SegmentEffect
			for _, ov := range uniqueVersions(out.Cells, f.FlagKey) {
				var c, a Arm
				for _, cell := range out.Cells {
					if cell.Versions[f.FlagKey] != ov {
						continue
					}
					switch cell.Versions[s.FlagKey] {
					case order[0]:
						addArm(&c, cell.Arm)
					case v:
						addArm(&a, cell.Arm)
					}
				}
				arms := []Arm{c, a}
				finishArms(arms, out.Metric)
				cm, am := arms[0].Moments(out.Metric), arms[1].Moments(out.Metric)
				effects = append(effects, SegmentEffect{OtherVariant: f.FlagKey + ":" + ov, Visitors: c.Visitors + a.Visitors, Difference: am.Mean - cm.Mean})
				if c.Visitors >= 2 && a.Visitors >= 2 {
					if va := cm.Var/float64(c.Visitors) + am.Var/float64(a.Visitors); va > 0 {
						ds, ws = append(ds, am.Mean-cm.Mean), append(ws, 1/va)
					}
				}
			}
			p := cochranQ(ds, ws)
			out.Interactions = append(out.Interactions, VersionInteraction{Variant: v + " × " + f.FlagKey, Effects: effects, PValue: p, Interacts: p < InteractionAlpha})
		}
	}
	return out, nil
}

func addArm(dst *Arm, a Arm) {
	dst.Visitors += a.Visitors
	dst.Conversions += a.Conversions
	dst.Sum += a.Sum
	dst.SumSq += a.SumSq
}

func finishArms(arms []Arm, metric string) {
	for i := range arms {
		if metric != MetricConversion && arms[i].Visitors > 0 {
			arms[i].Mean = arms[i].Sum / float64(arms[i].Visitors)
		}
	}
}

func uniqueVersions(cells []Cell, key string) []string {
	var out []string
	for _, c := range cells {
		if v := c.Versions[key]; !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}
