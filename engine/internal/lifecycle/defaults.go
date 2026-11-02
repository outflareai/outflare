// Package lifecycle moves companies and people through stages per product
// (lead, trial, active, at risk, churned), emits an event for each move,
// and keeps a company's overall status across its products
// (plans/customer-data-platform/README.md, step 4; docs/product-lifecycle.md).
//
// Billing stages live on the company and usage stages on each person; a
// person with no company carries the billing stages too. Stages start
// empty when a brand turns them on (greenfield): events from before then
// never move anyone.
package lifecycle

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/journeys"
)

// Levels.
const (
	LevelCompany = "company" // billing stages: on the company, or a person with none
	LevelPerson  = "person"  // usage stages: on each person
	LevelOverall = "overall" // a company's status across products (transitions only)
)

// Stage categories: what a stage means to the engine.
const (
	CatLead    = "lead"
	CatTrial   = "trial"
	CatActive  = "active"
	CatAtRisk  = "at_risk"
	CatChurned = "churned"
)

// Move kinds.
const (
	KindEvent      = "event"      // a named event, with conditions
	KindUsage      = "usage"      // any event the product sent about itself
	KindInactivity = "inactivity" // no usage for the product's at_risk_days or churn_days
)

// None is the from stage of someone with no stage yet.
const None = "none"

// Stage is one of a brand's stages.
type Stage struct {
	Level    string `json:"level"`
	Key      string `json:"key"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Rank     int    `json:"rank"`
}

// Move is what moves someone from stages to a stage.
type Move struct {
	Level      string               `json:"level"`
	Key        string               `json:"key"`
	Name       string               `json:"name"`
	Kind       string               `json:"kind"`
	Event      string               `json:"event,omitempty"`
	After      string               `json:"after,omitempty"`
	Conditions []journeys.Condition `json:"conditions"`
	FromStages []string             `json:"from_stages"`
	AnyStage   bool                 `json:"any_stage"`
	ToStage    string               `json:"to_stage"`
	ChurnMode  string               `json:"churn_mode,omitempty"`
	Emit       string               `json:"emit_event"`
	Position   int                  `json:"position"`
}

func status(v string) journeys.Condition {
	return journeys.Condition{Property: "status", Op: "eq", Value: v}
}

// DefaultStages are the stages every brand starts with. A brand can
// rename, add and remove them.
var DefaultStages = []Stage{
	{LevelCompany, "lead", "Lead", CatLead, 0},
	{LevelCompany, "churned", "Churned", CatChurned, 5},
	{LevelCompany, "trial", "Trial", CatTrial, 20},
	{LevelCompany, "at_risk", "At risk", CatAtRisk, 30},
	{LevelCompany, "active", "Active", CatActive, 40},
	{LevelPerson, "invited", "Invited", CatLead, 0},
	{LevelPerson, "inactive", "Inactive", CatChurned, 5},
	{LevelPerson, "at_risk", "At risk", CatAtRisk, 30},
	{LevelPerson, "activated", "Activated", CatActive, 40},
}

// DefaultMoves are the moves every brand starts with, in the order they
// are tried: the first that fits an event wins.
var DefaultMoves = []Move{
	// Billing, on the company.
	{Level: LevelCompany, Key: "trial_started", Name: "Trial started", Kind: KindEvent, Event: "trial.started",
		FromStages: []string{None, "lead"}, ToStage: "trial", Emit: "product.trial_started"},
	{Level: LevelCompany, Key: "trial_subscription", Name: "Trial subscription started", Kind: KindEvent, Event: "subscription.started",
		Conditions: []journeys.Condition{status("trialing")}, FromStages: []string{None, "lead"}, ToStage: "trial", Emit: "product.trial_started"},
	{Level: LevelCompany, Key: "activated", Name: "Activated (first payment)", Kind: KindEvent, Event: "payment.succeeded",
		FromStages: []string{None, "lead", "trial"}, ToStage: "active", Emit: "product.activated"},
	{Level: LevelCompany, Key: "activated_subscription", Name: "Activated (subscription)", Kind: KindEvent, Event: "subscription.started",
		Conditions: []journeys.Condition{status("active")}, FromStages: []string{None, "lead", "trial"}, ToStage: "active", Emit: "product.activated"},
	{Level: LevelCompany, Key: "trial_converted", Name: "Trial converted", Kind: KindEvent, Event: "subscription.changed",
		Conditions: []journeys.Condition{status("active")}, FromStages: []string{"trial"}, ToStage: "active", Emit: "product.activated"},
	{Level: LevelCompany, Key: "reactivated", Name: "Reactivated (payment)", Kind: KindEvent, Event: "payment.succeeded",
		FromStages: []string{"churned"}, ToStage: "active", Emit: "product.reactivated"},
	{Level: LevelCompany, Key: "reactivated_subscription", Name: "Reactivated (subscription)", Kind: KindEvent, Event: "subscription.started",
		Conditions: []journeys.Condition{status("active")}, FromStages: []string{"churned"}, ToStage: "active", Emit: "product.reactivated"},
	{Level: LevelCompany, Key: "trial_expired", Name: "Trial expired", Kind: KindEvent, Event: "trial.ended",
		FromStages: []string{"trial"}, ToStage: "churned", Emit: "product.trial_expired"},
	{Level: LevelCompany, Key: "churned", Name: "Churned (cancelled)", Kind: KindEvent, Event: "subscription.canceled",
		FromStages: []string{"trial", "active", "at_risk"}, ToStage: "churned", ChurnMode: "cancellation", Emit: "product.churned"},
	{Level: LevelCompany, Key: "recovered", Name: "Recovered", Kind: KindUsage,
		FromStages: []string{"at_risk"}, ToStage: "active", Emit: "product.recovered"},
	{Level: LevelCompany, Key: "reactivated_usage", Name: "Reactivated (usage)", Kind: KindUsage,
		FromStages: []string{"churned"}, ToStage: "active", ChurnMode: "inactivity", Emit: "product.reactivated"},
	{Level: LevelCompany, Key: "at_risk", Name: "At risk (no usage)", Kind: KindInactivity, After: "at_risk_days",
		FromStages: []string{"active"}, ToStage: "at_risk", Emit: "product.at_risk"},
	{Level: LevelCompany, Key: "churned_inactive", Name: "Churned (no usage)", Kind: KindInactivity, After: "churn_days",
		FromStages: []string{"active", "at_risk"}, ToStage: "churned", ChurnMode: "inactivity", Emit: "product.churned"},
	// Usage, on each person.
	{Level: LevelPerson, Key: "invited", Name: "Invited", Kind: KindEvent, Event: "user.invited",
		FromStages: []string{None}, ToStage: "invited", Emit: "product.user_invited"},
	{Level: LevelPerson, Key: "activated", Name: "Activated", Kind: KindUsage,
		FromStages: []string{None, "invited"}, ToStage: "activated", Emit: "product.user_activated"},
	{Level: LevelPerson, Key: "recovered", Name: "Recovered", Kind: KindUsage,
		FromStages: []string{"at_risk", "inactive"}, ToStage: "activated", Emit: "product.user_recovered"},
	{Level: LevelPerson, Key: "at_risk", Name: "At risk (no usage)", Kind: KindInactivity, After: "at_risk_days",
		FromStages: []string{"activated"}, ToStage: "at_risk", Emit: "product.user_at_risk"},
	{Level: LevelPerson, Key: "inactive", Name: "Inactive (no usage)", Kind: KindInactivity, After: "churn_days",
		FromStages: []string{"activated", "at_risk"}, ToStage: "inactive", Emit: "product.user_inactive"},
}

// Overall status events, emitted on the company.
const (
	EvBecameCustomer = "customer.became_customer"
	EvPartlyChurned  = "customer.partly_churned"
	EvFullyChurned   = "customer.fully_churned"
	EvExpanded       = "customer.expanded"
)

// Overall status flags.
const (
	FlagPartlyChurned    = "partly_churned"
	FlagTriallingAnother = "trialling_another"
)

// Seed writes the default stages and moves for a level of a brand that
// has none yet.
func Seed(ctx context.Context, tx pgx.Tx, ws, brand string) error {
	for _, level := range []string{LevelCompany, LevelPerson} {
		var has bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM lifecycle_stages WHERE brand_id = $1 AND level = $2)`, brand, level).Scan(&has); err != nil {
			return err
		}
		if has {
			continue
		}
		for _, s := range DefaultStages {
			if s.Level != level {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO lifecycle_stages (workspace_id, brand_id, level, key, name, category, rank)
				VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT DO NOTHING`, ws, brand, s.Level, s.Key, s.Name, s.Category, s.Rank); err != nil {
				return err
			}
		}
		for i, m := range DefaultMoves {
			if m.Level != level {
				continue
			}
			m.Position = i
			if err := InsertMove(ctx, tx, ws, brand, m); err != nil {
				return err
			}
		}
	}
	return nil
}

// InsertMove stores a move.
func InsertMove(ctx context.Context, tx pgx.Tx, ws, brand string, m Move) error {
	conds := m.Conditions
	if conds == nil {
		conds = []journeys.Condition{}
	}
	raw, _ := json.Marshal(conds)
	from := m.FromStages
	if from == nil {
		from = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO lifecycle_moves (workspace_id, brand_id, level, key, name, kind, event, after, conditions, from_stages, any_stage,
			to_stage, churn_mode, emit_event, position)
		VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7, ''), NULLIF($8, ''), $9, $10, $11, $12, NULLIF($13, ''), $14, $15)`,
		ws, brand, m.Level, m.Key, m.Name, m.Kind, m.Event, m.After, raw, from, m.AnyStage, m.ToStage, m.ChurnMode, m.Emit, m.Position)
	return err
}

// from reports whether a move can start at stage (None for no stage).
func (m Move) from(stage string) bool {
	return m.AnyStage || slices.Contains(m.FromStages, stage)
}

// forProduct reports whether a move applies to a product with churnMode.
func (m Move) forProduct(churnMode string) bool {
	return m.ChurnMode == "" || m.ChurnMode == churnMode
}
