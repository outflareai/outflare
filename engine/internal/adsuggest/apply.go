package adsuggest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Applying findings. Every change a finding makes, on its own or on
// approval, is an ad_changes row like a person's: queued in the same
// transaction as its audit event and carried out by a job that calls the
// network and records how it ended. The campaign changes the first
// campaign calls knew (pause, resume, budget, keywords, bidding) go to the
// ad sync's change job (adsync, which imports this package, so its job's
// name is repeated here); the levers this package added (a negative taken
// out, a keyword or ad paused or resumed, a target moved, an exclusion
// added or removed) have their own job here, on the connector interfaces
// in connectors/ads.go.

// JobApply carries out one ad_changes row of the kinds this package added.
const JobApply = "ads.finding_change"

// jobChange is the ad sync's change job (adsync.JobChange), which carries
// out the campaign kinds.
const jobChange = "ads.change"

// Change kinds (ad_changes.kind).
const (
	ChangePause           = "pause"
	ChangeResume          = "resume"
	ChangeBudget          = "budget"
	ChangeKeywordAdd      = "keyword_add"
	ChangeNegativeAdd     = "negative_add"
	ChangeBidding         = "bidding"
	ChangeNegativeRemove  = "negative_remove"
	ChangeKeywordPause    = "keyword_pause"
	ChangeKeywordResume   = "keyword_resume"
	ChangeAdPause         = "ad_pause"
	ChangeAdResume        = "ad_resume"
	ChangeTarget          = "target"
	ChangeExclusionAdd    = "exclusion_add"
	ChangeExclusionRemove = "exclusion_remove"
)

// OwnChangeKinds are the change kinds JobApply carries out.
var OwnChangeKinds = []string{ChangeNegativeRemove, ChangeKeywordPause, ChangeKeywordResume, ChangeAdPause, ChangeAdResume, ChangeTarget, ChangeExclusionAdd, ChangeExclusionRemove}

// OwnChange says whether JobApply carries out a change kind.
func OwnChange(kind string) bool {
	for _, k := range OwnChangeKinds {
		if k == kind {
			return true
		}
	}
	return false
}

// Change is an ad_changes row's before or after. Its first fields are
// the ad sync's ChangeState, so the two jobs read each other's rows.
type Change struct {
	Status            string `json:"status,omitempty"`
	DailyBudgetMicros *int64 `json:"daily_budget_micros,omitempty"`
	// A keyword: its text and match type, the network's id of its ad
	// group (a negative without one is the campaign's), and, for a pause
	// or resume, the network's id of the keyword.
	Keyword   string `json:"keyword,omitempty"`
	MatchType string `json:"match_type,omitempty"`
	AdGroupID string `json:"ad_group_id,omitempty"`
	KeywordID string `json:"keyword_id,omitempty"`
	// An ad: the network's id.
	AdID string `json:"ad_id,omitempty"`
	// Bidding, as the ad sync's bidding change takes it.
	Bidding *connectors.AdBidding `json:"bidding,omitempty"`
	// A target move, and an exclusion.
	Target    *connectors.AdTarget    `json:"target,omitempty"`
	Exclusion *connectors.AdExclusion `json:"exclusion,omitempty"`
}

type changePayload struct {
	Change string `json:"change_id"`
}

// Current is what a campaign is before a change, for ChangeFor.
type Current struct {
	Status            string
	DailyBudgetMicros *int64
	Bidding           string
	TargetCPAMicros   *int64
	TargetROAS        *float64
}

// ErrNotApplicable says a finding's kind makes no change here (a
// consolidation is done in the network; a retired kind).
var ErrNotApplicable = errors.New("this finding makes no change Outflare can apply")

// num reads a number from a proposal, whether it came from the rule
// (int64) or from the row's JSON (float64).
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int64:
		return float64(x), true
	case int:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func str(m map[string]any, k string) string {
	s, _ := m[k].(string)
	return s
}

// ChangeFor is the change a finding of kind with proposed makes on a
// campaign that is cur now: the change kind and its before and after.
func ChangeFor(kind string, proposed map[string]any, cur Current) (changeKind string, before, after Change, err error) {
	before = Change{Status: cur.Status, DailyBudgetMicros: cur.DailyBudgetMicros}
	switch kind {
	case BudgetUp, BudgetDown:
		b, ok := num(proposed["daily_budget_micros"])
		if !ok || b < 10_000 {
			return "", before, after, fmt.Errorf("the finding names no daily budget to set")
		}
		nb := int64(b)
		return ChangeBudget, before, Change{DailyBudgetMicros: &nb}, nil
	case Pause:
		return ChangePause, before, Change{Status: connectors.AdPaused}, nil
	case Resume:
		return ChangeResume, before, Change{Status: connectors.AdActive}, nil
	case NegativeAdd, KeywordAdd:
		after = Change{Keyword: str(proposed, "keyword"), MatchType: str(proposed, "match_type"), AdGroupID: str(proposed, "ad_group_id")}
		if after.Keyword == "" || (kind == KeywordAdd && after.AdGroupID == "") {
			return "", before, after, fmt.Errorf("the finding names no keyword or ad group")
		}
		if kind == KeywordAdd {
			return ChangeKeywordAdd, before, after, nil
		}
		return ChangeNegativeAdd, before, after, nil
	case BiddingTarget:
		t, ok := num(proposed["target_cpa_micros"])
		if !ok || t < 10_000 {
			return "", before, after, fmt.Errorf("the finding has no target to bid to")
		}
		target := int64(t)
		before.Bidding = &connectors.AdBidding{Strategy: cur.Bidding, TargetCPAMicros: cur.TargetCPAMicros, TargetROAS: cur.TargetROAS}
		return ChangeBidding, before, Change{Bidding: &connectors.AdBidding{Strategy: connectors.BidMaxConversions, TargetCPAMicros: &target}}, nil
	case TargetTighten, TargetLoosen:
		before.Target = &connectors.AdTarget{TargetCPAMicros: cur.TargetCPAMicros, TargetROAS: cur.TargetROAS}
		if t, ok := num(proposed["target_cpa_micros"]); ok && t >= 10_000 {
			v := int64(t)
			return ChangeTarget, before, Change{Target: &connectors.AdTarget{TargetCPAMicros: &v}}, nil
		}
		if t, ok := num(proposed["target_roas"]); ok && t > 0 {
			return ChangeTarget, before, Change{Target: &connectors.AdTarget{TargetROAS: &t}}, nil
		}
		return "", before, after, fmt.Errorf("the finding names no target")
	case KeywordPause:
		after = Change{Status: connectors.AdPaused, Keyword: str(proposed, "keyword"), MatchType: str(proposed, "match_type"), AdGroupID: str(proposed, "ad_group_id"), KeywordID: str(proposed, "keyword_id")}
		if after.KeywordID == "" || after.AdGroupID == "" {
			return "", before, after, fmt.Errorf("the finding names no keyword")
		}
		return ChangeKeywordPause, Change{Status: connectors.AdActive, Keyword: after.Keyword, MatchType: after.MatchType, AdGroupID: after.AdGroupID, KeywordID: after.KeywordID}, after, nil
	case AdPause:
		after = Change{Status: connectors.AdPaused, AdGroupID: str(proposed, "ad_group_id"), AdID: str(proposed, "ad_id")}
		if after.AdID == "" || after.AdGroupID == "" {
			return "", before, after, fmt.Errorf("the finding names no ad")
		}
		return ChangeAdPause, Change{Status: connectors.AdActive, AdGroupID: after.AdGroupID, AdID: after.AdID}, after, nil
	case ExclusionAdd:
		x, err := exclusionOf(proposed["exclusion"])
		if err != nil {
			return "", before, after, err
		}
		return ChangeExclusionAdd, Change{}, Change{Exclusion: &x}, nil
	}
	return "", before, after, ErrNotApplicable
}

// exclusionOf reads an exclusion from a proposal: the struct itself, or
// its JSON.
func exclusionOf(v any) (connectors.AdExclusion, error) {
	switch x := v.(type) {
	case connectors.AdExclusion:
		return x, nil
	case map[string]any:
		out := connectors.AdExclusion{Kind: str(x, "kind"), Value: str(x, "value"), Detail: str(x, "detail"), Label: str(x, "label")}
		if out.Kind == "" || out.Value == "" {
			return out, fmt.Errorf("the finding names no exclusion")
		}
		return out, nil
	}
	return connectors.AdExclusion{}, fmt.Errorf("the finding names no exclusion")
}

// Inverse is the change that undoes an applied change: restore the
// budget, resume what was paused (or pause what was resumed), take the
// negative or exclusion out, put the target back. A keyword added is
// undone by pausing it, which needs the network's id of the keyword once
// synced (KeywordID is left for the caller to fill). ErrNotApplicable for
// a change with no inverse here (a split, a consolidation).
func Inverse(kind string, before, after Change) (changeKind string, inverseAfter Change, err error) {
	switch kind {
	case ChangeBudget:
		if before.DailyBudgetMicros == nil || *before.DailyBudgetMicros <= 0 {
			return "", Change{}, fmt.Errorf("the change didn't record the budget before it")
		}
		return ChangeBudget, Change{DailyBudgetMicros: before.DailyBudgetMicros}, nil
	case ChangePause:
		return ChangeResume, Change{Status: connectors.AdActive}, nil
	case ChangeResume:
		return ChangePause, Change{Status: connectors.AdPaused}, nil
	case ChangeNegativeAdd:
		return ChangeNegativeRemove, Change{Keyword: after.Keyword, MatchType: after.MatchType, AdGroupID: after.AdGroupID}, nil
	case ChangeKeywordAdd:
		return ChangeKeywordPause, Change{Status: connectors.AdPaused, Keyword: after.Keyword, MatchType: after.MatchType, AdGroupID: after.AdGroupID}, nil
	case ChangeKeywordPause:
		return ChangeKeywordResume, Change{Status: connectors.AdActive, Keyword: after.Keyword, MatchType: after.MatchType, AdGroupID: after.AdGroupID, KeywordID: after.KeywordID}, nil
	case ChangeKeywordResume:
		return ChangeKeywordPause, Change{Status: connectors.AdPaused, Keyword: after.Keyword, MatchType: after.MatchType, AdGroupID: after.AdGroupID, KeywordID: after.KeywordID}, nil
	case ChangeAdPause:
		return ChangeAdResume, Change{Status: connectors.AdActive, AdGroupID: after.AdGroupID, AdID: after.AdID}, nil
	case ChangeAdResume:
		return ChangeAdPause, Change{Status: connectors.AdPaused, AdGroupID: after.AdGroupID, AdID: after.AdID}, nil
	case ChangeTarget:
		if before.Target == nil {
			return "", Change{}, fmt.Errorf("the change didn't record the target before it")
		}
		return ChangeTarget, Change{Target: before.Target}, nil
	case ChangeBidding:
		if before.Bidding != nil && before.Bidding.Strategy != "" {
			return ChangeBidding, Change{Bidding: before.Bidding}, nil
		}
		if after.Bidding == nil {
			return "", Change{}, fmt.Errorf("the change didn't record the bidding")
		}
		// No bidding recorded before: the strategy stays and the target goes.
		return ChangeBidding, Change{Bidding: &connectors.AdBidding{Strategy: after.Bidding.Strategy}}, nil
	case ChangeExclusionAdd:
		if after.Exclusion == nil {
			return "", Change{}, fmt.Errorf("the change didn't record the exclusion")
		}
		return ChangeExclusionRemove, Change{Exclusion: after.Exclusion}, nil
	case ChangeExclusionRemove:
		if after.Exclusion == nil {
			return "", Change{}, fmt.Errorf("the change didn't record the exclusion")
		}
		return ChangeExclusionAdd, Change{Exclusion: after.Exclusion}, nil
	}
	return "", Change{}, ErrNotApplicable
}

// ActionFor is the audit action (and product event) a change kind
// records when asked for.
func ActionFor(kind string) string {
	return map[string]string{ChangePause: "ad_campaign.paused", ChangeResume: "ad_campaign.resumed", ChangeBudget: "ad_campaign.budget_changed",
		ChangeKeywordAdd: "ad_campaign.keyword_added", ChangeNegativeAdd: "ad_campaign.negative_added", ChangeBidding: "ad_campaign.bidding_changed",
		ChangeNegativeRemove: "ad_campaign.negative_removed", ChangeKeywordPause: "ad_campaign.keyword_paused", ChangeKeywordResume: "ad_campaign.keyword_resumed",
		ChangeAdPause: "ad_campaign.ad_paused", ChangeAdResume: "ad_campaign.ad_resumed", ChangeTarget: "ad_campaign.target_changed",
		ChangeExclusionAdd: "ad_campaign.exclusion_added", ChangeExclusionRemove: "ad_campaign.exclusion_removed"}[kind]
}

// QueueChange records one change for a campaign in q (a transaction, with
// the caller's audit event) and queues the job that makes it: the ad
// sync's for the campaign kinds, this package's for its own. actorID is
// the person or agent, nil for a rule. It returns the change's id.
func QueueChange(ctx context.Context, q jobs.Querier, ws string, brand *string, entityID, kind string, before, after any, actorID *string) (string, error) {
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(after)
	var id string
	if err := q.QueryRow(ctx, `INSERT INTO ad_changes (workspace_id, brand_id, ad_entity_id, kind, before, after, actor_id) VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		ws, brand, entityID, kind, b, a, actorID).Scan(&id); err != nil {
		return "", err
	}
	job := jobChange
	if OwnChange(kind) {
		job = JobApply
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: job, WorkspaceID: &ws, Payload: changePayload{Change: id}, IdempotencyKey: job + ":" + id, MaxAttempts: 6})
	return id, err
}

// autoApply makes a saving's change at once, in tx: the finding is
// measuring from now with applied_auto set, so a person can revert it.
// It returns false, leaving the finding open, when the kind makes no
// change here or one of the kind is already queued on the campaign.
func (r *Runner) autoApply(ctx context.Context, tx pgx.Tx, ws string, c campaign, p *proposal, suggestionID string) (bool, error) {
	kind, before, after, err := ChangeFor(p.kind, p.proposed, Current{Status: c.status, DailyBudgetMicros: c.budget, Bidding: c.bidding, TargetCPAMicros: c.platformTCPA, TargetROAS: c.platformTROAS})
	if err != nil {
		return false, nil
	}
	var queued bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ad_changes WHERE ad_entity_id = $1 AND kind = $2 AND status = 'queued')`, c.id, kind).Scan(&queued); err != nil {
		return false, err
	}
	if queued {
		return false, nil
	}
	changeID, err := QueueChange(ctx, tx, ws, c.brand, c.id, kind, before, after, nil)
	if err != nil {
		return false, err
	}
	data := map[string]any{"change_id": changeID, "campaign_id": c.id, "external_id": c.externalID, "connector": c.connector, "brand_id": c.brand,
		"before": before, "after": after, "rule": p.rule, "suggestion_id": suggestionID}
	if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: ActionFor(kind), TargetType: "ad_campaign", TargetID: c.id, Data: data,
		Props: map[string]any{"connector": c.connector, "kind": kind, "auto": true, "rule": p.rule}}); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE ad_suggestions SET status = 'measuring', change_id = $2, applied_auto = true, decided_at = now(), updated_at = now() WHERE id = $1 AND status = 'open'`, suggestionID, changeID)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if err := Started(ctx, tx, suggestionID, p.kind, 0, r.now().UTC()); err != nil {
		return false, err
	}
	return true, audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_suggestion.applied", TargetType: "ad_suggestion", TargetID: suggestionID,
		Data:  map[string]any{"suggestion_id": suggestionID, "campaign_id": c.id, "connector": c.connector, "kind": p.kind, "change_id": changeID, "brand_id": c.brand, "rule": p.rule, "unit": c.unit},
		Props: map[string]any{"connector": c.connector, "kind": p.kind, "rule": p.rule, "autonomy": "auto", "unit": c.unit}})
}

// ApplyFindingChange carries out one queued change of this package's
// kinds. last says this is the job's last try, so an unreachable network
// ends the change as failed instead of leaving it queued.
func (r *Runner) ApplyFindingChange(ctx context.Context, ws, changeID string, last bool) error {
	var (
		kind, status, connector, integrationID, acctID, campaignExternal string
		brand                                                            *string
		after                                                            []byte
		acct                                                             connectors.AdAccount
		meta                                                             []byte
	)
	err := r.Pool.QueryRow(ctx, `
		SELECT c.kind, c.status, c.after, c.brand_id::text, a.connector, a.integration_id, a.id, a.external_id, a.name, a.currency, a.time_zone, a.meta, e.external_id
		FROM ad_changes c JOIN ad_entities e ON e.id = c.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE c.workspace_id = $1 AND c.id = $2`, ws, changeID).
		Scan(&kind, &status, &after, &brand, &connector, &integrationID, &acctID, &acct.ID, &acct.Name, &acct.Currency, &acct.TimeZone, &meta, &campaignExternal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "queued" || !OwnChange(kind) {
		return nil
	}
	_ = json.Unmarshal(meta, &acct.Meta)
	var want Change
	_ = json.Unmarshal(after, &want)
	ev := track.Event{Name: "ad_change.applied", WorkspaceID: ws, ActorType: "system", Surface: "job", Props: map[string]any{"connector": connector, "kind": kind, "change_id": changeID}}
	if brand != nil {
		ev.Props["brand_id"] = *brand
	}
	fail := func(outcome, code string, err error) error {
		metrics.AdChanged(connector, kind, outcome)
		ev.Name = "ad_change.failed"
		if outcome == "failed" {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, r.log(), ev, code, err)
		if outcome == "error" {
			return err
		}
		msg := err.Error()
		if len(msg) > 500 {
			msg = msg[:500]
		}
		_, dbErr := r.Pool.Exec(ctx, `UPDATE ad_changes SET status = 'failed', error = $3, applied_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, changeID, msg)
		return dbErr
	}
	if r.Store == nil {
		return fail("failed", "connector_invalid", errors.New("no connector store to apply the change with"))
	}
	o, err := r.Store.Open(ctx, r.Pool, ws, integrationID)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return fail("failed", "connector_gone", errors.New("the ad platform connection is gone"))
		}
		return fail("error", "internal", err)
	}
	name := o.Connector.Definition().Name
	invalid := func(what string) error {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot %s here", name, what))
	}
	var mirror func(tx pgx.Tx) error
	switch kind {
	case ChangeNegativeRemove:
		k, ok := o.Connector.(connectors.AdKeywordEditor)
		if !ok {
			return invalid("remove negative keywords")
		}
		err = k.RemoveNegativeKeyword(ctx, o.Client, o.Creds, acct, campaignExternal, want.AdGroupID, want.Keyword, want.MatchType)
	case ChangeKeywordPause, ChangeKeywordResume:
		k, ok := o.Connector.(connectors.AdKeywordEditor)
		if !ok {
			return invalid("pause or resume keywords")
		}
		active := kind == ChangeKeywordResume
		err = k.SetKeywordStatus(ctx, o.Client, o.Creds, acct, want.AdGroupID, want.KeywordID, active)
		mirror = func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE ad_keywords SET status = $3, updated_at = now() WHERE external_id = $2
				AND ad_group_id IN (SELECT id FROM ad_entities WHERE ad_account_id = $1 AND level = 'ad_group' AND external_id = $4)`, acctID, want.KeywordID, statusWord(active), want.AdGroupID)
			return err
		}
	case ChangeAdPause, ChangeAdResume:
		m, ok := o.Connector.(connectors.AdStatusManager)
		if !ok {
			return invalid("pause or resume ads")
		}
		active := kind == ChangeAdResume
		err = m.SetAdStatus(ctx, o.Client, o.Creds, acct, want.AdGroupID, want.AdID, active)
		mirror = func(tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE ad_entities SET status = $3, updated_at = now() WHERE ad_account_id = $1 AND level = 'ad' AND external_id = $2`, acctID, want.AdID, statusWord(active))
			return err
		}
	case ChangeTarget:
		m, ok := o.Connector.(connectors.AdTargetManager)
		if !ok {
			return invalid("move targets")
		}
		if want.Target == nil {
			err = connectors.Errf(connectors.ErrInvalid, "the change names no target")
		} else {
			err = m.SetCampaignTarget(ctx, o.Client, o.Creds, acct, campaignExternal, *want.Target)
			mirror = func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE ad_entities SET platform_target_cpa_micros = $3, platform_target_roas = $4, updated_at = now() WHERE ad_account_id = $1 AND level = 'campaign' AND external_id = $2`,
					acctID, campaignExternal, want.Target.TargetCPAMicros, want.Target.TargetROAS)
				return err
			}
		}
	case ChangeExclusionAdd, ChangeExclusionRemove:
		m, ok := o.Connector.(connectors.AdExclusionManager)
		if !ok {
			return invalid("exclude")
		}
		if want.Exclusion == nil {
			err = connectors.Errf(connectors.ErrInvalid, "the change names no exclusion")
		} else if kind == ChangeExclusionAdd {
			err = m.AddExclusion(ctx, o.Client, o.Creds, acct, campaignExternal, *want.Exclusion)
		} else {
			err = m.RemoveExclusion(ctx, o.Client, o.Creds, acct, campaignExternal, *want.Exclusion)
		}
	}
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable && !last {
			return err
		}
		return fail("failed", "connector_"+connectors.KindOf(err), err)
	}
	err = pgx.BeginFunc(ctx, r.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ad_changes SET status = 'applied', applied_at = $3, verify_note = 'shows after the next sync' WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`,
			ws, changeID, r.now()); err != nil {
			return err
		}
		if mirror != nil {
			return mirror(tx)
		}
		return nil
	})
	if err != nil {
		return fail("error", "internal", err)
	}
	metrics.AdChanged(connector, kind, "applied")
	track.Emit(ctx, ev)
	return nil
}

func statusWord(active bool) string {
	if active {
		return connectors.AdActive
	}
	return connectors.AdPaused
}
