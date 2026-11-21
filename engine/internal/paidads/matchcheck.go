package paidads

import (
	"context"
	"time"
)

// The match check: a dry run of joining visits by network and device.
//
// Outflare ties a visit to a person only by what the browser itself says
// (a sign-in, an email click, a visitor link). A person who clicks an ad
// in one browser and signs up in another is therefore not credited to the
// ad. The check answers, from the visits already stored and without
// collecting or joining anything, how often the two visits could have
// been tied together because they came from the same network, place and
// kind of device, and how often that guess would have been wrong.
//
// The signature of a visit is its network (ASN), country, region, city,
// time zone, device type, browser, system, device brand and model: what
// site_sessions already keeps. It holds no address and no user agent. A
// visit without a network or a browser has no signature.

const (
	// MatchCheckDefaultDays and MatchCheckMaxDays bound the period read.
	MatchCheckDefaultDays = 30
	MatchCheckMaxDays     = 90
	// matchCommon is how many browsers may share a signature before it is
	// too common to say anything (an office, a mobile carrier in a city).
	matchCommon = 50
	// matchEnough is how many people and known pairs the verdict needs.
	matchEnough = 20
)

// MatchCheck is what the dry run found.
type MatchCheck struct {
	Days       int       `json:"days"`
	WindowDays int       `json:"window_days"`
	Since      time.Time `json:"since"`
	// Visits in the period, and how many had a signature.
	Visits       int `json:"visits"`
	VisitsUsable int `json:"visits_usable"`
	// Browsers with a signature, and how many of them shared theirs with
	// no other browser.
	Browsers       int `json:"browsers"`
	BrowsersUnique int `json:"browsers_unique"`
	// People known by name who visited in the period, and how many of
	// them an ad is already credited with.
	People         int `json:"people"`
	PeopleCredited int `json:"people_credited"`
	// Of the people no ad is credited with: exactly one unknown browser
	// with the same signature clicked an ad within the window before
	// (would_credit), more than one did or the signature is too common
	// (ambiguous), none did (no_match).
	WouldCredit int `json:"would_credit"`
	Ambiguous   int `json:"ambiguous"`
	NoMatch     int `json:"no_match"`
	// The test against what is already known: pairs of browsers with the
	// same signature that each belong to a known person, and how many of
	// the pairs are the same person. Accuracy is their share, nil without
	// a pair.
	KnownPairs      int      `json:"known_pairs"`
	KnownPairsRight int      `json:"known_pairs_right"`
	Accuracy        *float64 `json:"accuracy"`
	// Verdict is worth_testing, not_worth_it or not_enough_data.
	Verdict string `json:"verdict"`
}

// RunMatchCheck reads the brand's stored visits and changes nothing.
func RunMatchCheck(ctx context.Context, q Querier, ws, brand string, days int, now time.Time) (MatchCheck, error) {
	econ, err := Load(ctx, q, ws, brand)
	if err != nil {
		return MatchCheck{}, err
	}
	if days <= 0 {
		days = MatchCheckDefaultDays
	}
	out := MatchCheck{Days: days, WindowDays: econ.AcquisitionWindowDays, Since: now.AddDate(0, 0, -days).UTC()}
	err = q.QueryRow(ctx, `
		WITH s AS (
			SELECT s.anonymous_id, s.started_at, COALESCE(s.record_id, v.record_id) AS record_id,
			       (s.channel = 'paid_ads' OR s.ad <> '{}'::jsonb
			        OR EXISTS (SELECT 1 FROM jsonb_object_keys(s.click_ids) k WHERE k = ANY($5::text[]))) AS paid,
			       CASE WHEN s.asn <> 0 AND s.browser <> '' THEN md5(concat_ws('|', s.asn, s.country, s.region, s.city,
			            s.timezone, s.device, s.browser, s.os, s.device_brand, s.device_model)) END AS sig
			FROM site_sessions s
			LEFT JOIN site_visitors v ON v.workspace_id = s.workspace_id AND v.anonymous_id = s.anonymous_id
			WHERE s.workspace_id = $1 AND (s.brand_id IS NULL OR s.brand_id = $2::uuid)
			  AND s.started_at > $3::timestamptz - make_interval(days => $4) AND s.started_at <= $6
		),
		-- One row per browser and signature, with who it is when known.
		b AS (
			SELECT sig, anonymous_id, (array_agg(record_id) FILTER (WHERE record_id IS NOT NULL))[1] AS record_id,
			       min(started_at) AS first_at, max(started_at) AS last_at,
			       min(started_at) FILTER (WHERE paid) AS first_paid, max(started_at) FILTER (WHERE paid) AS last_paid,
			       max(started_at) FILTER (WHERE started_at > $3) AS last_in
			FROM s WHERE sig IS NOT NULL GROUP BY sig, anonymous_id
		),
		c AS (SELECT sig, count(*) AS browsers FROM b GROUP BY sig),
		people AS (
			SELECT s.record_id,
			       EXISTS (SELECT 1 FROM ad_acquisitions a WHERE a.brand_id = $2::uuid AND a.record_id = s.record_id) AS credited
			FROM s WHERE s.record_id IS NOT NULL AND s.started_at > $3 GROUP BY s.record_id
		),
		-- For each person no ad is credited with: the unknown browsers that
		-- clicked an ad, with a signature the person also has, in the window
		-- before one of the person's visits.
		cand AS (
			SELECT p.record_id,
			       count(DISTINCT u.anonymous_id) FILTER (WHERE c.browsers <= $7) AS browsers,
			       bool_or(c.browsers > $7) AS common
			FROM people p
			JOIN b k ON k.record_id = p.record_id AND k.last_in IS NOT NULL
			JOIN c ON c.sig = k.sig
			JOIN b u ON u.sig = k.sig AND u.record_id IS NULL AND u.first_paid IS NOT NULL
			       AND u.first_paid <= k.last_at AND u.last_paid > k.first_at - make_interval(days => $4)
			WHERE NOT p.credited
			GROUP BY p.record_id
		),
		pairs AS (
			SELECT (k1.record_id = k2.record_id) AS same
			FROM b k1
			JOIN c ON c.sig = k1.sig AND c.browsers <= $7
			JOIN b k2 ON k2.sig = k1.sig AND k2.anonymous_id > k1.anonymous_id AND k2.record_id IS NOT NULL
			       AND k1.first_at <= k2.last_at + make_interval(days => $4)
			       AND k2.first_at <= k1.last_at + make_interval(days => $4)
			WHERE k1.record_id IS NOT NULL
		)
		SELECT
			(SELECT count(*) FROM s WHERE started_at > $3),
			(SELECT count(*) FROM s WHERE started_at > $3 AND sig IS NOT NULL),
			(SELECT count(DISTINCT anonymous_id) FROM b WHERE last_in IS NOT NULL),
			(SELECT count(DISTINCT b.anonymous_id) FROM b JOIN c ON c.sig = b.sig WHERE c.browsers = 1 AND b.last_in IS NOT NULL),
			(SELECT count(*) FROM people),
			(SELECT count(*) FROM people WHERE credited),
			(SELECT count(*) FROM cand WHERE browsers = 1 AND NOT common),
			(SELECT count(*) FROM cand WHERE browsers > 1 OR common),
			(SELECT count(*) FROM pairs),
			(SELECT count(*) FROM pairs WHERE same)`,
		ws, brand, out.Since, out.WindowDays, paidParams, now, matchCommon).
		Scan(&out.Visits, &out.VisitsUsable, &out.Browsers, &out.BrowsersUnique, &out.People, &out.PeopleCredited,
			&out.WouldCredit, &out.Ambiguous, &out.KnownPairs, &out.KnownPairsRight)
	if err != nil {
		return MatchCheck{}, err
	}
	open := out.People - out.PeopleCredited
	out.NoMatch = max(open-out.WouldCredit-out.Ambiguous, 0)
	if out.KnownPairs > 0 {
		a := float64(out.KnownPairsRight) / float64(out.KnownPairs)
		out.Accuracy = &a
	}
	switch {
	case open < matchEnough || out.KnownPairs < matchEnough:
		out.Verdict = "not_enough_data"
	case *out.Accuracy >= 0.9 && float64(out.WouldCredit) >= 0.05*float64(open):
		out.Verdict = "worth_testing"
	default:
		out.Verdict = "not_worth_it"
	}
	return out, nil
}
