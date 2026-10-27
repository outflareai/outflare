// Package vendorsync keeps a workspace's mailboxes in step with its mailbox
// suppliers (Cheap Inboxes, and any other connector that implements
// connectors.MailboxVendor).
//
// For each connected supplier account it keeps a list of every mailbox the
// supplier holds (vendor_mailboxes), refreshed every 15 minutes and on
// webhooks. Importing a mailbox reads its password from the supplier and
// adds it through mailbox.Service.Import, the same path as a CSV import, so
// it gets the same SMTP, IMAP and DNS checks. Google refuses plain passwords
// over SMTP and IMAP, so for Google mailboxes the supplier is first asked
// for an app password, at the pace the supplier allows.
package vendorsync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/mailbox"
	"github.com/outflareai/outflare/engine/internal/secrets"
)

// Job kinds.
const (
	JobSync         = "vendor.sync"
	JobSyncMailbox  = "vendor.sync_mailbox"
	JobImport       = "vendor.import"
	JobAppPasswords = "vendor.app_passwords"
	JobWebhook      = "vendor.webhook"
)

// Import states of a supplier mailbox.
const (
	StateAvailable   = "available"
	StateQueued      = "queued"
	StateWaiting     = "waiting"
	StateAppPassword = "app_password"
	StateImported    = "imported"
	StateFailed      = "failed"
)

// Service runs supplier syncs and imports.
type Service struct {
	Pool  *pgxpool.Pool
	Box   *secrets.Box
	Store *connectors.Store
	Mail  *mailbox.Service
	// PublicURL is where suppliers can reach the engine's webhook. Webhooks
	// are registered only when it is https and not localhost; otherwise the
	// sync relies on polling.
	PublicURL string
	Log       *slog.Logger
	// SyncEvery is the polling interval. Default 15 minutes.
	SyncEvery time.Duration
	// AppPasswordPoll is how often pending app passwords are checked.
	// Default 1 minute.
	AppPasswordPoll time.Duration
}

// Register adds the job handlers.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(JobSync, s.syncJob)
	q.Handle(JobSyncMailbox, s.syncMailboxJob)
	q.Handle(JobImport, s.importJob)
	q.Handle(JobAppPasswords, s.appPasswordsJob)
	q.Handle(JobWebhook, s.webhookJob)
	q.Handle(JobPushConnect, s.pushConnectJob)
	q.Handle(JobPush, s.pushJob)
	q.Handle(JobPushDisconnect, s.pushDisconnectJob)
}

func (s *Service) syncEvery() time.Duration {
	if s.SyncEvery > 0 {
		return s.SyncEvery
	}
	return 15 * time.Minute
}

func (s *Service) appPasswordPoll() time.Duration {
	if s.AppPasswordPoll > 0 {
		return s.AppPasswordPoll
	}
	return time.Minute
}

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// ErrNotVendor means the integration is not a mailbox supplier.
var ErrNotVendor = errors.New("this integration is not a mailbox supplier")

// IsVendor reports whether a connector id is a mailbox supplier.
func (s *Service) IsVendor(connector string) bool {
	c, ok := s.Store.Registry.Get(connector)
	if !ok {
		return false
	}
	_, ok = c.(connectors.MailboxVendor)
	return ok
}

// Ensure starts syncing a supplier integration, unless it already is:
// the first full sync runs at once and a webhook is registered when the
// engine is reachable. Call it in the transaction that connected the
// integration, or lazily before reading its mailboxes. It reports whether
// syncing started now.
func (s *Service) Ensure(ctx context.Context, tx pgx.Tx, ws, integrationID string) (bool, error) {
	var connector string
	err := tx.QueryRow(ctx, `SELECT connector FROM integrations WHERE workspace_id = $1 AND id = $2`, ws, integrationID).Scan(&connector)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, connectors.ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if !s.IsVendor(connector) {
		return false, ErrNotVendor
	}
	tag, err := tx.Exec(ctx, `INSERT INTO vendor_syncs (integration_id, workspace_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, integrationID, ws)
	if err != nil || tag.RowsAffected() == 0 {
		return false, err
	}
	if err := s.queueSync(ctx, tx, ws, integrationID, time.Time{}); err != nil {
		return false, err
	}
	_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobWebhook, WorkspaceID: &ws, Payload: idPayload{integrationID}, MaxAttempts: 5,
		IdempotencyKey: integrationID})
	return true, err
}

type idPayload struct {
	IntegrationID string `json:"integration_id"`
}

type mailboxPayload struct {
	IntegrationID string `json:"integration_id"`
	ExternalID    string `json:"external_id"`
}

// queueSync schedules a full sync. A zero at means now. Scheduled syncs
// share a key per time slot so there is one per integration per slot.
// Syncs never schedule the next one themselves: Run does, so there is
// exactly one schedule however many syncs were asked for.
func (s *Service) queueSync(ctx context.Context, q jobs.Querier, ws, id string, at time.Time) error {
	j := jobs.NewJob{Kind: JobSync, WorkspaceID: &ws, Payload: idPayload{id}, MaxAttempts: 3}
	if !at.IsZero() {
		j.RunAt = at
		j.IdempotencyKey = fmt.Sprintf("%s:%d", id, at.UnixMilli()/s.syncEvery().Milliseconds())
	}
	_, err := jobs.Enqueue(ctx, q, j)
	return err
}

// SyncNow queues a full sync now.
func (s *Service) SyncNow(ctx context.Context, q jobs.Querier, ws, id string) error {
	return s.queueSync(ctx, q, ws, id, time.Time{})
}

// open loads the integration as a supplier.
func (s *Service) open(ctx context.Context, ws, id string) (*connectors.Opened, connectors.MailboxVendor, error) {
	o, err := s.Store.Open(ctx, s.Pool, ws, id)
	if err != nil {
		return nil, nil, err
	}
	v, ok := o.Connector.(connectors.MailboxVendor)
	if !ok {
		return nil, nil, ErrNotVendor
	}
	return o, v, nil
}

// Settings are how one supplier account is synced.
type Settings struct {
	IntegrationID string     `json:"integration_id"`
	AutoImport    bool       `json:"auto_import"`
	AppPasswords  bool       `json:"app_passwords"`
	DailyLimit    *int       `json:"daily_limit"`
	Webhook       bool       `json:"webhook"`
	LastSyncedAt  *time.Time `json:"last_synced_at"`
	LastSyncError *string    `json:"last_sync_error"`
	Push          PushStatus `json:"push"`
}

// ErrNotSynced means Ensure has not run for the integration.
var ErrNotSynced = errors.New("this supplier has not been synced yet")

// GetSettings reads an integration's sync settings.
func (s *Service) GetSettings(ctx context.Context, q connectors.Querier, ws, id string) (Settings, error) {
	st := Settings{IntegrationID: id}
	err := q.QueryRow(ctx, `
		SELECT auto_import, app_passwords, daily_limit, webhook_id IS NOT NULL, last_synced_at, last_sync_error
		FROM vendor_syncs WHERE workspace_id = $1 AND integration_id = $2`, ws, id,
	).Scan(&st.AutoImport, &st.AppPasswords, &st.DailyLimit, &st.Webhook, &st.LastSyncedAt, &st.LastSyncError)
	if errors.Is(err, pgx.ErrNoRows) {
		return st, ErrNotSynced
	}
	if err != nil {
		return st, err
	}
	st.Push, err = s.pushStatus(ctx, q, ws, id)
	return st, err
}

// Change is a partial settings update.
type Change struct {
	AutoImport   *bool
	AppPasswords *bool
	DailyLimit   *int // 0 clears it
	SetLimit     bool
}

// UpdateSettings applies a change in tx. Turning on auto-import queues
// every active mailbox not yet imported.
func (s *Service) UpdateSettings(ctx context.Context, tx pgx.Tx, ws, id string, c Change) (Settings, error) {
	var limit *int
	if c.SetLimit && c.DailyLimit != nil && *c.DailyLimit > 0 {
		limit = c.DailyLimit
	}
	tag, err := tx.Exec(ctx, `
		UPDATE vendor_syncs SET
			auto_import = COALESCE($3, auto_import),
			app_passwords = COALESCE($4, app_passwords),
			daily_limit = CASE WHEN $5 THEN $6 ELSE daily_limit END,
			updated_at = now()
		WHERE workspace_id = $1 AND integration_id = $2`, ws, id, c.AutoImport, c.AppPasswords, c.SetLimit, limit)
	if err != nil {
		return Settings{}, err
	}
	if tag.RowsAffected() == 0 {
		return Settings{}, ErrNotSynced
	}
	if c.AutoImport != nil && *c.AutoImport {
		if err := s.settle(ctx, tx, ws, id); err != nil {
			return Settings{}, err
		}
	}
	return s.GetSettings(ctx, tx, ws, id)
}

// settle moves mailboxes along after their supplier state changed: waiting
// ones the supplier has finished become queued, with auto-import new active
// ones are queued too, and imported mailboxes the supplier cancelled are
// paused. It queues an import when anything is waiting to import.
func (s *Service) settle(ctx context.Context, tx pgx.Tx, ws, id string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_mailboxes v SET import_state = 'queued', import_error = NULL, updated_at = now()
		FROM vendor_syncs y
		WHERE v.integration_id = $2 AND v.workspace_id = $1 AND y.integration_id = v.integration_id
		  AND v.status = 'active'
		  AND (v.import_state = 'waiting' OR (y.auto_import AND v.import_state = 'available'))`, ws, id); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		UPDATE mailboxes m SET status = 'paused', status_reason = 'Cancelled at the supplier', updated_at = now()
		FROM vendor_mailboxes v
		WHERE v.integration_id = $2 AND v.workspace_id = $1 AND v.mailbox_id = m.id AND m.workspace_id = $1
		  AND v.status = 'cancelled' AND m.status <> 'paused'
		RETURNING m.id, m.email`, ws, id)
	if err != nil {
		return err
	}
	type paused struct{ id, email string }
	var done []paused
	for rows.Next() {
		var p paused
		if err := rows.Scan(&p.id, &p.email); err != nil {
			rows.Close()
			return err
		}
		done = append(done, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, p := range done {
		if err := audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: "mailbox.paused",
			TargetType: "mailbox", TargetID: p.id,
			Data: map[string]any{"email": p.email, "reason": "cancelled at the supplier", "integration_id": id}}); err != nil {
			return err
		}
	}
	var queued bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vendor_mailboxes WHERE integration_id = $1 AND import_state = 'queued')`, id).Scan(&queued); err != nil {
		return err
	}
	if queued {
		_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobImport, WorkspaceID: &ws, Payload: idPayload{id}, MaxAttempts: 5})
		return err
	}
	return nil
}

// upsert stores one supplier mailbox as last seen.
func upsert(ctx context.Context, tx pgx.Tx, ws, id string, m connectors.VendorMailbox) error {
	var limit *int
	if m.DailyLimit > 0 {
		limit = &m.DailyLimit
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO vendor_mailboxes (integration_id, workspace_id, external_id, email, first_name, last_name,
			provider, status, vendor_status, daily_limit, tags, connected_to, seen_at)
		VALUES ($1, $2, $3, lower($4), $5, $6, $7, $8, $9, $10, $11, $12, now())
		ON CONFLICT (integration_id, external_id) DO UPDATE SET
			email = EXCLUDED.email, first_name = EXCLUDED.first_name, last_name = EXCLUDED.last_name,
			provider = EXCLUDED.provider, status = EXCLUDED.status, vendor_status = EXCLUDED.vendor_status,
			daily_limit = EXCLUDED.daily_limit, tags = EXCLUDED.tags, connected_to = EXCLUDED.connected_to,
			seen_at = now(),
			updated_at = CASE WHEN vendor_mailboxes.status IS DISTINCT FROM EXCLUDED.status
			                   OR vendor_mailboxes.email IS DISTINCT FROM EXCLUDED.email
			                  THEN now() ELSE vendor_mailboxes.updated_at END`,
		id, ws, m.ExternalID, m.Email, m.FirstName, m.LastName, m.Provider, m.Status, m.VendorStatus, limit, m.Tags, m.ConnectedTo)
	return err
}

// maxMailboxes bounds one supplier account.
const maxMailboxes = 50000

func (s *Service) syncJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[idPayload](job)
	if err != nil {
		return err
	}
	o, v, err := s.open(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil // disconnected: the loop ends here
	}
	if err != nil {
		return s.syncFailed(ctx, ws, p.IntegrationID, err)
	}
	started := time.Now()
	if _, err := s.Pool.Exec(ctx, `UPDATE vendor_syncs SET last_attempt_at = now() WHERE integration_id = $1`, p.IntegrationID); err != nil {
		return err
	}
	var all []connectors.VendorMailbox
	cursor := ""
	for {
		page, next, err := v.ListMailboxes(ctx, o.Client, o.Creds, connectors.VendorPage{Cursor: cursor})
		if err != nil {
			return s.syncFailed(ctx, ws, p.IntegrationID, err)
		}
		all = append(all, page...)
		if next == "" || next == cursor || len(all) >= maxMailboxes {
			break
		}
		cursor = next
	}
	err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for _, m := range all {
			if err := upsert(ctx, tx, ws, p.IntegrationID, m); err != nil {
				return err
			}
		}
		// Mailboxes the supplier no longer lists are gone.
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_mailboxes SET status = 'cancelled', updated_at = now()
			WHERE integration_id = $1 AND seen_at < $2 AND status <> 'cancelled'`, p.IntegrationID, started); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_syncs SET last_synced_at = now(), last_sync_error = NULL, updated_at = now()
			WHERE integration_id = $1`, p.IntegrationID); err != nil {
			return err
		}
		if err := s.Store.Checked(ctx, tx, ws, p.IntegrationID, &o.Account, nil); err != nil {
			return err
		}
		if err := s.settle(ctx, tx, ws, p.IntegrationID); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.log().Info("vendor sync", "integration", p.IntegrationID, "mailboxes", len(all), "ms", time.Since(started).Milliseconds())
	return nil
}

// syncFailed records a failed sync; the next scheduled one tries again, so
// a supplier outage heals on its own. Credential errors also mark the
// integration broken.
func (s *Service) syncFailed(ctx context.Context, ws, id string, cause error) error {
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE vendor_syncs SET last_sync_error = $2, updated_at = now() WHERE integration_id = $1`,
			id, cause.Error()); err != nil {
			return err
		}
		return s.Store.Checked(ctx, tx, ws, id, nil, cause)
	})
	if err != nil {
		return err
	}
	s.log().Warn("vendor sync failed", "integration", id, "err", cause)
	return nil
}

func (s *Service) syncMailboxJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[mailboxPayload](job)
	if err != nil {
		return err
	}
	o, v, err := s.open(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	m, err := v.GetMailbox(ctx, o.Client, o.Creds, p.ExternalID)
	if err != nil {
		if connectors.KindOf(err) == connectors.ErrInvalid {
			return nil // not found at the supplier; the next full sync settles it
		}
		return err
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if err := upsert(ctx, tx, ws, p.IntegrationID, m); err != nil {
			return err
		}
		return s.settle(ctx, tx, ws, p.IntegrationID)
	})
}

// Request selects mailboxes to import.
type Request struct {
	ExternalIDs []string
	Emails      []string
	// All imports every mailbox not imported yet.
	All bool
}

// Queued reports what Queue did.
type Queued struct {
	// Import now.
	Queued int `json:"queued"`
	// Import once the supplier finishes setting them up.
	Waiting int `json:"waiting"`
	// Asked for but not found at this supplier.
	NotFound []string `json:"not_found"`
}

// Queue marks mailboxes for import and queues the import job. Listed
// mailboxes are imported again even if they were before (to refresh their
// passwords); All takes only those not imported yet.
func (s *Service) Queue(ctx context.Context, tx pgx.Tx, ws, id string, r Request) (Queued, error) {
	out := Queued{NotFound: []string{}}
	ids := r.ExternalIDs
	if ids == nil {
		ids = []string{}
	}
	emails := make([]string, 0, len(r.Emails))
	for _, e := range r.Emails {
		emails = append(emails, strings.ToLower(strings.TrimSpace(e)))
	}
	rows, err := tx.Query(ctx, `
		UPDATE vendor_mailboxes SET
			import_state = CASE WHEN status IN ('active', 'cancelling') THEN 'queued' ELSE 'waiting' END,
			import_error = NULL, updated_at = now()
		WHERE workspace_id = $1 AND integration_id = $2
		  AND status NOT IN ('cancelled', 'failed')
		  AND (($3 AND import_state IN ('available', 'failed'))
		       OR external_id = ANY($4) OR email = ANY($5))
		RETURNING external_id, email, import_state`, ws, id, r.All, ids, emails)
	if err != nil {
		return out, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var ext, email, state string
		if err := rows.Scan(&ext, &email, &state); err != nil {
			rows.Close()
			return out, err
		}
		seen[ext], seen[email] = true, true
		if state == StateQueued {
			out.Queued++
		} else {
			out.Waiting++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	for _, x := range append(append([]string{}, ids...), emails...) {
		if !seen[x] {
			out.NotFound = append(out.NotFound, x)
		}
	}
	if out.Queued > 0 {
		_, err = jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobImport, WorkspaceID: &ws, Payload: idPayload{id}, MaxAttempts: 5})
	}
	return out, err
}

// importBatch is how many mailboxes one import job takes. Each needs one
// supplier call, and Cheap Inboxes allows 120 a minute.
const importBatch = 25

type pending struct {
	ExternalID, Email, FirstName, LastName, Provider, Status string
	DailyLimit                                               *int
}

func (s *Service) importJob(ctx context.Context, job jobs.Job) error {
	p, ws, err := payload[idPayload](job)
	if err != nil {
		return err
	}
	o, v, err := s.open(ctx, ws, p.IntegrationID)
	if errors.Is(err, connectors.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	st, err := s.GetSettings(ctx, s.Pool, ws, p.IntegrationID)
	if err != nil {
		return err
	}
	rows, err := s.Pool.Query(ctx, `
		SELECT external_id, email, first_name, last_name, provider, status, daily_limit
		FROM vendor_mailboxes WHERE integration_id = $1 AND workspace_id = $2 AND import_state = 'queued'
		ORDER BY email LIMIT $3`, p.IntegrationID, ws, importBatch)
	if err != nil {
		return err
	}
	batch, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
		var x pending
		err := r.Scan(&x.ExternalID, &x.Email, &x.FirstName, &x.LastName, &x.Provider, &x.Status, &x.DailyLimit)
		return x, err
	})
	if err != nil {
		return err
	}
	_, canAppPassword := v.(connectors.AppPasswordVendor)
	needAppPasswords := false
	for _, m := range batch {
		if m.Status != connectors.VendorActive && m.Status != connectors.VendorCancelling {
			if err := s.setState(ctx, p.IntegrationID, m.ExternalID, StateWaiting, ""); err != nil {
				return err
			}
			continue
		}
		creds, err := v.MailboxCredentials(ctx, o.Client, o.Creds, m.ExternalID)
		switch {
		case err == nil:
		case connectors.KindOf(err) == connectors.ErrInvalid:
			// Not ready at the supplier yet: try again when it is.
			if err := s.setState(ctx, p.IntegrationID, m.ExternalID, StateWaiting, err.Error()); err != nil {
				return err
			}
			continue
		case connectors.KindOf(err) == connectors.ErrAuth:
			_ = s.Store.Checked(ctx, s.Pool, ws, p.IntegrationID, nil, err)
			return fmt.Errorf("%w: %v", jobs.ErrPermanent, err)
		default:
			return err // retried with backoff
		}
		password := creds.AppPassword
		if password == "" && m.Provider == mailbox.ProviderGoogle && st.AppPasswords && canAppPassword {
			if err := s.setState(ctx, p.IntegrationID, m.ExternalID, StateAppPassword, ""); err != nil {
				return err
			}
			needAppPasswords = true
			continue
		}
		if password == "" {
			password = creds.Password
		}
		if err := s.importOne(ctx, ws, p.IntegrationID, o.Connector.Definition(), m, creds, password, st); err != nil {
			return err
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		if needAppPasswords {
			if err := s.queueAppPasswords(ctx, tx, ws, p.IntegrationID, time.Time{}); err != nil {
				return err
			}
		}
		if len(batch) == importBatch {
			_, err := jobs.Enqueue(ctx, tx, jobs.NewJob{Kind: JobImport, WorkspaceID: &ws, Payload: idPayload{p.IntegrationID}, MaxAttempts: 5})
			return err
		}
		return nil
	})
}

func (s *Service) setState(ctx context.Context, id, ext, state, reason string) error {
	_, err := s.Pool.Exec(ctx, `
		UPDATE vendor_mailboxes SET import_state = $3, import_error = NULLIF($4, ''), updated_at = now()
		WHERE integration_id = $1 AND external_id = $2`, id, ext, state, reason)
	return err
}

// importOne adds one supplier mailbox as an Outflare mailbox, through the
// same path as a CSV import: provider defaults, then a connection check and
// a DNS check in the background.
func (s *Service) importOne(ctx context.Context, ws, id string, def connectors.Definition, m pending, creds connectors.VendorCredentials, password string, st Settings) error {
	email := strings.ToLower(orDefault(creds.Email, m.Email))
	username := orDefault(creds.Username, email)
	row := mailbox.Row{
		Email: email, DisplayName: strings.TrimSpace(m.FirstName + " " + m.LastName), Provider: m.Provider,
		SMTPHost: creds.SMTPHost, SMTPPort: creds.SMTPPort, SMTPUsername: username, SMTPPassword: password,
		IMAPHost: creds.IMAPHost, IMAPPort: creds.IMAPPort, IMAPUsername: username, IMAPPassword: password,
	}
	switch {
	case st.DailyLimit != nil:
		row.DailyLimit = *st.DailyLimit
	case m.DailyLimit != nil && *m.DailyLimit > 0 && *m.DailyLimit <= 500:
		row.DailyLimit = *m.DailyLimit
	}
	test, err := mailbox.IsTestWorkspace(ctx, s.Pool, ws)
	if err != nil {
		return err
	}
	good, bad := s.Mail.Prepare(ctx, test, []mailbox.Row{row})
	if len(bad) > 0 {
		return s.setState(ctx, id, m.ExternalID, StateFailed, bad[0].Error)
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		res, err := s.Mail.Import(ctx, tx, ws, def.ID, good)
		if err != nil {
			return err
		}
		if len(res.IDs) != 1 {
			return fmt.Errorf("import of %s returned %d mailboxes", email, len(res.IDs))
		}
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_mailboxes SET import_state = 'imported', import_error = NULL, mailbox_id = $3,
				imported_at = now(), updated_at = now()
			WHERE integration_id = $1 AND external_id = $2`, id, m.ExternalID, res.IDs[0]); err != nil {
			return err
		}
		action := "mailbox.imported"
		if res.Updated > 0 {
			action = "mailbox.reimported"
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: ws, ActorType: "system", Action: action,
			TargetType: "mailbox", TargetID: res.IDs[0],
			Data: map[string]any{"email": email, "source": def.ID, "integration_id": id, "app_password": password == creds.AppPassword && password != ""}})
	})
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func payload[T any](job jobs.Job) (T, string, error) {
	var p T
	if job.WorkspaceID == nil {
		return p, "", fmt.Errorf("%w: no workspace", jobs.ErrPermanent)
	}
	if err := jsonUnmarshal(job.Payload, &p); err != nil {
		return p, "", fmt.Errorf("%w: bad payload", jobs.ErrPermanent)
	}
	return p, *job.WorkspaceID, nil
}

// WebhookURL is where a supplier posts events for an integration, or ""
// when the engine is not reachable from the internet.
func (s *Service) WebhookURL(integrationID string) string {
	b := s.publicBase()
	if b == "" {
		return ""
	}
	return b + "/v1/integrations/" + integrationID + "/webhook"
}

// publicBase is PUBLIC_URL without a trailing slash when a supplier on the
// internet can reach it (https, not localhost), else "".
func (s *Service) publicBase() string {
	b := strings.TrimRight(s.PublicURL, "/")
	u, err := url.Parse(b)
	if err != nil || u.Scheme != "https" {
		return ""
	}
	h := u.Hostname()
	if h == "localhost" || h == "127.0.0.1" || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".localhost") {
		return ""
	}
	return b
}
