package adsync

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// New ads (step 6 of the paid ads plan): an ad_drafts row a person
// approved is made on the platform, paused, by this job (step create), and
// turned on when a person says so (step go_live). The API changes the
// draft's status and queues the step in the same transaction as its audit
// event; the platform calls run here.

// JobDraft carries out one step of an ad_drafts row.
const JobDraft = "ads.draft"

// Draft steps.
const (
	DraftCreate = "create"
	DraftGoLive = "go_live"
)

type draftPayload struct {
	Draft string `json:"draft_id"`
	Step  string `json:"step"`
}

// EnqueueDraft queues one step of a draft, once.
func EnqueueDraft(ctx context.Context, q jobs.Querier, ws, draftID, step string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobDraft, WorkspaceID: &ws, Payload: draftPayload{Draft: draftID, Step: step},
		IdempotencyKey: "ads.draft:" + draftID + ":" + step, MaxAttempts: 6})
	return err
}

func (s *Syncer) draftJob(ctx context.Context, job jobs.Job) error {
	var p draftPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	return s.RunDraft(ctx, *job.WorkspaceID, p.Draft, p.Step, job.Attempt >= job.MaxAttempts)
}

// RunDraft carries out one step of a draft. last says this is the job's
// last try, so an unreachable platform ends the step instead of retrying.
func (s *Syncer) RunDraft(ctx context.Context, ws, draftID, step string, last bool) error {
	var (
		kind, status, connector, integrationID string
		campaignExt, groupExt                  *string
		brand                                  string
		content, madeRaw                       []byte
		acct                                   account
	)
	err := s.Pool.QueryRow(ctx, `
		SELECT d.kind, d.status, d.brand_id, d.content, d.made, a.connector, a.integration_id, a.id, a.external_id, a.name, a.currency, a.time_zone, a.meta,
			c.external_id, g.external_id
		FROM ad_drafts d JOIN ad_accounts a ON a.id = d.ad_account_id
		LEFT JOIN ad_entities c ON c.id = d.campaign_entity_id LEFT JOIN ad_entities g ON g.id = d.ad_group_entity_id
		WHERE d.workspace_id = $1 AND d.id = $2`, ws, draftID).
		Scan(&kind, &status, &brand, &content, &madeRaw, &connector, &integrationID, &acct.id, &acct.externalID, &acct.name, &acct.currency, &acct.timeZone, &acct.meta,
			&campaignExt, &groupExt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	want := map[string]string{DraftCreate: "creating", DraftGoLive: "going_live"}[step]
	if want == "" || status != want {
		return nil
	}
	ev := track.Event{Name: "ad_draft.made", WorkspaceID: ws, ActorType: "system", Surface: "job",
		Props: map[string]any{"connector": connector, "kind": kind, "draft_id": draftID, "brand_id": brand, "step": step}}
	if step == DraftGoLive {
		ev.Name = "ad_draft.went_live"
	}
	// back is where a failed step leaves the draft: a refused draft has
	// failed; one that was made and couldn't be turned on stays paused,
	// so a person can try again.
	back := "failed"
	if step == DraftGoLive {
		back = "paused"
	}
	fail := func(outcome, code string, err error) error {
		metrics.AdDraftStep(connector, step, outcome)
		ev.Name = "ad_draft.failed"
		if outcome == "failed" {
			ev.Outcome = track.Failure
		}
		track.Fail(ctx, s.Log, ev, code, err)
		if outcome == "error" {
			return err
		}
		_, dbErr := s.Pool.Exec(ctx, `UPDATE ad_drafts SET status = $4, error = $3, updated_at = now() WHERE workspace_id = $1 AND id = $2 AND status = $5`,
			ws, draftID, trim(err.Error(), 500), back, want)
		return dbErr
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return fail("failed", "connector_gone", errors.New("the ad platform connection is gone"))
		}
		return fail("error", "internal", err)
	}
	cr, ok := o.Connector.(connectors.AdCreator)
	if !ok || !slices.Contains(cr.DraftKinds(), kind) {
		return fail("failed", "connector_invalid", fmt.Errorf("%s can't make a new %s here", o.Connector.Definition().Name, kind))
	}
	retry := func(err error) error {
		k := connectors.KindOf(err)
		if k == connectors.ErrUnavailable && !last {
			return err
		}
		return fail("failed", "connector_"+k, err)
	}
	if step == DraftGoLive {
		var made connectors.AdMade
		_ = json.Unmarshal(madeRaw, &made)
		if err := cr.GoLive(ctx, o.Client, o.Creds, acct.ad(), made); err != nil {
			return retry(err)
		}
		if _, err := s.Pool.Exec(ctx, `UPDATE ad_drafts SET status = 'live', error = '', live_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND id = $2 AND status = 'going_live'`, ws, draftID); err != nil {
			return fail("error", "internal", err)
		}
		metrics.AdDraftStep(connector, step, "success")
		track.Emit(ctx, ev)
		return nil
	}

	var d connectors.AdDraft
	if err := json.Unmarshal(content, &d); err != nil {
		return fail("failed", "invalid_request", fmt.Errorf("the draft can't be read: %w", err))
	}
	d.Kind = kind
	if campaignExt != nil {
		d.CampaignID = *campaignExt
	}
	if groupExt != nil {
		d.AdGroupID = *groupExt
	}
	made, err := cr.CreateAds(ctx, o.Client, o.Creds, acct.ad(), d)
	if err != nil {
		return retry(err)
	}
	madeJSON, _ := json.Marshal(made)
	if _, err := s.Pool.Exec(ctx, `UPDATE ad_drafts SET status = 'paused', made = $3, error = '', made_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND id = $2 AND status = 'creating'`, ws, draftID, madeJSON); err != nil {
		return fail("error", "internal", err)
	}
	metrics.AdDraftStep(connector, step, "success")
	track.Emit(ctx, ev)
	return nil
}
