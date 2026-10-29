package abtests

import (
	"context"
	"slices"
	"strings"
)

// Breakdowns: how each version did within groups of visitors, such as
// mobile and desktop. A test is judged on everyone; a breakdown helps to
// learn where a change works, and is easy to over-read, since every group
// is another chance of a lucky difference. So each group's comparisons are
// corrected for every version in every group shown.

// Dimensions are what a breakdown can group visitors by: their device, the
// source of the visit they first saw their version in (its utm_source, else
// the referring site, else direct), its country, and whether they had
// visited before (new or returning).
var Dimensions = []string{"device", "source", "country", "visitor"}

// MaxSegments is the most groups a breakdown shows; the smaller ones are
// added up as "other".
const MaxSegments = 8

// Segment is one group's arms and its versions against the control.
type Segment struct {
	Key         string       `json:"key"`
	Arms        []Arm        `json:"arms"`
	Comparisons []Comparison `json:"comparisons"`
}

// BreakdownResult is a test's first goal, group by group.
type BreakdownResult struct {
	Dimension string    `json:"dimension"`
	Goal      Goal      `json:"goal"`
	Metric    string    `json:"metric"`
	Segments  []Segment `json:"segments"`
	// CorrectedFor is how many comparisons the p-values and ranges are
	// corrected for: every version in every group.
	CorrectedFor int `json:"corrected_for"`
}

// Breakdown splits the test's first goal by dim: the largest MaxSegments
// groups by visitors, the rest as "other", each compared at its totals so
// far.
func Breakdown(ctx context.Context, q Querier, ws string, s Spec, dim string) (BreakdownResult, error) {
	out := BreakdownResult{Dimension: dim, Segments: []Segment{}}
	if len(s.Goals) == 0 || segmentSQL[dim] == "" {
		return out, nil
	}
	g := s.Goals[0]
	out.Goal, out.Metric = g, g.MetricOf()
	order, _ := controlFirst(s)
	by, err := tally(ctx, q, ws, s, g, order, dim)
	if err != nil {
		return out, err
	}
	total := func(arms []Arm) (n int64) {
		for _, a := range arms {
			n += a.Visitors
		}
		return n
	}
	for key, days := range by {
		if len(days) > 0 {
			out.Segments = append(out.Segments, Segment{Key: key, Arms: days[len(days)-1].Arms})
		}
	}
	slices.SortFunc(out.Segments, func(a, b Segment) int {
		if d := total(b.Arms) - total(a.Arms); d != 0 {
			if d > 0 {
				return 1
			}
			return -1
		}
		return strings.Compare(a.Key, b.Key)
	})
	if len(out.Segments) > MaxSegments {
		other := Segment{Key: "other", Arms: make([]Arm, len(order))}
		for i, v := range order {
			other.Arms[i].Variant = v
		}
		for _, sg := range out.Segments[MaxSegments-1:] {
			for i, a := range sg.Arms {
				other.Arms[i].Visitors += a.Visitors
				other.Arms[i].Conversions += a.Conversions
				other.Arms[i].Sum += a.Sum
				other.Arms[i].SumSq += a.SumSq
			}
		}
		for i := range other.Arms {
			if other.Arms[i].Visitors > 0 && out.Metric != MetricConversion {
				other.Arms[i].Mean = other.Arms[i].Sum / float64(other.Arms[i].Visitors)
			}
		}
		out.Segments = append(out.Segments[:MaxSegments-1], other)
	}
	out.CorrectedFor = max(1, len(order)-1) * max(1, len(out.Segments))
	for i := range out.Segments {
		out.Segments[i].Comparisons = compareSeries([][]Arm{out.Segments[i].Arms}, out.Metric, s.MinEffect, 0, out.CorrectedFor)
	}
	return out, nil
}
