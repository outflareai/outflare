// Package paidads is the network-neutral core of the paid ads intelligence
// layer (plans/paid-ads/plan.md): the brand's economics, the lever
// catalogue and what each network can do, the ledger that ties sign-ups
// and payments to the ad click that brought them, and the queries the
// rules and reports read. It scores everything in one unit, cost per new
// paying customer on Outflare's own attribution, so the network adapters
// (engine/internal/connectors/ads) only translate levers into calls.
package paidads

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// Defaults for a brand that has not set its economics yet.
const (
	DefaultWindowDays    = 30
	DefaultPaybackMonths = 12
	// MaxReadWindowDays is the widest window a report recomputes at read
	// time (GET /performance?window_days=).
	MaxReadWindowDays = 90
)

// Economics is a brand's ad_brand_economics row: the two targets, the
// acquisition window, payback and margin, and shadow mode.
type Economics struct {
	BrandID     string `json:"brand_id"`
	WorkspaceID string `json:"-"`
	Currency    string `json:"currency"`
	// The owner's two numbers, micros of Currency. Nil: not set yet.
	TargetCostPerPayerMicros  *int64 `json:"target_cost_per_payer_micros"`
	TargetCostPerSignupMicros *int64 `json:"target_cost_per_signup_micros"`
	// Days after an ad click a sign-up or first payment still counts for it.
	AcquisitionWindowDays int `json:"acquisition_window_days"`
	// Months a new customer's revenue is expected to take to pay back its
	// cost, and the brand's gross margin on that revenue.
	PaybackMonths      int  `json:"payback_months"`
	GrossMarginPercent *int `json:"gross_margin_percent"`
	// Rules only record what they would do until this is off.
	ShadowMode bool `json:"shadow_mode"`
	// Set reports whether the brand has saved its economics; a brand that
	// hasn't reads the defaults.
	Set       bool      `json:"set"`
	UpdatedBy *string   `json:"updated_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Defaults are a brand's economics before it sets them: the brand's
// currency, a 30-day window, 12 months' payback, no targets, shadow on.
func Defaults(ws, brand, currency string) Economics {
	if currency == "" {
		currency = "USD"
	}
	now := time.Now().UTC()
	return Economics{BrandID: brand, WorkspaceID: ws, Currency: currency, AcquisitionWindowDays: DefaultWindowDays,
		PaybackMonths: DefaultPaybackMonths, ShadowMode: true, CreatedAt: now, UpdatedAt: now}
}

// Load reads a brand's economics, or its defaults (Set false) when it has
// none yet. The brand must be the workspace's, or ErrNoBrand.
func Load(ctx context.Context, q Querier, ws, brand string) (Economics, error) {
	var e Economics
	var currency string
	var brandCurrency string
	var set bool
	err := q.QueryRow(ctx, `
		SELECT b.currency, x.brand_id IS NOT NULL, COALESCE(x.currency, b.currency), x.target_cost_per_payer_micros, x.target_cost_per_signup_micros,
			COALESCE(x.acquisition_window_days, $3), COALESCE(x.payback_months, $4), x.gross_margin_percent,
			COALESCE(x.shadow_mode, true), x.updated_by::text, COALESCE(x.created_at, b.created_at), COALESCE(x.updated_at, b.created_at)
		FROM brands b LEFT JOIN ad_brand_economics x ON x.brand_id = b.id
		WHERE b.workspace_id = $1 AND b.id = $2`, ws, brand, DefaultWindowDays, DefaultPaybackMonths).
		Scan(&brandCurrency, &set, &currency, &e.TargetCostPerPayerMicros, &e.TargetCostPerSignupMicros, &e.AcquisitionWindowDays, &e.PaybackMonths,
			&e.GrossMarginPercent, &e.ShadowMode, &e.UpdatedBy, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNoBrand
	}
	if err != nil {
		return e, err
	}
	e.BrandID, e.WorkspaceID, e.Currency, e.Set = brand, ws, currency, set
	if e.Currency == "" {
		e.Currency = "USD"
	}
	return e, nil
}

// ErrNoBrand is Load's answer for a brand the workspace doesn't have.
var ErrNoBrand = errors.New("brand not found")

// Save writes a brand's economics (insert or update) and returns the row
// as stored. updatedBy is the actor, or nil for the system.
func Save(ctx context.Context, q Querier, e Economics, updatedBy *string) (Economics, error) {
	if e.AcquisitionWindowDays == 0 {
		e.AcquisitionWindowDays = DefaultWindowDays
	}
	if e.PaybackMonths == 0 {
		e.PaybackMonths = DefaultPaybackMonths
	}
	if e.Currency == "" {
		e.Currency = "USD"
	}
	_, err := q.Exec(ctx, `
		INSERT INTO ad_brand_economics (brand_id, workspace_id, currency, target_cost_per_payer_micros, target_cost_per_signup_micros,
			acquisition_window_days, payback_months, gross_margin_percent, shadow_mode, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (brand_id) DO UPDATE SET currency = EXCLUDED.currency, target_cost_per_payer_micros = EXCLUDED.target_cost_per_payer_micros,
			target_cost_per_signup_micros = EXCLUDED.target_cost_per_signup_micros, acquisition_window_days = EXCLUDED.acquisition_window_days,
			payback_months = EXCLUDED.payback_months, gross_margin_percent = EXCLUDED.gross_margin_percent,
			shadow_mode = EXCLUDED.shadow_mode, updated_by = EXCLUDED.updated_by, updated_at = now()
		WHERE ad_brand_economics.workspace_id = EXCLUDED.workspace_id`,
		e.BrandID, e.WorkspaceID, e.Currency, e.TargetCostPerPayerMicros, e.TargetCostPerSignupMicros, e.AcquisitionWindowDays, e.PaybackMonths,
		e.GrossMarginPercent, e.ShadowMode, updatedBy)
	if err != nil {
		return e, err
	}
	return Load(ctx, q, e.WorkspaceID, e.BrandID)
}

// HasTarget reports whether the brand judges in payers: it has a target
// cost per new paying customer. Without one, rules fall back to platform
// conversions and say so.
func (e Economics) HasTarget() bool { return e.TargetCostPerPayerMicros != nil }

// Window is the brand's acquisition window as a duration.
func (e Economics) Window() time.Duration {
	return time.Duration(e.AcquisitionWindowDays) * 24 * time.Hour
}

// BreakEvenCostPerPayerMicros is the most a new paying customer may cost
// for their revenue to pay it back within PaybackMonths at the brand's
// gross margin: monthly revenue per payer, times the margin, times the
// months. Nil without a margin, or without revenue per payer yet. It is
// what the target defaults to when the owner gives only the margin and
// payback.
func (e Economics) BreakEvenCostPerPayerMicros(monthlyRevenuePerPayerMicros int64) *int64 {
	if e.GrossMarginPercent == nil || monthlyRevenuePerPayerMicros <= 0 || e.PaybackMonths <= 0 {
		return nil
	}
	v := int64(math.Round(float64(monthlyRevenuePerPayerMicros) * float64(*e.GrossMarginPercent) / 100 * float64(e.PaybackMonths)))
	return &v
}

// PaybackMonthsFor is how many months a new customer costing costMicros
// takes to pay back at monthly revenue per payer and the brand's margin
// (every percent of margin when none is set). Nil without revenue.
func (e Economics) PaybackMonthsFor(costMicros, monthlyRevenuePerPayerMicros int64) *float64 {
	if monthlyRevenuePerPayerMicros <= 0 || costMicros <= 0 {
		return nil
	}
	margin := 1.0
	if e.GrossMarginPercent != nil {
		margin = float64(*e.GrossMarginPercent) / 100
	}
	v := float64(costMicros) / (float64(monthlyRevenuePerPayerMicros) * margin)
	v = math.Round(v*10) / 10
	return &v
}

// TargetStatus compares a cost per payer with the brand's target: on_target
// within a tenth either way, under below that, over above it (and over
// when money was spent and nobody paid), no_target without a target and
// no_spend with nothing spent.
func (e Economics) TargetStatus(spendMicros int64, payers int) string {
	return targetStatus(e.TargetCostPerPayerMicros, spendMicros, payers)
}

// targetBand is how far a cost may sit from its target and still count as
// on target: a tenth either way, as the campaigns list judges.
const targetBand = 0.10

func targetStatus(target *int64, spendMicros int64, n int) string {
	switch {
	case target == nil:
		return "no_target"
	case spendMicros == 0:
		return "no_spend"
	case n == 0:
		return "over"
	}
	cost := float64(spendMicros) / float64(n)
	switch {
	case cost > float64(*target)*(1+targetBand):
		return "over"
	case cost < float64(*target)*(1-targetBand):
		return "under"
	}
	return "on_target"
}
