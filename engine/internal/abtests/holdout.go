package abtests

import (
	"context"
	"time"

	"github.com/outflareai/outflare/engine/internal/flags"
)

// Holdouts: an exclusion group can keep a share of its visitors out of
// every flag in it, so they see the default experience wherever the
// group's tests run. Comparing them with everyone else shows what the
// group's changes did together, which single tests can't: effects that add
// up, fade, or cancel out.

// HoldoutResult compares a group's holdout (the control) with the rest of
// its visitors on one goal.
type HoldoutResult struct {
	Group   string `json:"group"`
	Holdout int    `json:"holdout"`
	Goal    Goal   `json:"goal"`
	Metric  string `json:"metric"`
	// Arms are the holdout first, then everyone else.
	Arms       []Arm       `json:"arms"`
	Comparison *Comparison `json:"comparison"`
	From       time.Time   `json:"from"`
	To         time.Time   `json:"to"`
}

// Holdout counts every visitor who viewed a page of the site between from
// and to, by whether they fall in group's holdout (the top holdout percent
// of its buckets, by the same hash the flags use), and compares the goal
// after their first view.
func Holdout(ctx context.Context, q Querier, ws, group string, holdout int, g Goal, from, to time.Time) (HoldoutResult, error) {
	out := HoldoutResult{Group: group, Holdout: holdout, Goal: g, Metric: g.MetricOf(), From: from, To: to,
		Arms: []Arm{{Variant: "holdout"}, {Variant: "everyone_else"}}}
	if holdout <= 0 {
		return out, nil
	}
	hits := hitsSQL(g, "$1", "$3::timestamptz", "$4", "$5", "$6")
	arg, window, prop, capv := goalArgs(g)
	bucket := flags.GroupBucketSQL("$8::text", "e.anonymous_id")
	rows, err := q.Query(ctx, `
		WITH params AS (SELECT $4::text, $5::int, $6::text, $7::float8, $9::int), seen AS (
			SELECT DISTINCT ON (e.anonymous_id) e.anonymous_id,
				CASE WHEN `+bucket+` >= $9::int THEN 'holdout' ELSE 'everyone_else' END AS variant, e.occurred_at
			FROM customer_events e
			WHERE e.workspace_id = $1 AND e.name = 'page.viewed' AND e.anonymous_id IS NOT NULL
				AND e.occurred_at >= $2::timestamptz AND e.occurred_at < $3::timestamptz
			ORDER BY e.anonymous_id, e.occurred_at
		), hits AS (`+hits+`
		), per AS (
			SELECT anonymous_id, least(sum(val), $7::float8) AS v FROM hits GROUP BY 1
		)
		SELECT s.variant, count(*), count(*) FILTER (WHERE p.v > 0), coalesce(sum(p.v), 0)::float8, coalesce(sum(p.v * p.v), 0)::float8
		FROM seen s LEFT JOIN per p ON p.anonymous_id = s.anonymous_id GROUP BY 1`,
		ws, from, to, arg, window, prop, capv, group, flags.Buckets-holdout*100)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var variant string
		var a Arm
		if err := rows.Scan(&variant, &a.Visitors, &a.Conversions, &a.Sum, &a.SumSq); err != nil {
			return out, err
		}
		i := 1
		if variant == "holdout" {
			i = 0
		}
		a.Variant = out.Arms[i].Variant
		if out.Metric != MetricConversion && a.Visitors > 0 {
			a.Mean = a.Sum / float64(a.Visitors)
		} else {
			a.Sum, a.SumSq = 0, 0
		}
		out.Arms[i] = a
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if c := CompareSeries([][]Arm{out.Arms}, out.Metric, DefaultMinEffect, 0); len(c) == 1 {
		out.Comparison = &c[0]
	}
	return out, nil
}

// HoldoutDays bounds how far back a holdout report reads.
const (
	HoldoutDaysDefault = 28
	HoldoutDaysMax     = 180
)
