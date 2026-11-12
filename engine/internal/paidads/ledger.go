package paidads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
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
	// From and To are dates (UTC), inclusive.
	From, To time.Time
	// Filters, each optional.
	Connector, AccountID, CampaignID string
	// WindowDays recomputes acquisitions from visits at read time with this
	// window instead of the stored one; 0 reads the ledger as stored.
	WindowDays int
	// Customers keeps the revenue to new, returning or all customers.
	Customers string
	// Econ is the brand's economics, for the target and the payback maths.
	Econ Economics
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
	Currency    string  `json:"currency"`
	// Spend, clicks, impressions and the platform's own conversions, in the
	// account's currency.
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
	// Revenue in the range from first payments (new), from later payments
	// whose last paid click within the window was this entity's (returning;
	// for ad groups and ads, later payments by the people they acquired),
	// and every payment to date by the people it acquired (lifetime).
	NewRevenue       []Money `json:"new_revenue"`
	ReturningRevenue []Money `json:"returning_revenue"`
	LifetimeRevenue  []Money `json:"lifetime_revenue"`
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
	args := []any{p.WorkspaceID, p.BrandID, p.Level, from, to}
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
					  AND e.occurred_at >= $4::date AND e.occurred_at < $5::date + 1
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
	rows, err := q.Query(ctx, `WITH `+ents+`, `+acq+`,
		spend AS (SELECT m.ad_entity_id, sum(m.spend_micros)::bigint s, sum(m.clicks)::bigint c, sum(m.impressions)::bigint i, sum(m.conversions)::float8 cv,
			sum(m.all_conversions)::float8 acv
			FROM ad_metrics_daily m WHERE m.workspace_id = $1 AND m.ad_entity_id IN (SELECT id FROM ents) AND m.day BETWEEN $4::date AND $5::date GROUP BY 1),
		prev AS (SELECT m.ad_entity_id, sum(m.spend_micros)::bigint s
			FROM ad_metrics_daily m WHERE m.workspace_id = $1 AND m.ad_entity_id IN (SELECT id FROM ents) AND m.day BETWEEN $`+pf+`::date AND $`+pt+`::date GROUP BY 1),
		counts AS (SELECT a.entity_id,
				count(*) FILTER (WHERE a.signed_up_at >= $4::date AND a.signed_up_at < $5::date + 1)::int AS signups,
				count(*) FILTER (WHERE a.first_paid_at >= $4::date AND a.first_paid_at < $5::date + 1)::int AS payers,
				COALESCE(sum(GREATEST(1, extract(epoch from now() - a.first_paid_at) / 2629800)) FILTER (WHERE a.first_paid_at IS NOT NULL AND a.first_paid_at < $5::date + 1), 0)::float8 AS payer_months
			FROM acq a GROUP BY 1)
		SELECT ents.id::text, ents.name, ents.external_id, ents.status, ents.connector, ents.account_id::text, ents.account_name, ents.campaign_id::text, ents.currency,
			COALESCE(spend.s, 0), COALESCE(spend.c, 0), COALESCE(spend.i, 0), spend.cv, spend.acv, COALESCE(prev.s, 0),
			COALESCE(counts.signups, 0), COALESCE(counts.payers, 0), COALESCE(counts.payer_months, 0)
		FROM ents LEFT JOIN spend ON spend.ad_entity_id = ents.id LEFT JOIN prev ON prev.ad_entity_id = ents.id LEFT JOIN counts ON counts.entity_id = ents.id
		WHERE COALESCE(spend.s, 0) > 0 OR ents.status = 'active' OR COALESCE(counts.payers, 0) > 0 OR COALESCE(counts.signups, 0) > 0
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
		r.NewRevenue, r.ReturningRevenue, r.LifetimeRevenue = []Money{}, []Money{}, []Money{}
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
	// Revenue by measure and currency, from the ledger as stored.
	returning := `SELECT a.entity_id, 'returning', r.currency, r.value_minor FROM ad_revenue r JOIN acq a ON a.id = r.acquisition_id
		WHERE r.bucket = 'returning' AND r.occurred_at >= $4::date AND r.occurred_at < $5::date + 1`
	if p.Level == LevelCampaign {
		returning = `SELECT r.touch_campaign_entity_id, 'returning', r.currency, r.value_minor FROM ad_revenue r
		WHERE r.brand_id = $2::uuid AND r.bucket = 'returning' AND r.touch_campaign_entity_id IN (SELECT id FROM ents)
		  AND r.occurred_at >= $4::date AND r.occurred_at < $5::date + 1`
	}
	rrows, err := q.Query(ctx, `WITH `+ents+`, `+stored+`
		SELECT x.entity_id::text, x.measure, x.currency, sum(x.value_minor)::bigint FROM (
			SELECT a.entity_id, 'new' AS measure, r.currency, r.value_minor FROM ad_revenue r JOIN acq a ON a.id = r.acquisition_id
			WHERE r.bucket = 'new' AND r.occurred_at >= $4::date AND r.occurred_at < $5::date + 1
			UNION ALL
			SELECT a.entity_id, 'lifetime', r.currency, r.value_minor FROM ad_revenue r JOIN acq a ON a.id = r.acquisition_id
			WHERE r.occurred_at < $5::date + 1
			UNION ALL `+returning+`
		) x WHERE x.entity_id IN (SELECT id FROM ents) GROUP BY 1, 2, 3`, rargs...)
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
	for i := range out {
		out[i].judge(p.Econ)
	}
	return out, nil
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
	for _, m := range [][]Money{r.NewRevenue, r.ReturningRevenue, r.LifetimeRevenue} {
		sort.Slice(m, func(i, j int) bool { return m[i].Currency < m[j].Currency })
	}
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
	// From and To bound clicked_at (dates, inclusive); either may be zero.
	From, To   time.Time
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
		args = append(args, p.From.UTC().Format(time.DateOnly))
		where += ` AND q.clicked_at >= $` + strconv.Itoa(len(args)) + `::date`
	}
	if !p.To.IsZero() {
		args = append(args, p.To.UTC().Format(time.DateOnly))
		where += ` AND q.clicked_at < $` + strconv.Itoa(len(args)) + `::date + 1`
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
