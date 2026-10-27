package vendorsync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
)

// Push lets the supplier send mailboxes into Outflare through the
// EmailBison-compatible API, the way it sends them to a sequencer. Turning
// it on mints an API token, and a job adds the connection at the supplier
// with it. Pushing then asks the supplier to send chosen mailboxes.

// Job kinds for push.
const (
	JobPushConnect    = "vendor.push_connect"
	JobPush           = "vendor.push"
	JobPushDisconnect = "vendor.push_disconnect"
)

// Push states.
const (
	PushOff        = "off"
	PushConnecting = "connecting"
	PushOn         = "on"
	PushFailed     = "failed"
)

// Push errors.
var (
	ErrPushUnavailable = errors.New("push needs a supplier that supports it and an Outflare address the supplier can reach (PUBLIC_URL, https)")
	ErrPushNotOn       = errors.New("push is not on for this supplier")
	ErrPushBusy        = errors.New("push is already on or being connected")
)

// PushStatus is the push side of Settings.
type PushStatus struct {
	// Available is whether push can be turned on: the supplier supports it
	// and the engine has a public https address.
	Available    bool                   `json:"available"`
	State        string                 `json:"state"`
	BaseURL      *string                `json:"base_url"`
	ConnectedAt  *time.Time             `json:"connected_at"`
	LastPushedAt *time.Time             `json:"last_pushed_at"`
	LastResult   *connectors.PushResult `json:"last_result"`
	Error        *string                `json:"error"`
}

// CompatURL is the EmailBison-compatible base URL for a workspace, or ""
// when the engine cannot be reached from the internet.
func (s *Service) CompatURL(ws string) string {
	b := s.publicBase()
	if b == "" {
		return ""
	}
	return b + "/emailbison/" + ws
}

// CanPush reports whether a connector supports push.
func (s *Service) CanPush(connector string) bool {
	c, ok := s.Store.Registry.Get(connector)
	if !ok {
		return false
	}
	_, ok = c.(connectors.PushVendor)
	return ok
}

func (s *Service) pushStatus(ctx context.Context, q connectors.Querier, ws, id string) (PushStatus, error) {
	var p PushStatus
	var connector string
	var last []byte
	err := q.QueryRow(ctx, `
		SELECT i.connector, y.push_state, y.push_connected_at, y.push_last_at, y.push_last_result, y.push_error
		FROM vendor_syncs y JOIN integrations i ON i.id = y.integration_id
		WHERE y.workspace_id = $1 AND y.integration_id = $2`, ws, id,
	).Scan(&connector, &p.State, &p.ConnectedAt, &p.LastPushedAt, &last, &p.Error)
	if err != nil {
		return p, err
	}
	if u := s.CompatURL(ws); u != "" && s.CanPush(connector) {
		p.Available = true
		p.BaseURL = &u
	}
	if len(last) > 0 {
		var r connectors.PushResult
		if json.Unmarshal(last, &r) == nil {
			p.LastResult = &r
		}
	}
	return p, nil
}

// EnablePush turns push on in tx: it stores the token the supplier will use
// (sealed) and queues the job that adds the connection. tokenID and secret
// are a token the caller minted in the same transaction.
func (s *Service) EnablePush(ctx context.Context, tx pgx.Tx, ws, id, tokenID, secret, loginEmail string) error {
	var connector, state string
	err := tx.QueryRow(ctx, `
		SELECT i.connector, y.push_state FROM vendor_syncs y JOIN integrations i ON i.id = y.integration_id
		WHERE y.workspace_id = $1 AND y.integration_id = $2 FOR UPDATE OF y`, ws, id).Scan(&connector, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotSynced
	}
	if err != nil {
		return err
	}
	if !s.CanPush(connector) || s.CompatURL(ws) == "" {
		return ErrPushUnavailable
	}
	if state == PushOn || state == PushConnecting {
		return ErrPushBusy
	}
	sealed := s.Box.Seal([]byte(secret), []byte(id))
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_syncs SET push_state = 'connecting', push_token_id = $3, push_token_sealed = $4,
			push_login_email = $5, push_error = NULL, push_remote_id = NULL, updated_at = now()
		WHERE workspace_id = $1 AND integration_id = $2`, ws, id, tokenID, sealed, loginEmail); err != nil {
		return err
	}
	// One attempt: adding a connection twice would leave a duplicate at the
	// supplier. A failure is shown, and turning push on again retries.
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobPushConnect, WorkspaceID: &ws, Payload: idPayload{id},
		MaxAttempts: 1, IdempotencyKey: id + ":" + tokenID})
	return err
}

// DisablePush turns push off in tx: the token is revoked at once and a job
// removes the connection at the supplier. Mailboxes already pushed stay.
func (s *Service) DisablePush(ctx context.Context, tx pgx.Tx, ws, id string) error {
	var remote, token *string
	err := tx.QueryRow(ctx, `
		SELECT push_remote_id, push_token_id FROM vendor_syncs
		WHERE workspace_id = $1 AND integration_id = $2 FOR UPDATE`, ws, id).Scan(&remote, &token)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotSynced
	}
	if err != nil {
		return err
	}
	if err := revokePushToken(ctx, tx, ws, token); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_syncs SET push_state = 'off', push_remote_id = NULL, push_token_id = NULL, push_token_sealed = NULL,
			push_error = NULL, push_connected_at = NULL, updated_at = now()
		WHERE workspace_id = $1 AND integration_id = $2`, ws, id); err != nil {
		return err
	}
	if remote == nil {
		return nil
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobPushDisconnect, WorkspaceID: &ws,
		Payload: pushDisconnectPayload{IntegrationID: id, RemoteID: *remote}, MaxAttempts: 5, IdempotencyKey: id + ":" + *remote})
	return err
}

func revokePushToken(ctx context.Context, tx pgx.Tx, ws string, token *string) error {
	if token == nil {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE api_tokens SET revoked_at = now() WHERE workspace_id = $1 AND id = $2 AND revoked_at IS NULL`, ws, *token)
	return err
}

// PushNow queues a push of these supplier mailboxes (by external id), or,
// with all, of every active one not yet in Outflare. It returns how many.
func (s *Service) PushNow(ctx context.Context, tx pgx.Tx, ws, id string, all bool, externalIDs []string) (int, error) {
	var state string
	err := tx.QueryRow(ctx, `SELECT push_state FROM vendor_syncs WHERE workspace_id = $1 AND integration_id = $2`, ws, id).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotSynced
	}
	if err != nil {
		return 0, err
	}
	if state != PushOn {
		return 0, ErrPushNotOn
	}
	var ids []string
	if all {
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(external_id ORDER BY email), '{}') FROM vendor_mailboxes
			WHERE workspace_id = $1 AND integration_id = $2 AND status = 'active' AND mailbox_id IS NULL`, ws, id).Scan(&ids)
	} else {
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(array_agg(external_id ORDER BY email), '{}') FROM vendor_mailboxes
			WHERE workspace_id = $1 AND integration_id = $2 AND external_id = ANY($3) AND status <> 'cancelled'`, ws, id, externalIDs).Scan(&ids)
	}
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobPush, WorkspaceID: &ws, Payload: pushPayload{IntegrationID: id, ExternalIDs: ids}, MaxAttempts: 3})
	return len(ids), err
}

type pushPayload struct {
	IntegrationID string   `json:"integration_id"`
	ExternalIDs   []string `json:"external_ids"`
}

type pushDisconnectPayload struct {
	IntegrationID string `json:"integration_id"`
	RemoteID      string `json:"remote_id"`
}

func (s *Service) openPush(ctx context.Context, ws, id string) (*connectors.Opened, connectors.PushVendor, error) {
	o, v, err := s.open(ctx, ws, id)
	if err != nil {
		return nil, nil, err
	}
	pv, ok := v.(connectors.PushVendor)
	if !ok {
		return nil, nil, fmt.Errorf("%w: %s cannot push", jobs.ErrPermanent, o.Integration.Connector)
	}
	return o, pv, nil
}

func (s *Service) pushConnectJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[idPayload](job)
	if err != nil {
		return err
	}
	var state string
	var sealed []byte
	var login *string
	err = s.Pool.QueryRow(ctx, `SELECT push_state, push_token_sealed, push_login_email FROM vendor_syncs WHERE integration_id = $1`,
		p.IntegrationID).Scan(&state, &sealed, &login)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != PushConnecting || len(sealed) == 0 {
		return nil // turned off meanwhile
	}
	fail := func(cause error) error {
		s.log().Warn("supplier push connection failed", "integration", p.IntegrationID, "err", cause)
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			var token *string
			err := tx.QueryRow(ctx, `SELECT push_token_id FROM vendor_syncs WHERE integration_id = $1 AND push_state = 'connecting' FOR UPDATE`,
				p.IntegrationID).Scan(&token)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // turned off meanwhile
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE vendor_syncs SET push_state = 'failed', push_error = $2, push_token_sealed = NULL, push_token_id = NULL, updated_at = now()
				WHERE integration_id = $1`, p.IntegrationID, cause.Error()); err != nil {
				return err
			}
			if err := revokePushToken(ctx, tx, ws, token); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "integration.push_failed",
				TargetType: "integration", TargetID: p.IntegrationID, Data: map[string]any{"kind": connectors.KindOf(cause)}})
		})
	}
	o, pv, err := s.openPush(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return fail(err)
	}
	secret, err := s.Box.Open(sealed, []byte(p.IntegrationID))
	if err != nil {
		return fail(errors.New("the push token could not be read; turn push on again"))
	}
	base := s.CompatURL(ws)
	if base == "" {
		return fail(ErrPushUnavailable)
	}
	email := "push@outflare.ai"
	if login != nil && *login != "" {
		email = *login
	}
	remote, err := pv.ConnectPush(ctx, o.Client, o.Creds, connectors.PushTarget{
		Name: "Outflare", BaseURL: base, APIKey: string(secret), LoginEmail: email, LoginPassword: randomPassword(),
	})
	if err != nil {
		return fail(err)
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE vendor_syncs SET push_state = 'on', push_remote_id = $2, push_token_sealed = NULL, push_error = NULL,
				push_connected_at = now(), updated_at = now()
			WHERE integration_id = $1 AND push_state = 'connecting'`, p.IntegrationID, remote)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			// Turned off while connecting: remove what was just added.
			_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobPushDisconnect, WorkspaceID: &ws,
				Payload: pushDisconnectPayload{IntegrationID: p.IntegrationID, RemoteID: remote}, MaxAttempts: 5})
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "integration.push_connected",
			TargetType: "integration", TargetID: p.IntegrationID, Data: map[string]any{"connector": o.Integration.Connector}})
	})
}

func (s *Service) pushJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[pushPayload](job)
	if err != nil {
		return err
	}
	var state string
	var remote *string
	err = s.Pool.QueryRow(ctx, `SELECT push_state, push_remote_id FROM vendor_syncs WHERE integration_id = $1`, p.IntegrationID).Scan(&state, &remote)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state != PushOn || remote == nil {
		return nil
	}
	o, pv, err := s.openPush(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	res, err := pv.PushMailboxes(ctx, o.Client, o.Creds, *remote, p.ExternalIDs)
	if err != nil {
		_, _ = s.Pool.Exec(ctx, `UPDATE vendor_syncs SET push_error = $2, updated_at = now() WHERE integration_id = $1`, p.IntegrationID, err.Error())
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		raw, _ := json.Marshal(res)
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_syncs SET push_last_at = now(), push_last_result = $2, push_error = NULL, updated_at = now()
			WHERE integration_id = $1`, p.IntegrationID, raw); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "integration.mailboxes_pushed",
			TargetType: "integration", TargetID: p.IntegrationID,
			Data: map[string]any{"requested": len(p.ExternalIDs), "synced": res.Synced, "failed": res.Failed, "skipped": res.Skipped}})
	})
}

func (s *Service) pushDisconnectJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[pushDisconnectPayload](job)
	if err != nil {
		return err
	}
	o, pv, err := s.openPush(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil // disconnected: Disconnect already tried
	}
	if err != nil {
		return err
	}
	return pv.DisconnectPush(ctx, o.Client, o.Creds, p.RemoteID)
}

// randomPassword fills the supplier's required sequencer login. Outflare
// has no sequencer UI to sign in to, so it is never kept or used.
func randomPassword() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
