package paidads

import (
	"context"
	"encoding/json"
	"errors"
)

// Shadow mode: while a brand's shadow_mode is on, every rule records what
// it would have done as a finding that is never applied or offered, so the
// owner can see the rules at work before turning them live.

// Finding is what a rule would have done.
type Finding struct {
	WorkspaceID, BrandID string
	// EntityID is the campaign, ad group or ad (ad_entities).
	EntityID string
	// Kind is a lever kind (Levers).
	Kind string
	// Autonomy is what the finding would have been live: auto or approve.
	Autonomy string
	// Unit is what the evidence is in: payers, signups or
	// platform_conversions.
	Unit string
	// Rule names the rule and its version, so a re-fit never changes old
	// rows.
	Rule        string
	RuleVersion int
	// ImpactMicros is the expected change in cost per new paying customer,
	// micros per payer; negative is better.
	ImpactMicros *int64
	Reason       string
	Evidence     map[string]any
	Proposed     map[string]any
}

// ErrShadowNotReady says the findings queue doesn't have its shadow
// columns yet (migration 0499, the verdicts workstream), so a shadow
// finding can't be recorded without showing up as a live suggestion.
var ErrShadowNotReady = errors.New("ad_suggestions has no shadow columns yet")

// TODO(paid-ads verdicts): migration 0499 adds autonomy, shadow, unit,
// rule, rule_version and impact_micros to ad_suggestions, and the status
// 'shadow'. Once it has merged, drop shadowReady and the error: the insert
// below already names those columns.
func shadowReady(ctx context.Context, q Querier) (bool, error) {
	var ok bool
	err := q.QueryRow(ctx, `SELECT count(*) = 4 FROM information_schema.columns
		WHERE table_name = 'ad_suggestions' AND column_name IN ('autonomy', 'shadow', 'unit', 'rule')`).Scan(&ok)
	return ok, err
}

// Shadow records what a rule would have done. It writes nothing and
// returns ErrShadowNotReady until the findings queue has its shadow
// columns. One shadow row per entity, kind and rule stands at a time: a
// repeat updates its reason, evidence and proposal.
func Shadow(ctx context.Context, q Querier, f Finding) error {
	ok, err := shadowReady(ctx, q)
	if err != nil {
		return err
	}
	if !ok {
		return ErrShadowNotReady
	}
	if f.Autonomy == "" {
		f.Autonomy = AutonomyApprove
	}
	if f.Unit == "" {
		f.Unit = "platform_conversions"
	}
	if f.RuleVersion == 0 {
		f.RuleVersion = 1
	}
	if f.Evidence == nil {
		f.Evidence = map[string]any{}
	}
	if f.Proposed == nil {
		f.Proposed = map[string]any{}
	}
	ev, _ := json.Marshal(f.Evidence)
	pr, _ := json.Marshal(f.Proposed)
	var existing string
	err = q.QueryRow(ctx, `SELECT id::text FROM ad_suggestions WHERE ad_entity_id = $1 AND kind = $2 AND rule = $3 AND status = 'shadow' AND shadow LIMIT 1`,
		f.EntityID, f.Kind, f.Rule).Scan(&existing)
	if err == nil {
		_, err = q.Exec(ctx, `UPDATE ad_suggestions SET reason = $2, evidence = $3, proposed = $4, impact_micros = $5, autonomy = $6, unit = $7, rule_version = $8, updated_at = now() WHERE id = $1`,
			existing, f.Reason, ev, pr, f.ImpactMicros, f.Autonomy, f.Unit, f.RuleVersion)
		return err
	}
	_, err = q.Exec(ctx, `INSERT INTO ad_suggestions (workspace_id, brand_id, ad_entity_id, kind, status, reason, evidence, proposed, autonomy, shadow, unit, rule, rule_version, impact_micros)
		VALUES ($1, $2, $3, $4, 'shadow', $5, $6, $7, $8, true, $9, $10, $11, $12)`,
		f.WorkspaceID, f.BrandID, f.EntityID, f.Kind, f.Reason, ev, pr, f.Autonomy, f.Unit, f.Rule, f.RuleVersion, f.ImpactMicros)
	return err
}
