package vendorsync

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// webhookJob subscribes to the supplier's events when the engine can be
// reached from the internet. Without it, the 15-minute sync still catches
// every change.
func (s *Service) webhookJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[idPayload](job)
	if err != nil {
		return err
	}
	hookURL := s.WebhookURL(p.IntegrationID)
	if hookURL == "" {
		return nil
	}
	o, v, err := s.open(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	wv, ok := v.(connectors.WebhookVendor)
	if !ok {
		return nil
	}
	var has bool
	if err := s.Pool.QueryRow(ctx, `SELECT webhook_id IS NOT NULL FROM vendor_syncs WHERE integration_id = $1`, p.IntegrationID).Scan(&has); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if has {
		return nil
	}
	hookID, secret, err := wv.RegisterWebhook(ctx, o.Client, o.Creds, hookURL)
	if err != nil {
		return err
	}
	sealed := s.Box.Seal([]byte(secret), []byte(p.IntegrationID))
	tag, err := s.Pool.Exec(ctx, `UPDATE vendor_syncs SET webhook_id = $2, webhook_secret = $3, updated_at = now()
		WHERE integration_id = $1 AND webhook_id IS NULL`, p.IntegrationID, hookID, sealed)
	if err == nil && tag.RowsAffected() == 0 {
		// Disconnected (or registered twice) meanwhile: remove this one.
		_ = wv.DeleteWebhook(ctx, o.Client, o.Creds, hookID)
	}
	return err
}

// Disconnect removes the supplier's webhook before the integration is
// deleted. It is best effort: an unreachable supplier must not stop
// someone disconnecting, and a leftover webhook only gets 404s from us.
func (s *Service) Disconnect(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
	var hookID, pushID, pushToken *string
	err := tx.QueryRow(ctx, `SELECT webhook_id, push_remote_id, push_token_id FROM vendor_syncs WHERE integration_id = $1`, i.ID).Scan(&hookID, &pushID, &pushToken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	// The push token dies with the connection.
	if err := revokePushToken(ctx, tx, i.WorkspaceID, pushToken); err != nil {
		return err
	}
	if hookID == nil && pushID == nil {
		return nil
	}
	o, err := s.Store.Open(ctx, tx, i.WorkspaceID, i.ID)
	if err != nil {
		return nil
	}
	if pv, ok := o.Connector.(connectors.PushVendor); ok && pushID != nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := pv.DisconnectPush(cctx, o.Client, o.Creds, *pushID); err != nil {
			s.log().Warn("could not remove the supplier push connection", "integration", i.ID, "err", err)
		}
		cancel()
	}
	if wv, ok := o.Connector.(connectors.WebhookVendor); ok && hookID != nil {
		cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := wv.DeleteWebhook(cctx, o.Client, o.Creds, *hookID); err != nil {
			s.log().Warn("could not delete the supplier webhook", "integration", i.ID, "err", err)
		}
	}
	return nil
}

// ErrBadSignature means a webhook delivery failed verification.
var ErrBadSignature = errors.New("webhook signature does not match")

// HandleWebhook verifies and records one delivery, and queues the work it
// implies. Duplicate deliveries are accepted and ignored.
func (s *Service) HandleWebhook(ctx context.Context, integrationID string, h http.Header, body []byte) error {
	var ws, connector string
	var sealed []byte
	err := s.Pool.QueryRow(ctx, `
		SELECT y.workspace_id, i.connector, y.webhook_secret
		FROM vendor_syncs y JOIN integrations i ON i.id = y.integration_id
		WHERE y.integration_id = $1 AND y.webhook_secret IS NOT NULL`, integrationID).Scan(&ws, &connector, &sealed)
	if errors.Is(err, pgx.ErrNoRows) {
		return connectors.ErrNotFound
	}
	if err != nil {
		return err
	}
	c, ok := s.Store.Registry.Get(connector)
	if !ok {
		return connectors.ErrNotFound
	}
	wv, ok := c.(connectors.WebhookVendor)
	if !ok {
		return connectors.ErrNotFound
	}
	secret, err := s.Box.Open(sealed, []byte(integrationID))
	if err != nil {
		return err
	}
	ev, err := wv.ParseWebhook(string(secret), h, body)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrAuth {
			return ErrBadSignature
		}
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `INSERT INTO vendor_webhook_events (integration_id, event_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			integrationID, ev.ID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if ev.MailboxExternalID != "" {
			_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobSyncMailbox, WorkspaceID: &ws, MaxAttempts: 5,
				Payload: mailboxPayload{integrationID, ev.MailboxExternalID}})
			if err != nil {
				return err
			}
		}
		if ev.Resync {
			return s.queueSync(ctx, tx, ws, integrationID, time.Time{})
		}
		return nil
	})
}

// Connected starts syncing a newly connected supplier. Other connectors
// are ignored. Register it as a connectors.Store OnConnect hook.
func (s *Service) Connected(ctx context.Context, tx pgx.Tx, i connectors.Integration) error {
	if !s.IsVendor(i.Connector) {
		return nil
	}
	_, err := s.Ensure(ctx, tx, i.WorkspaceID, i.ID)
	return err
}
