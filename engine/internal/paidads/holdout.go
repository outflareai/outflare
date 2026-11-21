package paidads

import (
	"context"
	"math"
	"time"
)

// Holdout tests: what the ads really add.
//
// Attribution says which ad a person clicked before signing up. It cannot
// say whether they would have signed up anyway. A holdout answers that:
// the brand stops showing ads in some countries for a while, and the
// sign-ups from those countries (from every source, ads or not) are
// compared with the sign-ups from everywhere else, over the same number
// of days before the holdout and during it. If the other countries moved
// by some factor, the held-out countries would have moved by the same
// factor had the ads kept running; what is missing is what the ads were
// adding.
//
// A sign-up's country is the country of the person's latest visit up to a
// day after they signed up. Days are UTC.

// holdoutEnough is how many sign-ups each side needs before the holdout
// to say anything.
const holdoutEnough = 30

// HoldoutSide is one group of countries over one period.
type HoldoutSide struct {
	// Sign-ups from every source, and those an ad is credited with.
	Signups   int `json:"signups"`
	AdSignups int `json:"ad_signups"`
	// Visits that arrived from a paid click.
	PaidVisits int `json:"paid_visits"`
}

// HoldoutResult is the comparison so far.
type HoldoutResult struct {
	// The days compared: the same number before the start as have passed
	// since (at most the holdout's length).
	Days       int         `json:"days"`
	BeforeFrom string      `json:"before_from"`
	Through    string      `json:"through"`
	HeldBefore HoldoutSide `json:"held_before"`
	HeldDuring HoldoutSide `json:"held_during"`
	RestBefore HoldoutSide `json:"rest_before"`
	RestDuring HoldoutSide `json:"rest_during"`
	// Expected is the sign-ups the held-out countries would have had with
	// the ads running; Lift is how many fewer they had: what the ads were
	// adding. Credited is what attribution gave the ads there before, and
	// Incrementality is Lift over Credited. Nil while there is too little
	// to say.
	Expected       *float64 `json:"expected_signups"`
	Lift           *float64 `json:"lift_signups"`
	Incrementality *float64 `json:"incrementality"`
	// Leaked is true when paid clicks still arrived from the held-out
	// countries at more than a tenth of their rate before: the ads were
	// not really off.
	Leaked bool `json:"leaked"`
	// Verdict is scheduled, not_enough_data, leaked, ads_add (the ads
	// were adding sign-ups), ads_add_little (under a third of what they
	// are credited with) or no_difference.
	Verdict string `json:"verdict"`
}

// MeasureHoldout counts the holdout's sides. connector narrows the
// credited sign-ups and paid visits to one network; ” is every network.
func MeasureHoldout(ctx context.Context, q Querier, ws, brand, connector string, countries []string, startsOn, endsOn, now time.Time) (HoldoutResult, error) {
	day := 24 * time.Hour
	start := startsOn.UTC().Truncate(day)
	end := endsOn.UTC().Truncate(day).Add(day) // exclusive
	if t := now.UTC().Truncate(day).Add(day); t.Before(end) {
		end = t
	}
	var out HoldoutResult
	if !end.After(start) {
		out.Verdict = "scheduled"
		return out, nil
	}
	out.Days = int(end.Sub(start) / day)
	before := start.Add(-time.Duration(out.Days) * day)
	out.BeforeFrom, out.Through = before.Format(time.DateOnly), end.Add(-day).Format(time.DateOnly)
	names := signupNames(ctx, q, ws, brand)
	rows, err := q.Query(ctx, `
		WITH su AS (
			SELECT e.record_id, min(e.occurred_at) AS at
			FROM customer_events e
			WHERE e.workspace_id = $1 AND e.kind = 'track' AND e.record_id IS NOT NULL AND e.name = ANY($3)
			  AND COALESCE(e.brand_id, outflare_first_brand(e.workspace_id)) = $2::uuid
			  AND e.occurred_at >= $4 AND e.occurred_at < $6
			GROUP BY e.record_id
		),
		placed AS (
			SELECT su.record_id, su.at >= $5 AS during,
			       COALESCE((SELECT s.country FROM site_sessions s
			                 WHERE s.workspace_id = $1 AND s.country <> '' AND s.started_at <= su.at + interval '1 day'
			                   AND (s.record_id = su.record_id OR s.anonymous_id IN (SELECT v.anonymous_id FROM site_visitors v WHERE v.workspace_id = $1 AND v.record_id = su.record_id))
			                 ORDER BY s.started_at DESC LIMIT 1), '') = ANY($7::text[]) AS held,
			       EXISTS (SELECT 1 FROM ad_acquisitions a WHERE a.brand_id = $2::uuid AND a.record_id = su.record_id
			                 AND ($8 = '' OR a.connector = $8)) AS ad
			FROM su
		),
		visits AS (
			SELECT s.started_at >= $5 AS during, s.country = ANY($7::text[]) AS held, count(*) AS n
			FROM site_sessions s
			WHERE s.workspace_id = $1 AND (s.brand_id IS NULL OR s.brand_id = $2::uuid)
			  AND s.started_at >= $4 AND s.started_at < $6
			  AND (s.channel = 'paid_ads' OR s.ad <> '{}'::jsonb
			       OR EXISTS (SELECT 1 FROM jsonb_object_keys(s.click_ids) k WHERE k = ANY($9::text[])))
			GROUP BY 1, 2
		)
		SELECT during, held, count(*)::int, (count(*) FILTER (WHERE ad))::int, 0 FROM placed GROUP BY 1, 2
		UNION ALL
		SELECT during, held, 0, 0, n::int FROM visits`,
		ws, brand, names, before, start, end, countries, connector, paidParams)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var during, held bool
		var s, ad, v int
		if err := rows.Scan(&during, &held, &s, &ad, &v); err != nil {
			return out, err
		}
		side := &out.RestBefore
		switch {
		case held && during:
			side = &out.HeldDuring
		case held:
			side = &out.HeldBefore
		case during:
			side = &out.RestDuring
		}
		side.Signups += s
		side.AdSignups += ad
		side.PaidVisits += v
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Leaked = out.HeldBefore.PaidVisits > 0 && out.HeldDuring.PaidVisits*10 > out.HeldBefore.PaidVisits
	switch {
	case out.HeldBefore.Signups < holdoutEnough || out.RestBefore.Signups < holdoutEnough:
		out.Verdict = "not_enough_data"
		return out, nil
	case out.Leaked:
		out.Verdict = "leaked"
		return out, nil
	}
	expected := float64(out.HeldBefore.Signups) * float64(out.RestDuring.Signups) / float64(out.RestBefore.Signups)
	lift := expected - float64(out.HeldDuring.Signups)
	out.Expected, out.Lift = &expected, &lift
	credited := float64(out.HeldBefore.AdSignups)
	if credited > 0 {
		inc := lift / credited
		out.Incrementality = &inc
	}
	// The difference has to be more than chance: about two standard
	// deviations of a count this size.
	switch noise := 2 * math.Sqrt(expected+float64(out.HeldDuring.Signups)); {
	case lift <= noise:
		out.Verdict = "no_difference"
	case out.Incrementality != nil && *out.Incrementality < 1.0/3:
		out.Verdict = "ads_add_little"
	default:
		out.Verdict = "ads_add"
	}
	return out, nil
}
