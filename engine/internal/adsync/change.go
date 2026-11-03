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
)

type changePayload struct {
	Change string `json:"change_id"`
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
}

func (s *Syncer) changeJob(ctx context.Context, job jobs.Job) error {
	var p changePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
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
var ChangeKinds = []string{ChangePause, ChangeResume, ChangeBudget}

// ChangeKind reports whether k is a change kind.
func ChangeKind(k string) bool {
	for _, x := range ChangeKinds {
		if x == strings.TrimSpace(k) {
			return true
		}
	}
	return false
}
