package platforms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// JobWarmup brings one mailbox's warm-up on one platform in line with
// what Outflare says it should be.
const JobWarmup = "platforms.warmup"

type warmupPayload struct {
	WorkspaceID   string `json:"workspace_id"`
	MailboxID     string `json:"mailbox_id"`
	IntegrationID string `json:"integration_id"`
}

// RequestWarmup queues a job that switches the platform's warm-up for a
// mailbox on or off to match the mailbox: on while warm-up is enabled and
// runs on that platform, off otherwise. Call it in the transaction that
// changes the mailbox, for the platform it uses now and the one it used
// before.
func RequestWarmup(ctx context.Context, q jobs.Querier, ws, mailboxID, integrationID string) error {
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{Kind: JobWarmup, WorkspaceID: &ws,
		Payload: warmupPayload{ws, mailboxID, integrationID}, MaxAttempts: 5})
	return err
}

func (s *Service) warmupJob(ctx context.Context, job jobs.Job) error {
	var p warmupPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	err := s.SetWarmup(ctx, p.WorkspaceID, p.MailboxID, p.IntegrationID)
	if err != nil && connectors.KindOf(err) != connectors.ErrUnavailable && connectors.KindOf(err) != connectors.ErrQuota {
		return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
	}
	return err
}

// SetWarmup switches warm-up for one mailbox on one platform to what the
// mailbox says now, so jobs that run late or out of order still end right.
func (s *Service) SetWarmup(ctx context.Context, ws, mailboxID, integrationID string) error {
	var on bool
	var m connectors.MailboxState
	err := s.Pool.QueryRow(ctx, `
		SELECT m.warmup_enabled AND m.warmup_integration_id IS NOT DISTINCT FROM $3::uuid, p.external_id, m.email
		FROM mailboxes m JOIN mailbox_platforms p ON p.mailbox_id = m.id AND p.integration_id = $3
		WHERE m.id = $1 AND m.workspace_id = $2`, mailboxID, ws, integrationID).Scan(&on, &m.ExternalID, &m.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // the mailbox or the platform's copy of it is gone
	}
	if err != nil {
		return err
	}
	op, err := s.Store.Open(ctx, s.Pool, ws, integrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	ctl, ok := op.Connector.(connectors.WarmupController)
	if !ok {
		return nil
	}
	if err := ctl.SetWarmup(ctx, op.Client, op.Creds, []connectors.MailboxState{m}, on); err != nil {
		_ = s.Store.Checked(ctx, s.Pool, ws, integrationID, nil, err)
		s.Log.Warn("platform warm-up", "integration", integrationID, "mailbox", m.Email, "on", on, "err", err)
		return err
	}
	// Read back soon, so the mailbox shows what the platform now reports.
	return RequestSync(ctx, s.Pool, ws, integrationID, 0)
}
