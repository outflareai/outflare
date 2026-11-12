package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Managing campaigns: a change a person or an agent asked for (pausing or
// resuming a campaign, setting its daily budget) is an ad_changes row the
// API makes in the same transaction as its audit event, with this job
// queued to carry it out. The job makes the one call the change needs on
// the workspace's own account, reads the campaign back to see that the
// platform shows it, and records how it ended. The platform call is the
// side effect, so it runs here and never in a request handler.

// JobChange carries one ad_changes row out.
const JobChange = "ads.change"

// Change kinds.
const (
	ChangePause  = "pause"
	ChangeResume = "resume"
	ChangeBudget = "budget"
	// Keywords (Google): a keyword added to an ad group, a negative
	// keyword added to a campaign or ad group, a keyword given its own ad
	// group.
	ChangeKeywordAdd   = "keyword_add"
	ChangeNegativeAdd  = "negative_add"
	ChangeKeywordSplit = "keyword_split"
	// Targeting: how the campaign bids, a customer list audience added or
	// taken out, a country added or taken out.
	ChangeBidding        = "bidding"
	ChangeAudienceAdd    = "audience_add"
	ChangeAudienceRemove = "audience_remove"
	ChangeCountryAdd     = "country_add"
	ChangeCountryRemove  = "country_remove"
	// Removing a campaign, an ad or a keyword, which can't be undone on
	// the platform: only a person's own session asks for one.
	ChangeCampaignRemove = "campaign_remove"
	ChangeAdRemove       = "ad_remove"
	ChangeKeywordRemove  = "keyword_remove"
)

// RemoveKind reports whether k removes a campaign, an ad or a keyword.
func RemoveKind(k string) bool {
	return k == ChangeCampaignRemove || k == ChangeAdRemove || k == ChangeKeywordRemove
}

// TargetingKind reports whether k changes the campaign's bidding or whom
// it reaches.
func TargetingKind(k string) bool {
	switch k {
	case ChangeBidding, ChangeAudienceAdd, ChangeAudienceRemove, ChangeCountryAdd, ChangeCountryRemove:
		return true
	}
	return false
}

// KeywordKind reports whether k changes keywords rather than the campaign.
func KeywordKind(k string) bool {
	return k == ChangeKeywordAdd || k == ChangeNegativeAdd || k == ChangeKeywordSplit
}

type changePayload struct {
	Change string `json:"change_id,omitempty"`
	// AccountChange is an ad_account_changes row instead (accountchange.go).
	AccountChange string `json:"account_change_id,omitempty"`
}

// EnqueueChange queues the job for an ad_changes row, once: a retry of
// the request that made it finds the same job.
func EnqueueChange(ctx context.Context, q jobs.Querier, ws, changeID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobChange, WorkspaceID: &ws, Payload: changePayload{Change: changeID},
		IdempotencyKey: "ads.change:" + changeID, MaxAttempts: 6})
	return err
}

// ChangeState is a campaign's status and daily budget, as kept in an
// ad_changes row's before and after.
type ChangeState struct {
	Status            string `json:"status,omitempty"`
	DailyBudgetMicros *int64 `json:"daily_budget_micros,omitempty"`
	// A keyword change: the keyword, its match type (exact, phrase,
	// broad), and the platform's id of the ad group it is in (a negative
	// without one is for the whole campaign). A split also names the new
	// ad group, and once made, its id.
	Keyword        string `json:"keyword,omitempty"`
	MatchType      string `json:"match_type,omitempty"`
	AdGroupID      string `json:"ad_group_id,omitempty"`
	NewAdGroupName string `json:"new_ad_group_name,omitempty"`
	NewAdGroupID   string `json:"new_ad_group_id,omitempty"`
	// A targeting change: the bidding asked for, or the audience (its id
	// on the platform and name) or country added or taken out.
	Bidding      *connectors.AdBidding `json:"bidding,omitempty"`
	AudienceID   string                `json:"audience_id,omitempty"`
	AudienceName string                `json:"audience_name,omitempty"`
	Country      string                `json:"country,omitempty"`
	// A removal: the platform's ids of the ad or keyword (with AdGroupID),
	// and the name of what was removed, as it was.
	AdID      string `json:"ad_id,omitempty"`
	KeywordID string `json:"keyword_id,omitempty"`
	Name      string `json:"name,omitempty"`
}

func (s *Syncer) changeJob(ctx context.Context, job jobs.Job) error {
	var p changePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	if p.AccountChange != "" {
		return s.ApplyAccountChange(ctx, *job.WorkspaceID, p.AccountChange, job.Attempt >= job.MaxAttempts)
	}
	return s.ApplyChange(ctx, *job.WorkspaceID, p.Change, job.Attempt >= job.MaxAttempts)
}

// ApplyChange carries one queued change out. last says this is the job's
// last try, so an unreachable platform ends the change as failed instead
// of leaving it queued.
func (s *Syncer) ApplyChange(ctx context.Context, ws, changeID string, last bool) error {
	var (
		kind, connector, entityExternal, integrationID, status string
		brand                                                  *string
		after                                                  []byte
		acct                                                   account
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT c.kind, c.status, c.after, c.brand_id, a.connector, a.integration_id, a.id, a.external_id, a.name, a.currency, a.time_zone, a.meta, e.external_id
		FROM ad_changes c JOIN ad_entities e ON e.id = c.ad_entity_id JOIN ad_accounts a ON a.id = e.ad_account_id
		WHERE c.workspace_id = $1 AND c.id = $2`, ws, changeID).
		Scan(&kind, &status, &after, &brand, &connector, &integrationID, &acct.id, &acct.externalID, &acct.name, &acct.currency, &acct.timeZone, &acct.meta, &entityExternal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status != "queued" {
		return nil
	}
	var want ChangeState
	_ = json.Unmarshal(after, &want)
	ev := track.Event{Name: "ad_change.applied", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"connector": connector, "kind": kind, "change_id": changeID}}
	if brand != nil {
		ev.Props["brand_id"] = *brand
	}
	fail := func(outcome, code string, err error) error {
		metrics.AdChanged(connector, kind, outcome)
		ev.Name = "ad_change.failed"
		if outcome == "failed" {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, s.Log, ev, code, err)
		if outcome == "error" {
			return err
		}
		_, dbErr := s.Pool.Exec(ctx, `UPDATE ad_changes SET status = 'failed', error = $3, applied_at = now() WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`,
			ws, changeID, trim(err.Error(), 500))
		return dbErr
	}

	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return fail("failed", "connector_gone", errors.New("the ad platform connection is gone"))
		}
		return fail("error", "internal", err)
	}
	if KeywordKind(kind) {
		return s.applyKeyword(ctx, ws, changeID, kind, connector, o, acct.ad(), entityExternal, want, last, ev, fail)
	}
	if TargetingKind(kind) {
		return s.applyTargeting(ctx, ws, changeID, kind, connector, o, acct, entityExternal, want, last, ev, fail)
	}
	if RemoveKind(kind) {
		return s.applyRemove(ctx, ws, changeID, kind, connector, o, acct, entityExternal, want, last, ev, fail)
	}
	m, ok := o.Connector.(connectors.AdsManager)
	if !ok {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot change campaigns", o.Connector.Definition().Name))
	}
	ad := acct.ad()
	switch kind {
	case ChangePause:
		err = m.SetCampaignStatus(ctx, o.Client, o.Creds, ad, entityExternal, false)
	case ChangeResume:
		err = m.SetCampaignStatus(ctx, o.Client, o.Creds, ad, entityExternal, true)
	case ChangeBudget:
		if want.DailyBudgetMicros == nil {
			err = connectors.Errf(connectors.ErrInvalid, "the change names no budget")
		} else {
			err = m.SetCampaignDailyBudget(ctx, o.Client, o.Creds, ad, entityExternal, *want.DailyBudgetMicros)
		}
	default:
		err = connectors.Errf(connectors.ErrInvalid, "unknown change kind %q", kind)
	}
	if err != nil {
		k := connectors.KindOf(err)
		if k == connectors.ErrUnavailable && !last {
			// The platform may be back on the next try; the change stays
			// queued until the job gives up.
			return err
		}
		return fail("failed", "connector_"+k, err)
	}

	// Read it back: what the platform shows is what counts.
	now := s.now()
	got, readErr := m.AdCampaign(ctx, o.Client, o.Creds, ad, entityExternal)
	verified, note := false, ""
	switch {
	case readErr != nil:
		note = "could not read the campaign back: " + trim(readErr.Error(), 300)
	case kind == ChangeBudget && (got.DailyBudgetMicros == nil || *got.DailyBudgetMicros != *want.DailyBudgetMicros):
		note = fmt.Sprintf("the platform shows a daily budget of %s micros", budgetWord(got.DailyBudgetMicros))
	case kind != ChangeBudget && got.Status != want.Status:
		note = "the platform shows the campaign as " + got.Status + " (" + got.ProviderStatus + ")"
	default:
		verified = true
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var verifiedAt *time.Time
		if verified {
			verifiedAt = &now
		}
		if _, err := tx.Exec(ctx, `UPDATE ad_changes SET status = 'applied', applied_at = $3, verified_at = $4, verify_note = $5
			WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, changeID, now, verifiedAt, note); err != nil {
			return err
		}
		if readErr != nil {
			// Keep the mirror at what was asked for; the next sync corrects it.
			got = connectors.AdCampaign{Status: want.Status, DailyBudgetMicros: want.DailyBudgetMicros}
			if kind == ChangeBudget {
				got.Status = ""
			}
		}
		_, err := tx.Exec(ctx, `UPDATE ad_entities SET status = CASE WHEN $3 = '' THEN status ELSE $3 END,
			provider_status = CASE WHEN $4 = '' THEN provider_status ELSE $4 END,
			daily_budget_micros = COALESCE($5, daily_budget_micros), updated_at = now()
			WHERE ad_account_id = $1 AND level = 'campaign' AND external_id = $2`, acct.id, entityExternal, statusOrEmpty(got.Status), trim(got.ProviderStatus, 60), got.DailyBudgetMicros)
		return err
	})
	if err != nil {
		return fail("error", "internal", err)
	}
	outcome := "applied"
	if !verified {
		outcome = "unverified"
		ev.Props["unverified"] = true
	}
	metrics.AdChanged(connector, kind, outcome)
	track.Emit(ctx, ev)
	return nil
}

func statusOrEmpty(s string) string {
	if s == "" {
		return ""
	}
	return statusOr(s)
}

func budgetWord(b *int64) string {
	if b == nil {
		return "nothing"
	}
	return fmt.Sprint(*b)
}

// ChangeKinds lists the kinds, for the API's checks.
var ChangeKinds = []string{ChangePause, ChangeResume, ChangeBudget, ChangeKeywordAdd, ChangeNegativeAdd, ChangeKeywordSplit,
	ChangeBidding, ChangeAudienceAdd, ChangeAudienceRemove, ChangeCountryAdd, ChangeCountryRemove,
	ChangeCampaignRemove, ChangeAdRemove, ChangeKeywordRemove}

// applyKeyword makes a keyword change. Keywords aren't read back here: the
// next sync lists them.
func (s *Syncer) applyKeyword(ctx context.Context, ws, changeID, kind, connector string, o *connectors.Opened, ad connectors.AdAccount, campaignExternal string,
	want ChangeState, last bool, ev track.Event, fail func(outcome, code string, err error) error) error {
	k, ok := o.Connector.(connectors.AdKeywordManager)
	if !ok {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot change keywords", o.Connector.Definition().Name))
	}
	var err error
	switch kind {
	case ChangeKeywordAdd:
		err = k.AddKeyword(ctx, o.Client, o.Creds, ad, want.AdGroupID, want.Keyword, want.MatchType)
	case ChangeNegativeAdd:
		err = k.AddNegativeKeyword(ctx, o.Client, o.Creds, ad, campaignExternal, want.AdGroupID, want.Keyword, want.MatchType)
	case ChangeKeywordSplit:
		want.NewAdGroupID, err = k.SplitKeyword(ctx, o.Client, o.Creds, ad, campaignExternal, want.AdGroupID, want.NewAdGroupName, want.Keyword)
	}
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable && !last {
			return err
		}
		return fail("failed", "connector_"+connectors.KindOf(err), err)
	}
	after, _ := json.Marshal(want)
	if _, err := s.Pool.Exec(ctx, `UPDATE ad_changes SET status = 'applied', applied_at = $3, after = $4, verify_note = 'shows after the next sync'
		WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, changeID, s.now(), after); err != nil {
		return fail("error", "internal", err)
	}
	metrics.AdChanged(connector, kind, "applied")
	track.Emit(ctx, ev)
	return nil
}

// applyTargeting makes a bidding, audience or country change. Bidding is
// kept in the mirror as asked; the next sync shows what the platform
// says, and the campaign's targeting is read live.
func (s *Syncer) applyTargeting(ctx context.Context, ws, changeID, kind, connector string, o *connectors.Opened, acct account, campaignExternal string,
	want ChangeState, last bool, ev track.Event, fail func(outcome, code string, err error) error) error {
	t, ok := o.Connector.(connectors.AdTargetingManager)
	if !ok {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot change targeting", o.Connector.Definition().Name))
	}
	ad := acct.ad()
	var err error
	switch kind {
	case ChangeBidding:
		if want.Bidding == nil {
			err = connectors.Errf(connectors.ErrInvalid, "the change names no bidding")
		} else {
			err = t.SetBidding(ctx, o.Client, o.Creds, ad, campaignExternal, *want.Bidding)
		}
	case ChangeAudienceAdd, ChangeAudienceRemove:
		err = t.SetCampaignAudience(ctx, o.Client, o.Creds, ad, campaignExternal, want.AudienceID, kind == ChangeAudienceAdd)
	case ChangeCountryAdd, ChangeCountryRemove:
		err = t.SetCampaignCountry(ctx, o.Client, o.Creds, ad, campaignExternal, want.Country, kind == ChangeCountryAdd)
	}
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable && !last {
			return err
		}
		return fail("failed", "connector_"+connectors.KindOf(err), err)
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE ad_changes SET status = 'applied', applied_at = $3, verify_note = 'shows on the campaign''s targeting now, and in its numbers after the next sync'
			WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, changeID, s.now()); err != nil {
			return err
		}
		if kind != ChangeBidding {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE ad_entities SET bidding_strategy = $3, platform_target_cpa_micros = $4, platform_target_roas = $5, updated_at = now()
			WHERE ad_account_id = $1 AND level = 'campaign' AND external_id = $2`, acct.id, campaignExternal, want.Bidding.Strategy, want.Bidding.TargetCPAMicros, want.Bidding.TargetROAS)
		return err
	})
	if err != nil {
		return fail("error", "internal", err)
	}
	metrics.AdChanged(connector, kind, "applied")
	track.Emit(ctx, ev)
	return nil
}

// ChangeKind reports whether k is a change kind.
func ChangeKind(k string) bool {
	for _, x := range ChangeKinds {
		if x == strings.TrimSpace(k) {
			return true
		}
	}
	return false
}

// applyRemove removes a campaign, an ad or a keyword, then reads it back:
// the change is verified when the platform shows it removed, and only
// then is the mirror (ad_entities, ad_keywords) marked removed. A read
// that fails leaves the mirror at what was asked for; the next sync
// corrects it.
func (s *Syncer) applyRemove(ctx context.Context, ws, changeID, kind, connector string, o *connectors.Opened, acct account, campaignExternal string,
	want ChangeState, last bool, ev track.Event, fail func(outcome, code string, err error) error) error {
	rm, ok := o.Connector.(connectors.AdRemover)
	if !ok {
		return fail("failed", "connector_invalid", fmt.Errorf("%s cannot remove campaigns, ads or keywords here", o.Connector.Definition().Name))
	}
	ad := acct.ad()
	what, id := connectors.RemoveCampaign, campaignExternal
	switch kind {
	case ChangeAdRemove:
		what, id = connectors.RemoveAd, want.AdID
	case ChangeKeywordRemove:
		what, id = connectors.RemoveKeyword, want.KeywordID
	}
	if err := rm.Remove(ctx, o.Client, o.Creds, ad, what, want.AdGroupID, id); err != nil {
		if connectors.KindOf(err) == connectors.ErrUnavailable && !last {
			return err
		}
		return fail("failed", "connector_"+connectors.KindOf(err), err)
	}
	now := s.now()
	got, readErr := rm.RemovedStatus(ctx, o.Client, o.Creds, ad, what, want.AdGroupID, id)
	verified, note := false, ""
	switch {
	case readErr != nil:
		note = "could not read it back: " + trim(readErr.Error(), 300)
	case got != connectors.AdRemoved:
		note = "the platform shows it as " + got
	default:
		verified = true
	}
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var verifiedAt *time.Time
		if verified {
			verifiedAt = &now
		}
		if _, err := tx.Exec(ctx, `UPDATE ad_changes SET status = 'applied', applied_at = $3, verified_at = $4, verify_note = $5
			WHERE workspace_id = $1 AND id = $2 AND status = 'queued'`, ws, changeID, now, verifiedAt, note); err != nil {
			return err
		}
		if !verified && readErr == nil {
			return nil
		}
		var err error
		switch what {
		case connectors.RemoveCampaign:
			_, err = tx.Exec(ctx, `UPDATE ad_entities SET status = 'removed', provider_status = 'REMOVED', updated_at = now()
				WHERE ad_account_id = $1 AND level = 'campaign' AND external_id = $2`, acct.id, id)
		case connectors.RemoveAd:
			_, err = tx.Exec(ctx, `UPDATE ad_entities SET status = 'removed', provider_status = 'REMOVED', updated_at = now()
				WHERE ad_account_id = $1 AND level = 'ad' AND external_id = $2`, acct.id, id)
		case connectors.RemoveKeyword:
			_, err = tx.Exec(ctx, `UPDATE ad_keywords SET status = 'removed', updated_at = now() WHERE ad_account_id = $1 AND external_id = $2
				AND ad_group_id IN (SELECT g.id FROM ad_entities g WHERE g.ad_account_id = $1 AND g.level = 'ad_group' AND g.external_id = $3)`, acct.id, id, want.AdGroupID)
		}
		return err
	})
	if err != nil {
		return fail("error", "internal", err)
	}
	outcome := "applied"
	if !verified {
		outcome = "unverified"
		ev.Props["unverified"] = true
	}
	metrics.AdChanged(connector, kind, outcome)
	track.Emit(ctx, ev)
	return nil
}
