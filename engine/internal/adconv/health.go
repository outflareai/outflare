package adconv

import (
	"context"
	"math"
	"time"

	"github.com/outflareai/outflare/engine/internal/conversions"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Reconciliation: what the platform counts for the feed's conversion
// actions, what Outflare sent it, and (when the paid ads ledger exists)
// the first-party new payers, per day and per campaign, so a person can
// see whether the feed is reaching Google. The ratio of conversions the
// platform accepted to sends in the last week is compared with the four
// weeks before; a move of more than drift raises ad_feed.ratio_drifted.

const (
	healthDays = 35
	drift      = 0.3
)

// Health is one account's reconciliation.
type Health struct {
	Days      []HealthDay      `json:"days"`
	Campaigns []HealthCampaign `json:"campaigns"`
	// Ratio7d is sends accepted over platform conversions in the last 7
	// days; Baseline28d the same over the 28 days before. Null without
	// conversions to compare.
	Ratio7d     *float64 `json:"ratio_7d"`
	Baseline28d *float64 `json:"baseline_28d"`
	Drifted     bool     `json:"drifted"`
	// HasLedger says whether new_payers is known (the ad_acquisitions
	// table exists).
	HasLedger bool `json:"has_ledger"`
}

// HealthDay is one day of the account.
type HealthDay struct {
	Day                 string  `json:"day"`
	PlatformConversions float64 `json:"platform_conversions"`
	SendsAccepted       int64   `json:"sends_accepted"`
	SendsFailed         int64   `json:"sends_failed"`
	NewPayers           *int64  `json:"new_payers"`
}

// HealthCampaign is one campaign over the last 28 days.
type HealthCampaign struct {
	CampaignID          string  `json:"campaign_id"`
	Name                string  `json:"name"`
	PlatformConversions float64 `json:"platform_conversions"`
	NewPayers           *int64  `json:"new_payers"`
}

// FeedHealth computes an account's reconciliation over the last healthDays
// days.
func FeedHealth(ctx context.Context, q conversions.Querier, ws, accountID string, now time.Time) (Health, error) {
	var h Health
	feed, err := LoadFeed(ctx, q, ws, accountID)
	if err != nil {
		return h, err
	}
	var names []string
	for _, a := range feed.Actions {
		names = append(names, a.Name)
	}
	if names == nil {
		names = []string{}
	}
	if err := q.QueryRow(ctx, `SELECT to_regclass('public.ad_acquisitions') IS NOT NULL`).Scan(&h.HasLedger); err != nil {
		return h, err
	}
	from := now.UTC().AddDate(0, 0, -healthDays).Format("2006-01-02")
	byDay := map[string]*HealthDay{}
	day := func(d string) *HealthDay {
		if byDay[d] == nil {
			byDay[d] = &HealthDay{Day: d}
			h.Days = append(h.Days, HealthDay{})
		}
		return byDay[d]
	}
	rows, err := q.Query(ctx, `SELECT x.day::text, sum(x.conversions)::float8 FROM ad_conversion_actions_daily x
		JOIN ad_entities e ON e.id = x.ad_entity_id AND e.level = 'campaign'
		WHERE x.workspace_id = $1 AND e.ad_account_id = $2 AND x.day >= $3::date AND x.action = ANY($4) GROUP BY 1`, ws, accountID, from, names)
	if err != nil {
		return h, err
	}
	for rows.Next() {
		var d string
		var n float64
		if err := rows.Scan(&d, &n); err != nil {
			rows.Close()
			return h, err
		}
		day(d).PlatformConversions = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return h, err
	}
	rows, err = q.Query(ctx, `SELECT (COALESCE(q.sent_at, q.updated_at) AT TIME ZONE 'UTC')::date::text,
			count(*) FILTER (WHERE q.status = 'sent'), count(*) FILTER (WHERE q.status = 'failed')
		FROM ad_conversion_sends q JOIN ad_conversion_rules r ON r.id = q.rule_id
		WHERE q.workspace_id = $1 AND r.ad_account_id = $2 AND r.feed_role <> 'custom' AND q.status IN ('sent', 'failed')
		  AND COALESCE(q.sent_at, q.updated_at) >= $3::date GROUP BY 1`, ws, accountID, from)
	if err != nil {
		return h, err
	}
	for rows.Next() {
		var d string
		var sent, failed int64
		if err := rows.Scan(&d, &sent, &failed); err != nil {
			rows.Close()
			return h, err
		}
		x := day(d)
		x.SendsAccepted, x.SendsFailed = sent, failed
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return h, err
	}
	payersByCampaign := map[string]int64{}
	if h.HasLedger {
		rows, err = q.Query(ctx, `SELECT (first_paid_at AT TIME ZONE 'UTC')::date::text, coalesce(campaign_entity_id::text, ''), count(*)
			FROM ad_acquisitions WHERE workspace_id = $1 AND ad_account_id = $2 AND first_paid_at >= $3::date GROUP BY 1, 2`, ws, accountID, from)
		if err != nil {
			return h, err
		}
		for rows.Next() {
			var d, campaign string
			var n int64
			if err := rows.Scan(&d, &campaign, &n); err != nil {
				rows.Close()
				return h, err
			}
			x := day(d)
			if x.NewPayers == nil {
				x.NewPayers = new(int64)
			}
			*x.NewPayers += n
			payersByCampaign[campaign] += n
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return h, err
		}
	}
	// Every day in the window, oldest first.
	h.Days = h.Days[:0]
	for i := healthDays; i >= 0; i-- {
		d := now.UTC().AddDate(0, 0, -i).Format("2006-01-02")
		x := byDay[d]
		if x == nil {
			x = &HealthDay{Day: d}
		}
		if h.HasLedger && x.NewPayers == nil {
			x.NewPayers = new(int64)
		}
		h.Days = append(h.Days, *x)
	}
	rows, err = q.Query(ctx, `SELECT e.id::text, e.name, coalesce(sum(x.conversions), 0)::float8 FROM ad_entities e
		LEFT JOIN ad_conversion_actions_daily x ON x.ad_entity_id = e.id AND x.day >= $3::date AND x.action = ANY($4)
		WHERE e.workspace_id = $1 AND e.ad_account_id = $2 AND e.level = 'campaign' AND e.status IN ('active', 'paused')
		GROUP BY e.id, e.name ORDER BY 3 DESC, e.name LIMIT 200`, ws, accountID, now.UTC().AddDate(0, 0, -28).Format("2006-01-02"), names)
	if err != nil {
		return h, err
	}
	h.Campaigns = []HealthCampaign{}
	for rows.Next() {
		var c HealthCampaign
		if err := rows.Scan(&c.CampaignID, &c.Name, &c.PlatformConversions); err != nil {
			rows.Close()
			return h, err
		}
		if h.HasLedger {
			n := payersByCampaign[c.CampaignID]
			c.NewPayers = &n
		}
		h.Campaigns = append(h.Campaigns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return h, err
	}
	h.Ratio7d, h.Baseline28d, h.Drifted = ratios(h.Days)
	return h, nil
}

// ratios compares the last week's sends-accepted-to-conversions ratio with
// the four weeks before. Both need conversions, else nothing is said.
func ratios(days []HealthDay) (week, base *float64, drifted bool) {
	n := len(days)
	if n < 36 {
		return nil, nil, false
	}
	var wSent, wConv, bSent, bConv float64
	for i, d := range days {
		switch {
		case i >= n-7:
			wSent += float64(d.SendsAccepted)
			wConv += d.PlatformConversions
		case i >= n-35:
			bSent += float64(d.SendsAccepted)
			bConv += d.PlatformConversions
		}
	}
	if wConv > 0 {
		r := wSent / wConv
		week = &r
	}
	if bConv > 0 {
		r := bSent / bConv
		base = &r
	}
	if week != nil && base != nil && *base > 0 {
		drifted = math.Abs(*week / *base - 1) > drift
	}
	return week, base, drifted
}

// checkHealth raises ad_feed.ratio_drifted when the week's ratio moved
// away from its baseline.
func (s *Sender) checkHealth(ctx context.Context, ws, accountID string) {
	h, err := FeedHealth(ctx, s.Pool, ws, accountID, s.now())
	if err != nil {
		track.Fail(ctx, s.Log, track.Event{Name: "ad_feed.health_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"ad_account_id": accountID}}, "internal", err)
		return
	}
	if !h.Drifted {
		return
	}
	track.Emit(ctx, track.Event{Name: "ad_feed.ratio_drifted", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Failure, ErrorCode: "ratio_drifted",
		Props: map[string]any{"ad_account_id": accountID, "ratio_7d": *h.Ratio7d, "baseline_28d": *h.Baseline28d}})
}
