// Package deploys watches code and deploy connections (GitHub, Vercel)
// for production deploys of the websites a workspace runs, and records
// each one. Other features act on a deploy instead of guessing when a
// change went live: OnReady runs in the transaction that records it.
package deploys

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

const (
	JobCheck = "deploys.check"
	// Each active target is checked this often. Deploys are free to read,
	// and a fix's re-check should follow its deploy within minutes.
	interval = 5 * time.Minute
	// A new target looks back this far, so recent deploys show at once.
	lookBack = 7 * 24 * time.Hour
)

// Deploy is a production deploy as recorded.
type Deploy struct {
	ID          string    `json:"id"`
	WorkspaceID string    `json:"-"`
	TargetID    string    `json:"target_id"`
	SiteID      *string   `json:"site_id"`
	BrandID     *string   `json:"brand_id"`
	ExternalID  string    `json:"external_id"`
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	Commit      string    `json:"commit"`
	Ref         string    `json:"ref"`
	Message     string    `json:"message"`
	URL         string    `json:"url"`
	DeployedAt  time.Time `json:"deployed_at"`
	SeenAt      time.Time `json:"seen_at"`
}

// Hook runs in the transaction that records a deploy going live, so what
// it starts commits with the deploy.
type Hook func(ctx context.Context, tx pgx.Tx, d Deploy) error

// Service checks watched targets.
type Service struct {
	Pool  *pgxpool.Pool
	Store *connectors.Store
	Log   *slog.Logger
	// OnReady runs for each production deploy that went live.
	OnReady []Hook
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

type payload struct {
	Target string `json:"target_id"`
}

// Register adds the job handler.
func (s *Service) Register(q *jobs.Queue) { q.Handle(JobCheck, s.job) }

// Run schedules checks until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		if err := s.tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "deploys.schedule_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Service) tick(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `
		SELECT t.id, t.workspace_id FROM deploy_targets t JOIN integrations i ON i.id = t.integration_id
		WHERE t.status = 'active' AND i.status = 'connected'
		  AND (t.last_checked_at IS NULL OR t.last_checked_at < now() - $1::interval)`,
		fmt.Sprintf("%d seconds", int(interval.Seconds())))
	if err != nil {
		return err
	}
	type due struct{ id, ws string }
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (due, error) {
		var d due
		return d, r.Scan(&d.id, &d.ws)
	})
	if err != nil {
		return err
	}
	slot := s.now().Truncate(interval).Unix()
	for _, d := range list {
		if _, err := Enqueue(ctx, s.Pool, d.ws, d.id, fmt.Sprintf("%s:%d", d.id, slot)); err != nil {
			return err
		}
	}
	return nil
}

// Enqueue asks for a check of one target. Key de-duplicates; "" is a check
// someone asked for now.
func Enqueue(ctx context.Context, q jobs.Querier, ws, targetID, key string) (int64, error) {
	if key == "" {
		key = fmt.Sprintf("%s:now:%d", targetID, time.Now().Unix()/30)
	}
	return jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobCheck, WorkspaceID: &ws, Payload: payload{Target: targetID},
		IdempotencyKey: key, MaxAttempts: 2})
}

func (s *Service) job(ctx context.Context, job jobs.Job) error {
	var p payload
	if err := json.Unmarshal(job.Payload, &p); err != nil || job.WorkspaceID == nil {
		return nil
	}
	_, err := s.Check(ctx, *job.WorkspaceID, p.Target)
	if err != nil && connectors.KindOf(err) == connectors.ErrUnavailable && !errors.Is(err, connectors.ErrNotFound) && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	// Refused tokens and missing repositories show on the target.
	return nil
}

// Result is what one check found.
type Result struct {
	Seen int `json:"seen"`
	New  int `json:"new"`
	Live int `json:"live"`
}

type target struct {
	id, ws, integration, target string
	site, brand                 *string
	lastDeploy                  *time.Time
}

// Check reads one target's recent production deploys and records the new
// ones.
func (s *Service) Check(ctx context.Context, ws, targetID string) (Result, error) {
	var t target
	err := s.Pool.QueryRow(ctx, `
		SELECT id, workspace_id, integration_id, target, site_id::text, brand_id::text, last_deploy_at
		FROM deploy_targets WHERE id = $1 AND workspace_id = $2`, targetID, ws).
		Scan(&t.id, &t.ws, &t.integration, &t.target, &t.site, &t.brand, &t.lastDeploy)
	if err != nil {
		return Result{}, err
	}
	res, err := s.check(ctx, t)
	msg := (*string)(nil)
	if err != nil {
		m := err.Error()
		msg = &m
		track.Fail(ctx, s.Log, track.Event{Name: "deploys.check_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"target_id": t.id}}, "connector_"+connectors.KindOf(err), err)
	} else {
		track.Emit(ctx, track.Event{Name: "deploys.checked", WorkspaceID: ws, ActorType: "system", Surface: "job",
			Props: map[string]any{"target_id": t.id, "seen": res.Seen, "new": res.New, "live": res.Live}})
	}
	if _, uerr := s.Pool.Exec(ctx, `UPDATE deploy_targets SET last_checked_at = now(), last_error = $2 WHERE id = $1`, t.id, msg); uerr != nil && err == nil {
		err = uerr
	}
	if connectors.KindOf(err) == connectors.ErrAuth {
		_ = s.Store.Checked(ctx, s.Pool, ws, t.integration, nil, err)
	}
	return res, err
}

func (s *Service) check(ctx context.Context, t target) (Result, error) {
	o, err := s.Store.Open(ctx, s.Pool, t.ws, t.integration)
	if err != nil {
		return Result{}, err
	}
	r, ok := o.Connector.(connectors.DeployReader)
	if !ok {
		return Result{}, connectors.Errf(connectors.ErrInvalid, "%s can't report deploys", o.Name)
	}
	since := s.now().Add(-lookBack)
	if t.lastDeploy != nil {
		// A deploy can take a while to go live after it was created.
		since = t.lastDeploy.Add(-time.Hour)
	}
	list, err := r.Deploys(ctx, s.Store.Client, o.Creds, t.target, since)
	if err != nil {
		return Result{}, err
	}
	res := Result{Seen: len(list)}
	for _, d := range list {
		n, live, err := s.record(ctx, t, d)
		if err != nil {
			return res, err
		}
		res.New += n
		res.Live += live
	}
	return res, nil
}

// record stores one deploy, and when it has just gone live, records
// deploy.detected and runs the hooks, all in one transaction.
func (s *Service) record(ctx context.Context, t target, d connectors.Deploy) (isNew, live int, err error) {
	if d.ID == "" || d.At.IsZero() {
		return 0, 0, nil
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		var prev *string
		if err := tx.QueryRow(ctx, `SELECT state FROM deploys WHERE target_id = $1 AND external_id = $2 FOR UPDATE`, t.id, d.ID).Scan(&prev); err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if prev != nil && *prev == d.State {
			return nil
		}
		if prev == nil {
			isNew = 1
		}
		var dep Deploy
		err := tx.QueryRow(ctx, `
			INSERT INTO deploys (workspace_id, target_id, site_id, brand_id, external_id, kind, state, commit_sha, ref, message, url, deployed_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			ON CONFLICT (target_id, external_id) DO UPDATE SET state = EXCLUDED.state, commit_sha = EXCLUDED.commit_sha,
				ref = EXCLUDED.ref, message = EXCLUDED.message, url = EXCLUDED.url, deployed_at = EXCLUDED.deployed_at
			RETURNING id, seen_at`,
			t.ws, t.id, t.site, t.brand, trunc(d.ID, 200), d.Kind, d.State, trunc(d.Commit, 80), trunc(d.Ref, 200),
			trunc(d.Message, 200), trunc(d.URL, 1000), d.At).Scan(&dep.ID, &dep.SeenAt)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE deploy_targets SET last_deploy_at = GREATEST(COALESCE(last_deploy_at, $2), $2) WHERE id = $1`, t.id, d.At); err != nil {
			return err
		}
		dep.WorkspaceID, dep.TargetID, dep.SiteID, dep.BrandID = t.ws, t.id, t.site, t.brand
		dep.ExternalID, dep.Kind, dep.State, dep.Commit, dep.Ref, dep.Message, dep.URL, dep.DeployedAt = d.ID, d.Kind, d.State, d.Commit, d.Ref, d.Message, d.URL, d.At
		var action string
		switch d.State {
		case connectors.DeployReady:
			action = "deploy.detected"
		case connectors.DeployFailed:
			action = "deploy.failed"
		default:
			return nil
		}
		brand := ""
		if t.brand != nil {
			brand = *t.brand
		}
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: t.ws, ActorType: "system", Action: action, TargetType: "deploy", TargetID: dep.ID,
			BrandID: brand, Data: map[string]any{"target_id": t.id, "site_id": t.site, "kind": d.Kind, "commit": dep.Commit, "ref": dep.Ref,
				"deployed_at": d.At.UTC().Format(time.RFC3339)}}); err != nil {
			return err
		}
		if d.State != connectors.DeployReady {
			return nil
		}
		live = 1
		for _, h := range s.OnReady {
			if err := h(ctx, tx, dep); err != nil {
				return err
			}
		}
		return nil
	})
	return isNew, live, err
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
