package adsync

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/adtrack"
	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Adding the tracking template: a person asks for it on one ad account
// (the API answers with the plan first, read from the platform), and this
// job reads the account again, adds Outflare's tracking template to every
// URL suffix that needs it, records each change in one audit event, and
// syncs the account so tracking health shows the result. Only the URL
// suffixes change, and every parameter already in them stays.

// JobTracking adds the tracking template to one ad account.
const JobTracking = "ads.tracking"

// ChangeTracking is the kind ad changes metrics count it under.
const ChangeTracking = "tracking"

// trackingAuditMax is how many changes the audit event lists; the counts
// cover the rest.
const trackingAuditMax = 500

type trackingPayload struct {
	Account string `json:"ad_account_id"`
	Actor   string `json:"actor_id,omitempty"`
}

// EnqueueTracking queues the job for an ad account. key de-duplicates a
// retried request.
func EnqueueTracking(ctx context.Context, q jobs.Querier, ws, accountID, actorID, key string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobTracking, WorkspaceID: &ws, Payload: trackingPayload{Account: accountID, Actor: actorID},
		IdempotencyKey: "ads.tracking:" + accountID + ":" + key, MaxAttempts: 4})
	return err
}

// ErrNoTracker says the account's platform can't have its tracking
// changed by Outflare yet.
var ErrNoTracker = errors.New("this ad platform's tracking can't be changed from Outflare yet")

// Tracking is an account as adding the template reads it.
type Tracking struct {
	Connector, Integration string
	Brand                  *string
	Template               string
	acct                   account
	tracker                connectors.AdTracker
	open                   *connectors.Opened
}

// OpenTracking reads one of the workspace's ad accounts and opens its
// connection, ready to plan or apply. ErrNotFound when there is no such
// active account, ErrNoTracker when its platform can't be changed.
func (s *Syncer) OpenTracking(ctx context.Context, ws, accountID string) (*Tracking, error) {
	t := &Tracking{}
	var meta []byte
	err := s.Pool.QueryRow(ctx, `SELECT connector, integration_id, brand_id, id, external_id, name, currency, time_zone, meta
		FROM ad_accounts WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, ws, accountID).
		Scan(&t.Connector, &t.Integration, &t.Brand, &t.acct.id, &t.acct.externalID, &t.acct.name, &t.acct.currency, &t.acct.timeZone, &meta)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, connectors.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(meta, &t.acct.meta)
	n, ok := adtrack.NetworkForConnector(t.Connector)
	if !ok || n.Template() == "" {
		return nil, ErrNoTracker
	}
	t.Template = n.Template()
	o, err := s.Store.Open(ctx, s.Pool, ws, t.Integration)
	if err != nil {
		return nil, err
	}
	tr, ok := o.Connector.(connectors.AdTracker)
	if !ok {
		return nil, ErrNoTracker
	}
	t.tracker, t.open = tr, o
	return t, nil
}

// Plan reads the platform and returns the changes that would leave every
// ad with the template.
func (t *Tracking) Plan(ctx context.Context) ([]connectors.TrackingChange, error) {
	return t.tracker.TrackingPlan(ctx, t.open.Client, t.open.Creds, t.acct.ad(), t.Template)
}

func (s *Syncer) trackingJob(ctx context.Context, job jobs.Job) error {
	var p trackingPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	return s.AddTracking(ctx, *job.WorkspaceID, p.Account, p.Actor, job.Attempt >= job.MaxAttempts)
}

// AddTracking plans and applies the template on one account. last says
// this is the job's last try.
func (s *Syncer) AddTracking(ctx context.Context, ws, accountID, actorID string, last bool) error {
	ev := track.Event{Name: "ad_account.tracking_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Failure,
		Props: map[string]any{"ad_account_id": accountID}}
	t, err := s.OpenTracking(ctx, ws, accountID)
	if err != nil {
		code := "connector_" + connectors.KindOf(err)
		switch {
		case errors.Is(err, ErrNoTracker):
			code = "connector_invalid"
		case errors.Is(err, connectors.ErrNotFound):
			code = "not_found"
		}
		track.Fail(ctx, s.Log, ev, code, err)
		return nil
	}
	ev.Props["connector"] = t.Connector
	retry := func(err error) error {
		k := connectors.KindOf(err)
		if k == connectors.ErrUnavailable && !last {
			return err
		}
		metrics.AdChanged(t.Connector, ChangeTracking, "failed")
		track.Fail(ctx, s.Log, ev, "connector_"+k, err)
		return nil
	}
	plan, err := t.Plan(ctx)
	if err != nil {
		return retry(err)
	}
	done, err := t.tracker.ApplyTracking(ctx, t.open.Client, t.open.Creds, t.acct.ad(), plan)
	if err != nil {
		return retry(err)
	}
	counts := map[string]int{}
	failed := 0
	for _, ch := range done {
		if ch.Error != "" {
			failed++
			continue
		}
		counts[ch.Level]++
	}
	listed := done
	if len(listed) > trackingAuditMax {
		listed = listed[:trackingAuditMax]
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		data := map[string]any{"ad_account_id": accountID, "connector": t.Connector, "template": t.Template, "changes": listed,
			"changed": len(done) - failed, "failed": failed, "by_level": counts}
		if actorID != "" {
			data["requested_by"] = actorID
		}
		if t.Brand != nil {
			data["brand_id"] = *t.Brand
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "ad_account.tracking_added",
			TargetType: "ad_account", TargetID: accountID, Data: data,
			Props: map[string]any{"connector": t.Connector, "changed": len(done) - failed, "failed": failed}}); err != nil {
			return err
		}
		// Sync now, so the ads' links and tracking health show the result.
		_, err := Enqueue(ctx, tx, ws, t.Integration, "")
		return err
	})
	if err != nil {
		metrics.AdChanged(t.Connector, ChangeTracking, "error")
		track.Fail(ctx, s.Log, track.Event{Name: "ad_account.tracking_failed", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Error,
			Props: ev.Props}, "internal", err)
		return err
	}
	for range len(done) - failed {
		metrics.AdChanged(t.Connector, ChangeTracking, "applied")
	}
	for range failed {
		metrics.AdChanged(t.Connector, ChangeTracking, "failed")
	}
	return nil
}
