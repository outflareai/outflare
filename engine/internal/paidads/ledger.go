package paidads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/fxrates"
)

// The queries behind the performance and acquisitions reports: what each
// campaign, ad group or ad spent and what the ledger says it brought, in
// one unit, cost per new paying customer, with cost per sign-up beside it.

// Money is an amount in one currency, in minor units (cents). Amounts are
// given per currency and never converted.
type Money struct {
	Currency    string `json:"currency"`
	AmountMinor int64  `json:"amount_minor"`
}

// Levels a performance report is at.
const (
	LevelCampaign = "campaign"
	LevelAdGroup  = "ad_group"
	LevelAd       = "ad"
)

// entityCol is the ad_acquisitions column that names the entity at a level.
var entityCol = map[string]string{LevelCampaign: "campaign_entity_id", LevelAdGroup: "ad_group_entity_id", LevelAd: "ad_entity_id"}

// adKey is the key of a visit's ad object that carries the network's id
// for the entity at a level.
var adKey = map[string]string{LevelCampaign: "campaign", LevelAdGroup: "adset", LevelAd: "ad"}

// PerformanceQuery is one performance read.
type PerformanceQuery struct {
	WorkspaceID, BrandID string
	Level                string
	// From and To are dates (UTC), inclusive. Ad platforms' days are their
	// accounts'; events are bucketed in Location (the brand's reporting
	// time zone, B4a), UTC when nil.
	From, To time.Time
	Location *time.Location
	// Filters, each optional.
	Connector, AccountID, CampaignID string
	// WindowDays recomputes acquisitions from visits at read time with this
	// window instead of the stored one; 0 reads the ledger as stored.
	WindowDays int
	// Customers keeps the revenue to new, returning or all customers.
	Customers string
	// Model, when set, also counts each entity under an attribution model:
	// the id its credits are stored under (attribution.Resolve). Empty
	// leaves the rows with the last paid click only.
	Model string
	// Econ is the brand's economics, for the target and the payback maths.
	Econ Economics
	// Idle lists every entity, also one that is paused or removed with no
	// spend, sign-ups or payers in the range (a campaign that never ran).
	Idle bool
	// Rates, when set, turn every row's money into Econ.Currency, so
	// accounts billed in different currencies add up. Nil leaves each row
	// in its account's currency.
	Rates Rates
}

// PerformanceRow is one entity's numbers over the range.
type PerformanceRow struct {
	ID          string  `json:"id"`
	Level       string  `json:"level"`
	Name        string  `json:"name"`
	ExternalID  string  `json:"external_id"`
	Status      string  `json:"status"`
	Connector   string  `json:"connector"`
	AdAccountID string  `json:"ad_account_id"`
	AccountName string  `json:"account_name"`
	CampaignID  *string `json:"campaign_id"`
	// Currency is what the row's money is in: the brand's currency when
	// the query converts (Rates), else the account's. AccountCurrency is
	// what the ad account is billed in.
	Currency        string `json:"currency"`
	AccountCurrency string `json:"account_currency"`
	// Spend, clicks, impressions and the platform's own conversions.
	SpendMicros         int64    `json:"spend_micros"`
	Clicks              int64    `json:"clicks"`
	Impressions         int64    `json:"impressions"`
	PlatformConversions *float64 `json:"platform_conversions"`
	// PlatformAllConversions also counts the network's secondary
	// conversion actions (Google's all conversions).
	PlatformAllConversions *float64 `json:"platform_all_conversions"`
	// Outflare's own count: sign-ups and first-time payers the entity's
	// clicks brought in the range, and what each cost.
	Signups               int    `json:"signups"`
	NewPayers             int    `json:"new_payers"`
	CostPerSignupMicros   *int64 `json:"cost_per_signup_micros"`
	CostPerNewPayerMicros *int64 `json:"cost_per_new_payer_micros"`
	TargetStatus          string `json:"target_status"`
	// Revenue in the range from first payments (new), from later payments by
	// people ads acquired whose last paid click within the window was
	// this entity's (returning;
	// for ad groups and ads, later payments by the people they acquired),
	// and every payment to date by the people it acquired (lifetime).
	NewRevenue       []Money `json:"new_revenue"`
	ReturningRevenue []Money `json:"returning_revenue"`
	LifetimeRevenue  []Money `json:"lifetime_revenue"`
	// ROAS is the return on ad spend: the new and returning revenue above
	// (so it follows the customers filter) in the account's currency,
	// divided by the spend. Nil without spend, or when the only revenue is
	// in other currencies; zero when there is spend and no revenue.
	ROAS *float64 `json:"roas"`
	// PaybackMonths is how long a new customer takes to pay back what they
	// cost, from the lifetime revenue per month of the people acquired and
	// the brand's margin. Nil without payers or revenue in the account's
	// currency.
	PaybackMonths *float64 `json:"payback_months"`
	// The same spend over the same number of days just before the range,
	// and which way it moved.
	PreviousSpendMicros int64    `json:"previous_spend_micros"`
	SpendChange         *float64 `json:"spend_change"`
	Trend               string   `json:"trend"`
	// Window is the acquisition window the row was worked out with.
	WindowDays int `json:"window_days"`
	// The same entity under the attribution model the query names: the
	// credit its visits got for sign-ups in the range (a share of each, so
	// not a whole number), the credited share of the money of every valued
	// conversion in the range (payments, less refunds), and the cost per
	// sign-up and return on ad spend those give. Nil and empty when the
	// query names no model.
	ModelSignups             *float64 `json:"model_signups"`
	ModelRevenue             []Money  `json:"model_revenue"`
	ModelCostPerSignupMicros *int64   `json:"model_cost_per_signup_micros"`
	ModelROAS                *float64 `json:"model_roas"`

	payerMonths float64
	lifetime    map[string]int64
}

// trendBand is how much spend must move against the period before to
// count as scaling up or down, as the campaigns list judges.
const trendBand = 0.15

// Performance reads the entities at a level in the brand's accounts with
// what each spent and brought over the range.
func Performance(ctx context.Context, q Querier, p PerformanceQuery) ([]PerformanceRow, error) {
	col, ok := entityCol[p.Level]
	if !ok {
		return nil, fmt.Errorf("level %q is campaign, ad_group or ad", p.Level)
	}
	window := p.Econ.AcquisitionWindowDays
	if window == 0 {
		window = DefaultWindowDays
	}
	if p.WindowDays > 0 {
		window = p.WindowDays
	}
	from, to := p.From.UTC().Format(time.DateOnly), p.To.UTC().Format(time.DateOnly)
	days := int(p.To.Sub(p.From).Hours()/24) + 1
	prevFrom, prevTo := p.From.AddDate(0, 0, -days).Format(time.DateOnly), p.From.AddDate(0, 0, -1).Format(time.DateOnly)
	// $6 and $7: when from begins and the day after to begins, in the
	// brand's time zone, for the events' timestamps.
	args := []any{p.WorkspaceID, p.BrandID, p.Level, from, to, DayStart(p.From, p.Location), DayStart(p.To.AddDate(0, 0, 1), p.Location)}
	where := ""
	if p.Connector != "" {
		args = append(args, p.Connector)
		where += ` AND a.connector = $` + strconv.Itoa(len(args))
	}
	if p.AccountID != "" {
		args = append(args, p.AccountID)
		where += ` AND a.id = $` + strconv.Itoa(len(args))
	}
	if p.CampaignID != "" {
		args = append(args, p.CampaignID)
		where += ` AND CASE e.level WHEN 'campaign' THEN e.id WHEN 'ad_group' THEN e.parent_id ELSE g.parent_id END = $` + strconv.Itoa(len(args))
	}
	ents := `ents AS (
		SELECT e.id, e.name, e.external_id, e.status, e.level, a.id AS account_id, a.name AS account_name, a.connector, a.currency,
			CASE e.level WHEN 'campaign' THEN e.id WHEN 'ad_group' THEN e.parent_id ELSE g.parent_id END AS campaign_id
		FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id LEFT JOIN ad_entities g ON g.id = e.parent_id
		WHERE e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2::uuid AND e.level = $3` + where + `)`
	stored := `acq AS (SELECT q.` + col + ` AS entity_id, q.id, q.signed_up_at, q.first_paid_at FROM ad_acquisitions q
		WHERE q.brand_id = $2::uuid AND q.` + col + ` IS NOT NULL)`
	acq := stored
	// The revenue query takes the arguments so far; the main query adds
	// the previous period and, for a read-time window, its own.
	rargs := append([]any{}, args...)
	args = append(args, prevFrom, prevTo)
	pf, pt := strconv.Itoa(len(args)-1), strconv.Itoa(len(args))
	if p.WindowDays > 0 && p.WindowDays != p.Econ.AcquisitionWindowDays {
		// Recomputed from visits with this window: each sign-up and first
		// payment in the range is credited to the last paid click within
		// window days before it whose ids name an entity at this level.
		args = append(args, p.WindowDays, paidParams, signupNames(ctx, q, p.WorkspaceID, p.BrandID), adKey[p.Level])
		n := len(args)
		acq = `acq AS (
			SELECT ent.id AS entity_id, NULL::uuid AS id,
				min(h.occurred_at) FILTER (WHERE NOT h.payment) AS signed_up_at,
				min(h.occurred_at) FILTER (WHERE h.payment) AS first_paid_at
			FROM (
				SELECT c.record_id, c.occurred_at, c.payment, v.ad
				FROM (
					SELECT e.record_id, e.anonymous_id, e.occurred_at, e.name = 'payment.succeeded' AS payment
					FROM customer_events e
					WHERE e.workspace_id = $1 AND e.kind = 'track' AND e.record_id IS NOT NULL
					  AND COALESCE(e.brand_id, outflare_first_brand(e.workspace_id)) = $2::uuid
					  AND e.occurred_at >= $6 AND e.occurred_at < $7
					  AND (e.name = ANY($` + strconv.Itoa(n-1) + `::text[]) OR (e.name = 'payment.succeeded' AND NOT EXISTS (
						SELECT 1 FROM customer_events p WHERE p.workspace_id = e.workspace_id AND p.record_id = e.record_id AND p.kind = 'track'
						  AND p.name = 'payment.succeeded' AND (p.occurred_at, p.id) < (e.occurred_at, e.id)
						  AND COALESCE(p.brand_id, outflare_first_brand(p.workspace_id)) = $2::uuid)))
				) c CROSS JOIN LATERAL (
					SELECT s.ad FROM site_sessions s
					WHERE s.workspace_id = $1 AND (s.record_id = c.record_id OR (c.anonymous_id IS NOT NULL AND s.anonymous_id = c.anonymous_id)
					       OR s.anonymous_id IN (SELECT w.anonymous_id FROM site_visitors w WHERE w.workspace_id = $1 AND w.record_id = c.record_id))
					  AND s.started_at <= c.occurred_at AND s.started_at > c.occurred_at - make_interval(days => $` + strconv.Itoa(n-3) + `)
					  AND (s.brand_id IS NULL OR s.brand_id = $2::uuid)
					  AND (s.channel = 'paid_ads' OR s.ad <> '{}'::jsonb OR EXISTS (SELECT 1 FROM jsonb_object_keys(s.click_ids) k WHERE k = ANY($` + strconv.Itoa(n-2) + `::text[])))
					ORDER BY s.started_at DESC LIMIT 1) v
			) h JOIN ents ent ON ent.external_id = h.ad->>$` + strconv.Itoa(n) + `
			GROUP BY ent.id, h.record_id)`
	}
	args = append(args, p.Idle)
	idleArg := len(args)
	rows, err := q.Query(ctx, `WITH `+ents+`, `+acq+`,
		spend AS (SELECT m.ad_entity_id, sum(m.spend_micros)::bigint s, sum(m.clicks)::bigint c, sum(m.impressions)::bigint i, sum(m.conversions)::float8 cv,
			sum(m.all_conversions)::float8 acv
			FROM ad_metrics_daily m WHERE m.workspace_id = $1 AND m.ad_entity_id IN (SELECT id FROM ents) AND m.day BETWEEN $4::date AND $5::date GROUP BY 1),
		prev AS (SELECT m.ad_entity_id, sum(m.spend_micros)::bigint s
			FROM ad_metrics_daily m WHERE m.workspace_id = $1 AND m.ad_entity_id IN (SELECT id FROM ents) AND m.day BETWEEN $`+pf+`::date AND $`+pt+`::date GROUP BY 1),
		counts AS (SELECT a.entity_id,
				count(*) FILTER (WHERE a.signed_up_at >= $6 AND a.signed_up_at < $7)::int AS signups,
				count(*) FILTER (WHERE a.first_paid_at >= $6 AND a.first_paid_at < $7)::int AS payers,
				COALESCE(sum(GREATEST(1, extract(epoch from now() - a.first_paid_at) / 2629800)) FILTER (WHERE a.first_paid_at IS NOT NULL AND a.first_paid_at < $7), 0)::float8 AS payer_months
			FROM acq a GROUP BY 1)
		SELECT ents.id::text, ents.name, ents.external_id, ents.status, ents.connector, ents.account_id::text, ents.account_name, ents.campaign_id::text, ents.currency,
			COALESCE(spend.s, 0), COALESCE(spend.c, 0), COALESCE(spend.i, 0), spend.cv, spend.acv, COALESCE(prev.s, 0),
			COALESCE(counts.signups, 0), COALESCE(counts.payers, 0), COALESCE(counts.payer_months, 0)
		FROM ents LEFT JOIN spend ON spend.ad_entity_id = ents.id LEFT JOIN prev ON prev.ad_entity_id = ents.id LEFT JOIN counts ON counts.entity_id = ents.id
		WHERE $`+strconv.Itoa(idleArg)+`::bool OR COALESCE(spend.s, 0) > 0 OR ents.status = 'active' OR COALESCE(counts.payers, 0) > 0 OR COALESCE(counts.signups, 0) > 0
		ORDER BY COALESCE(spend.s, 0) DESC, lower(ents.name), ents.id`, args...)
	if err != nil {
		return nil, err
	}
	out := []PerformanceRow{}
	at := map[string]int{}
	for rows.Next() {
		var r PerformanceRow
		if err := rows.Scan(&r.ID, &r.Name, &r.ExternalID, &r.Status, &r.Connector, &r.AdAccountID, &r.AccountName, &r.CampaignID, &r.Currency,
			&r.SpendMicros, &r.Clicks, &r.Impressions, &r.PlatformConversions, &r.PlatformAllConversions, &r.PreviousSpendMicros, &r.Signups, &r.NewPayers, &r.payerMonths); err != nil {
			rows.Close()
			return nil, err
		}
		r.Level, r.WindowDays = p.Level, window
		r.NewRevenue, r.ReturningRevenue, r.LifetimeRevenue, r.ModelRevenue = []Money{}, []Money{}, []Money{}, []Money{}
		r.lifetime = map[string]int64{}
		at[r.ID] = len(out)
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	// Revenue by measure and currency, from the ledger as stored. Only
	// people an ad brought in count: a customer who came another way and
	// clicks an ad before a later payment adds no revenue to the campaign.
	returning := `SELECT a.entity_id, 'returning', r.currency, r.value_minor FROM ad_revenue r JOIN acq a ON a.id = r.acquisition_id
		WHERE r.bucket = 'returning' AND r.occurred_at >= $6 AND r.occurred_at < $7`
	if p.Level == LevelCampaign {
		returning = `SELECT r.touch_campaign_entity_id, 'returning', r.currency, r.value_minor FROM ad_revenue r
		WHERE r.brand_id = $2::uuid AND r.bucket = 'returning' AND r.acquisition_id IS NOT NULL AND r.touch_campaign_entity_id IN (SELECT id FROM ents)
		  AND r.occurred_at >= $6 AND r.occurred_at < $7`
	}
	rrows, err := q.Query(ctx, `WITH `+ents+`, `+stored+`
		SELECT x.entity_id::text, x.measure, x.currency, sum(x.value_minor)::bigint FROM (
			SELECT a.entity_id, 'new' AS measure, r.currency, r.value_minor FROM ad_revenue r JOIN acq a ON a.id = r.acquisition_id
			WHERE r.bucket = 'new' AND r.occurred_at >= $6 AND r.occurred_at < $7
			UNION ALL
			SELECT a.entity_id, 'lifetime', r.currency, r.value_minor FROM ad_revenue r JOIN acq a ON a.id = r.acquisition_id
			WHERE r.occurred_at < $7
			UNION ALL `+returning+`
		) x WHERE x.entity_id IN (SELECT id FROM ents)
		  -- Revenue reads the brand-zone bounds ($6, $7); this keeps the
		  -- date parameters, used by the spend query, typed.
		  AND $4::date <= $5::date
		GROUP BY 1, 2, 3`, rargs...)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var id, measure, cur string
		var v int64
		if err := rrows.Scan(&id, &measure, &cur, &v); err != nil {
			rrows.Close()
			return nil, err
		}
		i, ok := at[id]
		if !ok {
			continue
		}
		r := &out[i]
		switch measure {
		case "new":
			if p.Customers != "returning" {
				r.NewRevenue = append(r.NewRevenue, Money{cur, v})
			}
		case "returning":
			if p.Customers != "new" {
				r.ReturningRevenue = append(r.ReturningRevenue, Money{cur, v})
			}
		case "lifetime":
			r.LifetimeRevenue = append(r.LifetimeRevenue, Money{cur, v})
			r.lifetime[cur] = v
		}
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	if p.Model != "" {
		if err := modelCredit(ctx, q, p, ents, rargs, out, at); err != nil {
			return nil, err
		}
	}
	for i := range out {
		out[i].convert(p.Rates, p.Econ.Currency)
		out[i].judge(p.Econ)
	}
	return out, nil
}

// modelCredit fills the rows' numbers under an attribution model: for each
// conversion in the range, the share of its credit that went to visits
// whose ad ids name the entity. A sign-up adds its share to ModelSignups;
// a conversion with a value adds that share of its money to ModelRevenue.
// args are the main query's ($1 to $7 and the filters ents uses).
func modelCredit(ctx context.Context, q Querier, p PerformanceQuery, ents string, args []any, out []PerformanceRow, at map[string]int) error {
	args = append(append([]any{}, args...), p.Model, signupNames(ctx, q, p.WorkspaceID, p.BrandID), adKey[p.Level])
	n := len(args)
	model, names, key := "$"+strconv.Itoa(n-2), "$"+strconv.Itoa(n-1), "$"+strconv.Itoa(n)
	rows, err := q.Query(ctx, `WITH `+ents+`
		SELECT ent.id::text, v.currency,
			COALESCE(sum(c.weight) FILTER (WHERE v.name = ANY(`+names+`::text[])), 0)::float8,
			COALESCE(sum(c.weight * v.value_minor) FILTER (WHERE v.value_minor IS NOT NULL), 0)::float8,
			bool_or(v.value_minor IS NOT NULL)
		FROM attribution_credits c
		JOIN attribution_conversions v ON v.event_id = c.event_id
		JOIN site_sessions s ON s.id = c.session_id
		JOIN ents ent ON ent.external_id = s.ad->>`+key+`
		WHERE c.workspace_id = $1 AND c.model = `+model+`
		  AND COALESCE(v.brand_id, outflare_first_brand(v.workspace_id)) = $2::uuid
		  AND v.occurred_at >= $6 AND v.occurred_at < $7
		  -- The date parameters, used by the spend query, stay typed.
		  AND $4::date <= $5::date
		GROUP BY 1, 2`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for i := range out {
		zero := 0.0
		out[i].ModelSignups = &zero
	}
	for rows.Next() {
		var id, cur string
		var signups, minor float64
		var valued bool
		if err := rows.Scan(&id, &cur, &signups, &minor, &valued); err != nil {
			return err
		}
		i, ok := at[id]
		if !ok {
			continue
		}
		*out[i].ModelSignups += signups
		if valued && cur != "" {
			out[i].ModelRevenue = append(out[i].ModelRevenue, Money{cur, int64(math.Round(minor))})
		}
	}
	return rows.Err()
}

// judge works out the costs, the target status, payback and the trend.
func (r *PerformanceRow) judge(econ Economics) {
	if r.Signups > 0 {
		v := r.SpendMicros / int64(r.Signups)
		r.CostPerSignupMicros = &v
	}
	if r.NewPayers > 0 {
		v := r.SpendMicros / int64(r.NewPayers)
		r.CostPerNewPayerMicros = &v
	}
	r.TargetStatus = targetStatus(econ.TargetCostPerPayerMicros, r.SpendMicros, r.NewPayers)
	r.ROAS = roas(r.SpendMicros, r.Currency, r.NewRevenue, r.ReturningRevenue)
	if r.ModelSignups != nil {
		v := math.Round(*r.ModelSignups*100) / 100
		r.ModelSignups = &v
		if v > 0 {
			c := int64(math.Round(float64(r.SpendMicros) / v))
			r.ModelCostPerSignupMicros = &c
		}
		r.ModelROAS = roas(r.SpendMicros, r.Currency, r.ModelRevenue)
	}
	// Payback: what a new customer costs against what the people this
	// entity acquired pay per month, in the account's currency.
	if r.CostPerNewPayerMicros != nil && r.payerMonths > 0 {
		if life := r.lifetime[r.Currency]; life > 0 {
			monthly := int64(float64(life) / r.payerMonths * 10_000) // minor units to micros
			r.PaybackMonths = econ.PaybackMonthsFor(*r.CostPerNewPayerMicros, monthly)
		}
	}
	now, before := r.SpendMicros, r.PreviousSpendMicros
	switch {
	case now == 0 && before == 0:
		r.Trend = "idle"
	case before == 0:
		r.Trend = "new"
	case now == 0:
		r.Trend = "stopped"
	default:
		v := float64(now-before) / float64(before)
		r.SpendChange = &v
		r.Trend = "steady"
		if v > trendBand {
			r.Trend = "scaling_up"
		} else if v < -trendBand {
			r.Trend = "scaling_down"
		}
	}
	for _, m := range [][]Money{r.NewRevenue, r.ReturningRevenue, r.LifetimeRevenue, r.ModelRevenue} {
		sort.Slice(m, func(i, j int) bool { return m[i].Currency < m[j].Currency })
	}
}

// roas is revenue in currency over spend (micros of that currency): nil
// without spend, or when every amount is in another currency; zero with no
// revenue at all. Revenue is in minor units, and a minor unit is 10,000
// micros.
func roas(spendMicros int64, currency string, revenue ...[]Money) *float64 {
	if spendMicros <= 0 {
		return nil
	}
	var minor int64
	found, other := false, false
	for _, list := range revenue {
		for _, m := range list {
			if m.Currency == currency {
				minor += m.AmountMinor
				found = true
			} else {
				other = true
			}
		}
	}
	if !found && other {
		return nil
	}
	v := float64(minor) * 10_000 / float64(spendMicros)
	return &v
}

// signupNames are the event names that count as a sign-up for a brand:
// signed_up and its attribution conversion events, payments aside.
func signupNames(ctx context.Context, q Querier, ws, brand string) []string {
	names := []string{"signed_up"}
	var events []string
	if err := q.QueryRow(ctx, `SELECT conversion_events FROM attribution_settings WHERE workspace_id = $1 AND brand_id = $2`, ws, brand).Scan(&events); err != nil {
		return names
	}
	for _, e := range events {
		if e == "" || e == "signed_up" || e == "order_completed" || len(e) > 8 && e[:8] == "payment." {
			continue
		}
		names = append(names, e)
	}
	return names
}

// AcquisitionsQuery is one read of the people the ads acquired.
type AcquisitionsQuery struct {
	WorkspaceID, BrandID string
	// From and To bound clicked_at (dates, inclusive, in Location, UTC when
	// nil); either may be zero.
	From, To   time.Time
	Location   *time.Location
	CampaignID string
	// Customers: all, new (people who paid) or returning (people who paid
	// more than once).
	Customers string
	// Before pages: rows clicked before this time.
	Before *time.Time
	Limit  int
}

// Acquisition is one person the ads acquired.
type Acquisition struct {
	ID          string     `json:"id"`
	RecordID    string     `json:"record_id"`
	RecordName  string     `json:"record_name"`
	Connector   string     `json:"connector"`
	AdAccountID *string    `json:"ad_account_id"`
	Campaign    EntityRef  `json:"campaign"`
	AdGroup     EntityRef  `json:"ad_group"`
	Ad          EntityRef  `json:"ad"`
	Keyword     string     `json:"keyword"`
	ClickIDKind string     `json:"click_id_kind"`
	MatchedBy   string     `json:"matched_by"`
	ClickedAt   time.Time  `json:"clicked_at"`
	SignedUpAt  *time.Time `json:"signed_up_at"`
	FirstPaidAt *time.Time `json:"first_paid_at"`
	Payments    int        `json:"payments"`
	Revenue     []Money    `json:"revenue_to_date"`
	Bucket      string     `json:"bucket"`
	AdConsent   bool       `json:"ad_consent"`
	WindowDays  int        `json:"window_days"`
	CreatedAt   time.Time  `json:"created_at"`
	revenueJSON map[string]int64
}

// EntityRef names a campaign, ad group or ad: Outflare's id when it is
// known, and the network's own id as the click carried it.
type EntityRef struct {
	ID         *string `json:"id"`
	Name       string  `json:"name"`
	ExternalID string  `json:"external_id"`
}

// Acquisitions lists the people a brand's ads acquired, newest click
// first.
func Acquisitions(ctx context.Context, q Querier, p AcquisitionsQuery) ([]Acquisition, error) {
	args := []any{p.WorkspaceID, p.BrandID}
	where := ``
	if !p.From.IsZero() {
		args = append(args, DayStart(p.From, p.Location))
		where += ` AND q.clicked_at >= $` + strconv.Itoa(len(args))
	}
	if !p.To.IsZero() {
		args = append(args, DayStart(p.To.AddDate(0, 0, 1), p.Location))
		where += ` AND q.clicked_at < $` + strconv.Itoa(len(args))
	}
	if p.CampaignID != "" {
		args = append(args, p.CampaignID)
		where += ` AND q.campaign_entity_id = $` + strconv.Itoa(len(args))
	}
	switch p.Customers {
	case "new":
		where += ` AND q.first_paid_at IS NOT NULL`
	case "returning":
		where += ` AND (SELECT count(*) FROM ad_revenue v WHERE v.acquisition_id = q.id AND v.value_minor > 0) > 1`
	}
	if p.Before != nil {
		args = append(args, *p.Before)
		where += ` AND q.clicked_at < $` + strconv.Itoa(len(args))
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := q.Query(ctx, `
		SELECT q.id::text, q.record_id::text, COALESCE(r.name, ''), q.connector, q.ad_account_id::text,
			q.campaign_entity_id::text, COALESCE(c.name, ''), q.campaign_external_id,
			q.ad_group_entity_id::text, COALESCE(g.name, ''), q.ad_group_external_id,
			q.ad_entity_id::text, COALESCE(d.name, ''), q.ad_external_id,
			q.keyword, q.click_id_kind, q.matched_by, q.clicked_at, q.signed_up_at, q.first_paid_at, q.ad_consent, q.window_days, q.created_at,
			(SELECT count(*) FROM ad_revenue v WHERE v.acquisition_id = q.id AND v.value_minor > 0)::int,
			(SELECT COALESCE(jsonb_object_agg(t.cur, t.s), '{}') FROM (SELECT v.currency AS cur, sum(v.value_minor) AS s FROM ad_revenue v WHERE v.acquisition_id = q.id GROUP BY 1) t)
		FROM ad_acquisitions q LEFT JOIN records r ON r.id = q.record_id
		LEFT JOIN ad_entities c ON c.id = q.campaign_entity_id LEFT JOIN ad_entities g ON g.id = q.ad_group_entity_id LEFT JOIN ad_entities d ON d.id = q.ad_entity_id
		WHERE q.workspace_id = $1 AND q.brand_id = $2::uuid`+where+`
		ORDER BY q.clicked_at DESC, q.id DESC LIMIT $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Acquisition, error) {
		var a Acquisition
		var rev []byte
		err := row.Scan(&a.ID, &a.RecordID, &a.RecordName, &a.Connector, &a.AdAccountID,
			&a.Campaign.ID, &a.Campaign.Name, &a.Campaign.ExternalID, &a.AdGroup.ID, &a.AdGroup.Name, &a.AdGroup.ExternalID,
			&a.Ad.ID, &a.Ad.Name, &a.Ad.ExternalID, &a.Keyword, &a.ClickIDKind, &a.MatchedBy, &a.ClickedAt, &a.SignedUpAt, &a.FirstPaidAt,
			&a.AdConsent, &a.WindowDays, &a.CreatedAt, &a.Payments, &rev)
		if err != nil {
			return a, err
		}
		_ = json.Unmarshal(rev, &a.revenueJSON)
		a.Revenue = []Money{}
		for cur, v := range a.revenueJSON {
			a.Revenue = append(a.Revenue, Money{cur, v})
		}
		sort.Slice(a.Revenue, func(i, j int) bool { return a.Revenue[i].Currency < a.Revenue[j].Currency })
		a.Bucket = "signup"
		if a.FirstPaidAt != nil {
			a.Bucket = "payer"
		}
		return a, nil
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []Acquisition{}
	}
	return out, nil
}

// ErrNotFound is for a record the brand doesn't have.
var ErrNotFound = errors.New("not found")

// DayStart is when the date day (as parsed, midnight UTC) begins in loc,
// UTC when nil.
func DayStart(day time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, loc)
}

// PerformanceDay is one day of the brand's campaigns in one currency: what
// they spent and the sign-ups and new paying customers they brought.
type PerformanceDay struct {
	Day         string `json:"day"`
	Currency    string `json:"currency"`
	SpendMicros int64  `json:"spend_micros"`
	Signups     int    `json:"signups"`
	NewPayers   int    `json:"new_payers"`
	// What the networks reported that day, and the revenue the campaigns'
	// customers paid that day (new and returning, as Customers narrows
	// it), in micros of Currency. Revenue in a currency that can't be
	// turned into Currency is left out.
	Clicks              int64   `json:"clicks"`
	Impressions         int64   `json:"impressions"`
	PlatformConversions float64 `json:"platform_conversions"`
	RevenueMicros       int64   `json:"revenue_micros"`
}

// PerformanceDaily is Performance at the campaign level per day: the same
// campaigns, filters and day boundaries, so its days add up to the rows'
// totals. It reads the ledger as stored (WindowDays is not applied). Days
// with nothing are absent.
func PerformanceDaily(ctx context.Context, q Querier, p PerformanceQuery) ([]PerformanceDay, error) {
	zone := "UTC"
	if p.Location != nil {
		zone = p.Location.String()
	}
	args := []any{p.WorkspaceID, p.BrandID, p.From.UTC().Format(time.DateOnly), p.To.UTC().Format(time.DateOnly),
		DayStart(p.From, p.Location), DayStart(p.To.AddDate(0, 0, 1), p.Location), zone}
	where := ""
	if p.Connector != "" {
		args = append(args, p.Connector)
		where += ` AND a.connector = $` + strconv.Itoa(len(args))
	}
	if p.AccountID != "" {
		args = append(args, p.AccountID)
		where += ` AND a.id = $` + strconv.Itoa(len(args))
	}
	if p.CampaignID != "" {
		args = append(args, p.CampaignID)
		where += ` AND e.id = $` + strconv.Itoa(len(args))
	}
	rows, err := q.Query(ctx, `WITH ents AS (
			SELECT e.id, a.currency FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2::uuid AND e.level = 'campaign'`+where+`)
		SELECT to_char(x.day, 'YYYY-MM-DD'), x.currency, sum(x.spend)::bigint, sum(x.signups)::int, sum(x.payers)::int,
			sum(x.clicks)::bigint, sum(x.impressions)::bigint, sum(x.conv)::float8 FROM (
			SELECT m.day, ents.currency, m.spend_micros AS spend, 0 AS signups, 0 AS payers, m.clicks, m.impressions, COALESCE(m.conversions, 0) AS conv
			FROM ad_metrics_daily m JOIN ents ON ents.id = m.ad_entity_id
			WHERE m.workspace_id = $1 AND m.day BETWEEN $3::date AND $4::date
			UNION ALL
			SELECT (q.signed_up_at AT TIME ZONE $7)::date, ents.currency, 0, 1, 0, 0, 0, 0
			FROM ad_acquisitions q JOIN ents ON ents.id = q.campaign_entity_id
			WHERE q.brand_id = $2::uuid AND q.signed_up_at >= $5 AND q.signed_up_at < $6
			UNION ALL
			SELECT (q.first_paid_at AT TIME ZONE $7)::date, ents.currency, 0, 0, 1, 0, 0, 0
			FROM ad_acquisitions q JOIN ents ON ents.id = q.campaign_entity_id
			WHERE q.brand_id = $2::uuid AND q.first_paid_at >= $5 AND q.first_paid_at < $6
		) x GROUP BY 1, 2 HAVING sum(x.spend) > 0 OR sum(x.signups) > 0 OR sum(x.payers) > 0 ORDER BY 1, 2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PerformanceDay{}
	for rows.Next() {
		var d PerformanceDay
		if err := rows.Scan(&d.Day, &d.Currency, &d.SpendMicros, &d.Signups, &d.NewPayers, &d.Clicks, &d.Impressions, &d.PlatformConversions); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The revenue those campaigns' customers paid per day: first payments
	// by the campaign that brought the customer, later ones by the last
	// campaign they clicked, as the rows count it. A day with revenue and
	// nothing else gets a row of its own.
	buckets := []string{"new", "returning"}
	switch p.Customers {
	case "new", "returning":
		buckets = []string{p.Customers}
	}
	rrows, err := q.Query(ctx, `WITH ents AS (
			SELECT e.id, a.currency FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id
			WHERE e.workspace_id = $1 AND COALESCE(e.brand_id, a.brand_id) = $2::uuid AND e.level = 'campaign'`+where+`)
		SELECT to_char(x.day, 'YYYY-MM-DD'), x.currency, x.paid_in, sum(x.value_minor)::bigint FROM (
			SELECT (r.occurred_at AT TIME ZONE $7)::date AS day, ents.currency, r.currency AS paid_in, r.value_minor, r.bucket
			FROM ad_revenue r JOIN ad_acquisitions q ON q.id = r.acquisition_id JOIN ents ON ents.id = q.campaign_entity_id
			WHERE r.brand_id = $2::uuid AND r.bucket = 'new' AND r.occurred_at >= $5 AND r.occurred_at < $6
			UNION ALL
			SELECT (r.occurred_at AT TIME ZONE $7)::date, ents.currency, r.currency, r.value_minor, r.bucket
			FROM ad_revenue r JOIN ents ON ents.id = r.touch_campaign_entity_id
			WHERE r.brand_id = $2::uuid AND r.bucket = 'returning' AND r.acquisition_id IS NOT NULL AND r.occurred_at >= $5 AND r.occurred_at < $6
		) x WHERE x.bucket = ANY($`+strconv.Itoa(len(args)+1)+`::text[]) AND $3::date <= $4::date GROUP BY 1, 2, 3 ORDER BY 1, 2, 3`, append(args, buckets)...)
	if err != nil {
		return nil, err
	}
	defer rrows.Close()
	index := map[string]int{}
	for i, d := range out {
		index[d.Day+" "+d.Currency] = i
	}
	for rrows.Next() {
		var day, currency, paidIn string
		var minor int64
		if err := rrows.Scan(&day, &currency, &paidIn, &minor); err != nil {
			return nil, err
		}
		micros := int64(math.Round(float64(minor) / math.Pow10(fxrates.MinorDigits(paidIn)) * 1e6))
		if paidIn != currency {
			v, ok := p.Rates.Micros(micros, paidIn, currency)
			if !ok {
				continue
			}
			micros = v
		}
		i, ok := index[day+" "+currency]
		if !ok {
			i = len(out)
			index[day+" "+currency] = i
			out = append(out, PerformanceDay{Day: day, Currency: currency})
		}
		out[i].RevenueMicros += micros
	}
	if err := rrows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(a, b int) bool {
		if out[a].Day != out[b].Day {
			return out[a].Day < out[b].Day
		}
		return out[a].Currency < out[b].Currency
	})
	if p.Rates == nil {
		return out, nil
	}
	// In the brand's currency, a day is one row.
	merged, at := []PerformanceDay{}, map[string]int{}
	for _, d := range out {
		if v, ok := p.Rates.Micros(d.SpendMicros, d.Currency, p.Econ.Currency); ok {
			rev, _ := p.Rates.Micros(d.RevenueMicros, d.Currency, p.Econ.Currency)
			d.SpendMicros, d.RevenueMicros, d.Currency = v, rev, p.Econ.Currency
		}
		key := d.Day + " " + d.Currency
		if i, ok := at[key]; ok {
			merged[i].SpendMicros += d.SpendMicros
			merged[i].Signups += d.Signups
			merged[i].NewPayers += d.NewPayers
			merged[i].Clicks += d.Clicks
			merged[i].Impressions += d.Impressions
			merged[i].PlatformConversions += d.PlatformConversions
			merged[i].RevenueMicros += d.RevenueMicros
			continue
		}
		at[key] = len(merged)
		merged = append(merged, d)
	}
	return merged, nil
}
