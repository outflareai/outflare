// Package tgsession signs company Telegram accounts in to Outflare as a
// linked device and keeps an eye on them. Someone scans a QR code once;
// after that the sealed session is only used to read. The Telegram app
// (api_id and api_hash) is always the workspace's own connection.
package tgsession

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/connectors/tgapp"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/metrics"
	"github.com/outflareai/outflare/engine/internal/research"
	"github.com/outflareai/outflare/engine/internal/secrets"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Job kinds.
const (
	KindLogin    = "telegram.login"
	KindPassword = "telegram.password"
	KindCheck    = "telegram.check"
	KindLogout   = "telegram.logout"
)

// LoginWindow is how long one QR sign-in waits for a scan.
const LoginWindow = 3 * time.Minute

// CheckEvery is how often a connected account is checked.
const CheckEvery = 6 * time.Hour

// Error codes stored on a session (error_code), for the page to explain.
const (
	CodeWrongPassword = "wrong_password"
	CodeSignedOut     = "signed_out"
	CodeBadApp        = "bad_app"
	CodeNoApp         = "no_app"
	CodeUnreachable   = "unreachable"
)

// Payload is every job's payload.
type Payload struct {
	WorkspaceID string `json:"workspace_id"`
	AccountID   string `json:"account_id"`
	Attempt     int    `json:"attempt"`
}

// Service runs the jobs.
type Service struct {
	Pool     *pgxpool.Pool
	Box      *secrets.Box
	Store    *connectors.Store
	Telegram Telegram
	// Models is the workspace's own AI, for matching chats; nil matches
	// only exactly.
	Models research.Models
	Log    *slog.Logger
}

// Register adds the job handlers.
func (s *Service) Register(q *jobs.Queue) {
	q.Handle(KindLogin, s.login)
	q.Handle(KindPassword, s.password)
	q.Handle(KindCheck, s.check)
	q.Handle(KindLogout, s.logout)
	q.Handle(KindSync, s.sync)
	q.Handle(KindMatch, s.match)
	q.Handle(KindSummarize, s.summarize)
}

// Enqueue queues one of the jobs for an account, in the caller's
// transaction.
func Enqueue(ctx context.Context, q jobs.Querier, kind, ws, account string, attempt int, at time.Time) error {
	key := ""
	if kind == KindCheck {
		// One check per account at a time.
		key = account + ":" + at.UTC().Truncate(time.Hour).Format(time.RFC3339)
	}
	_, err := jobs.Enqueue(ctx, q, jobs.NewJob{
		Kind: kind, WorkspaceID: &ws, RunAt: at, IdempotencyKey: key, MaxAttempts: 3,
		Payload: Payload{WorkspaceID: ws, AccountID: account, Attempt: attempt},
	})
	return err
}

type row struct {
	integration *string
	status      string
	attempt     int
	session     []byte
	password    []byte
}

func (s *Service) load(ctx context.Context, p Payload) (row, error) {
	var r row
	err := s.Pool.QueryRow(ctx, `SELECT integration_id::text, status, attempt, session_sealed, password_sealed
		FROM telegram_sessions WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID).
		Scan(&r.integration, &r.status, &r.attempt, &r.session, &r.password)
	return r, err
}

func (s *Service) app(ctx context.Context, ws string, integration *string) (tgapp.App, error) {
	if integration == nil {
		return tgapp.App{}, errNoApp
	}
	o, err := s.Store.Open(ctx, s.Pool, ws, *integration)
	if err != nil {
		if errors.Is(err, connectors.ErrNotFound) {
			return tgapp.App{}, errNoApp
		}
		return tgapp.App{}, err
	}
	if o.Connector.Definition().ID != tgapp.ID {
		return tgapp.App{}, errNoApp
	}
	return tgapp.Parse(o.Creds)
}

var errNoApp = errors.New("no Telegram app connection")

func (s *Service) open(sealed []byte, account string) ([]byte, error) {
	if len(sealed) == 0 {
		return nil, nil
	}
	return s.Box.Open(sealed, []byte(account))
}

func (s *Service) seal(plain []byte, account string) []byte {
	if len(plain) == 0 {
		return nil
	}
	return s.Box.Seal(plain, []byte(account))
}

func payload(j jobs.Job) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(j.Payload, &p); err != nil || p.WorkspaceID == "" || p.AccountID == "" {
		return p, fmt.Errorf("%w: bad telegram job payload", jobs.ErrPermanent)
	}
	return p, nil
}

func codeOf(err error) string {
	switch {
	case errors.Is(err, ErrWrongPassword):
		return CodeWrongPassword
	case errors.Is(err, ErrSignedOut):
		return CodeSignedOut
	case errors.Is(err, ErrBadApp):
		return CodeBadApp
	case errors.Is(err, errNoApp), errors.Is(err, connectors.ErrNotFound):
		return CodeNoApp
	}
	var ce *connectors.Error
	if errors.As(err, &ce) {
		return CodeBadApp
	}
	return CodeUnreachable
}

// fail records a step that broke, on the row and as an event.
func (s *Service) fail(ctx context.Context, p Payload, step, status string, err error) error {
	code := codeOf(err)
	metrics.TelegramStep(step, code)
	track.Fail(ctx, s.Log, track.Event{Name: "telegram_account.check_failed", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"step": step, "reason": code}}, "connector_unavailable", err)
	_, uerr := s.Pool.Exec(ctx, `UPDATE telegram_sessions SET status = $4, error_code = $5, login_url = NULL, updated_at = now()
		WHERE workspace_id = $1 AND account_id = $2 AND attempt = $3`, p.WorkspaceID, p.AccountID, p.Attempt, status, code)
	return uerr
}

var errStale = errors.New("a newer sign-in started")

// login shows QR codes until someone scans one with the account's phone.
func (s *Service) login(ctx context.Context, j jobs.Job) error {
	p, err := payload(j)
	if err != nil {
		return err
	}
	r, err := s.load(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (r.attempt != p.Attempt || r.status != "starting" && r.status != "waiting_scan")) {
		return nil
	}
	if err != nil {
		return err
	}
	app, err := s.app(ctx, p.WorkspaceID, r.integration)
	if err != nil {
		return s.fail(ctx, p, "login", "failed", err)
	}
	stored, err := s.open(r.session, p.AccountID)
	if err != nil {
		return s.fail(ctx, p, "login", "failed", err)
	}
	wait, cancel := context.WithTimeout(ctx, LoginWindow)
	defer cancel()
	res, err := s.Telegram.QRLogin(wait, app, stored, func(url string, expires time.Time) error {
		tag, err := s.Pool.Exec(ctx, `UPDATE telegram_sessions SET status = 'waiting_scan', login_url = $4, login_expires_at = $5, error_code = NULL, updated_at = now()
			WHERE workspace_id = $1 AND account_id = $2 AND attempt = $3 AND status IN ('starting', 'waiting_scan')`,
			p.WorkspaceID, p.AccountID, p.Attempt, url, expires)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errStale
		}
		return nil
	})
	switch {
	case errors.Is(err, errStale):
		return nil
	case err != nil && wait.Err() != nil && ctx.Err() == nil:
		metrics.TelegramStep("login", "expired")
		_, err = s.Pool.Exec(ctx, `UPDATE telegram_sessions SET status = 'expired', login_url = NULL, session_sealed = $4, updated_at = now()
			WHERE workspace_id = $1 AND account_id = $2 AND attempt = $3 AND status IN ('starting', 'waiting_scan')`,
			p.WorkspaceID, p.AccountID, p.Attempt, s.seal(res.Session, p.AccountID))
		return err
	case err != nil:
		return s.fail(ctx, p, "login", "failed", err)
	}
	return s.finish(ctx, p, "login", res)
}

// finish stores the outcome of a sign-in step.
func (s *Service) finish(ctx context.Context, p Payload, step string, res Result) error {
	sealed := s.seal(res.Session, p.AccountID)
	if res.NeedPassword {
		metrics.TelegramStep(step, "needs_password")
		_, err := s.Pool.Exec(ctx, `UPDATE telegram_sessions SET status = 'needs_password', password_hint = $4, session_sealed = $5,
			password_sealed = NULL, login_url = NULL, error_code = NULL, updated_at = now()
			WHERE workspace_id = $1 AND account_id = $2 AND attempt = $3`, p.WorkspaceID, p.AccountID, p.Attempt, res.PasswordHint, sealed)
		return err
	}
	if res.Self == nil {
		return fmt.Errorf("telegram %s ended without an account", step)
	}
	metrics.TelegramStep(step, "connected")
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE telegram_sessions SET status = 'connected', session_sealed = $4, password_sealed = NULL, login_url = NULL,
			login_expires_at = NULL, password_hint = '', telegram_user_id = $5, telegram_username = NULLIF($6, ''), telegram_phone = NULLIF($7, ''),
			telegram_name = NULLIF($8, ''), error_code = NULL, connected_at = now(), updated_at = now()
			WHERE workspace_id = $1 AND account_id = $2 AND attempt = $3`,
			p.WorkspaceID, p.AccountID, p.Attempt, sealed, res.Self.ID, res.Self.Username, res.Self.Phone, res.Self.Name)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if err := Enqueue(ctx, tx, KindCheck, p.WorkspaceID, p.AccountID, p.Attempt, time.Now()); err != nil {
			return err
		}
		return audit.Record(ctx, tx, audit.Event{WorkspaceID: p.WorkspaceID, ActorType: "system", Action: "telegram_account.connected",
			TargetType: "staff_account", TargetID: p.AccountID, Data: map[string]any{"telegram_user_id": strconv.FormatInt(res.Self.ID, 10)}})
	})
}

// password checks the two-step password someone typed, then forgets it.
func (s *Service) password(ctx context.Context, j jobs.Job) error {
	p, err := payload(j)
	if err != nil {
		return err
	}
	r, err := s.load(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (r.attempt != p.Attempt || r.status != "checking_password")) {
		return nil
	}
	if err != nil {
		return err
	}
	// The password is used once: cleared before anything else can fail.
	if _, err := s.Pool.Exec(ctx, `UPDATE telegram_sessions SET password_sealed = NULL WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID); err != nil {
		return err
	}
	pw, err := s.open(r.password, p.AccountID)
	if err != nil || len(pw) == 0 {
		return s.fail(ctx, p, "password", "needs_password", ErrWrongPassword)
	}
	app, err := s.app(ctx, p.WorkspaceID, r.integration)
	if err != nil {
		return s.fail(ctx, p, "password", "failed", err)
	}
	stored, err := s.open(r.session, p.AccountID)
	if err != nil {
		return s.fail(ctx, p, "password", "failed", err)
	}
	res, err := s.Telegram.Password(ctx, app, stored, string(pw))
	if errors.Is(err, ErrWrongPassword) {
		return s.fail(ctx, p, "password", "needs_password", err)
	}
	if err != nil {
		return s.fail(ctx, p, "password", "failed", err)
	}
	return s.finish(ctx, p, "password", res)
}

// check reads who the account is and how it is protected.
func (s *Service) check(ctx context.Context, j jobs.Job) error {
	p, err := payload(j)
	if err != nil {
		return err
	}
	r, err := s.load(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && r.status != "connected") {
		return nil
	}
	if err != nil {
		return err
	}
	p.Attempt = r.attempt
	app, err := s.app(ctx, p.WorkspaceID, r.integration)
	if err != nil {
		return s.fail(ctx, p, "check", "connected", err)
	}
	stored, err := s.open(r.session, p.AccountID)
	if err != nil {
		return s.fail(ctx, p, "check", "connected", err)
	}
	h, err := s.Telegram.Check(ctx, app, stored)
	if errors.Is(err, ErrSignedOut) {
		metrics.TelegramStep("check", CodeSignedOut)
		return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `UPDATE telegram_sessions SET status = 'signed_out', session_sealed = NULL, error_code = $3, last_checked_at = now(), updated_at = now()
				WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID, CodeSignedOut); err != nil {
				return err
			}
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: p.WorkspaceID, ActorType: "system", Action: "telegram_account.signed_out",
				TargetType: "staff_account", TargetID: p.AccountID, Data: map[string]any{"by": "telegram"}})
		})
	}
	if err != nil {
		if _, uerr := s.Pool.Exec(ctx, `UPDATE telegram_sessions SET last_checked_at = now() WHERE workspace_id = $1 AND account_id = $2`, p.WorkspaceID, p.AccountID); uerr != nil {
			return uerr
		}
		return s.fail(ctx, p, "check", "connected", err)
	}
	metrics.TelegramStep("check", "success")
	track.Emit(ctx, track.Event{Name: "telegram_account.checked", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job",
		Props: map[string]any{"two_step": h.TwoStep, "login_email": h.LoginEmail, "other_sessions": h.OtherSessions}})
	_, err = s.Pool.Exec(ctx, `UPDATE telegram_sessions SET session_sealed = COALESCE($3, session_sealed), telegram_user_id = $4,
		telegram_username = NULLIF($5, ''), telegram_phone = NULLIF($6, ''), telegram_name = NULLIF($7, ''), two_step = $8, login_email = $9,
		other_sessions = $10, error_code = NULL, last_checked_at = now(), updated_at = now()
		WHERE workspace_id = $1 AND account_id = $2 AND status = 'connected'`,
		p.WorkspaceID, p.AccountID, s.seal(h.Session, p.AccountID), h.Self.ID, h.Self.Username, h.Self.Phone, h.Self.Name, h.TwoStep, h.LoginEmail, h.OtherSessions)
	return err
}

// logout ends the session at Telegram, then forgets it.
func (s *Service) logout(ctx context.Context, j jobs.Job) error {
	p, err := payload(j)
	if err != nil {
		return err
	}
	r, err := s.load(ctx, p)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (r.attempt != p.Attempt || r.status != "disconnecting")) {
		return nil
	}
	if err != nil {
		return err
	}
	if stored, err := s.open(r.session, p.AccountID); err == nil && len(stored) > 0 {
		if app, err := s.app(ctx, p.WorkspaceID, r.integration); err == nil {
			if err := s.Telegram.LogOut(ctx, app, stored); err != nil && !errors.Is(err, ErrSignedOut) {
				if j.Attempt < j.MaxAttempts {
					return err
				}
				// Out of tries: forget it anyway; it can be ended from the
				// account's phone (Settings > Devices).
				track.Fail(ctx, s.Log, track.Event{Name: "telegram_account.check_failed", WorkspaceID: p.WorkspaceID, ActorType: "system", Surface: "job",
					Props: map[string]any{"step": "logout", "reason": codeOf(err)}}, "connector_unavailable", err)
			}
		}
	}
	metrics.TelegramStep("logout", "success")
	_, err = s.Pool.Exec(ctx, `UPDATE telegram_sessions SET status = 'signed_out', session_sealed = NULL, password_sealed = NULL, login_url = NULL,
		error_code = NULL, updated_at = now() WHERE workspace_id = $1 AND account_id = $2 AND attempt = $3`, p.WorkspaceID, p.AccountID, p.Attempt)
	return err
}

// Run queues a check for each connected account whose last one is older
// than CheckEvery, and counts accounts by status for the dashboard.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := s.Tick(ctx); err != nil && ctx.Err() == nil {
			track.Fail(ctx, s.Log, track.Event{Name: "telegram_account.checks_failed", ActorType: "system", Surface: "system"}, "internal", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick is one round of Run.
func (s *Service) Tick(ctx context.Context) error {
	rows, err := s.Pool.Query(ctx, `SELECT workspace_id::text, account_id::text, attempt FROM telegram_sessions
		WHERE status = 'connected' AND (last_checked_at IS NULL OR last_checked_at < now() - make_interval(secs => $1))
		ORDER BY last_checked_at NULLS FIRST LIMIT 500`, CheckEvery.Seconds())
	if err != nil {
		return err
	}
	due, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Payload, error) {
		var p Payload
		return p, r.Scan(&p.WorkspaceID, &p.AccountID, &p.Attempt)
	})
	if err != nil {
		return err
	}
	now := time.Now()
	for _, p := range due {
		if err := Enqueue(ctx, s.Pool, KindCheck, p.WorkspaceID, p.AccountID, p.Attempt, now); err != nil {
			return err
		}
	}
	// A sync chain that stopped (a deploy mid-run, a lost job) starts again.
	rows, err = s.Pool.Query(ctx, `SELECT workspace_id::text, account_id::text FROM telegram_sessions
		WHERE status = 'connected' AND sync_status IN ('importing', 'live') AND brand_id IS NOT NULL
		  AND (sync_lease_until IS NULL OR sync_lease_until < now())
		  AND COALESCE(synced_at, sync_started_at, '-infinity') < now() - interval '15 minutes'
		LIMIT 500`)
	if err != nil {
		return err
	}
	stalled, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Payload, error) {
		var p Payload
		return p, r.Scan(&p.WorkspaceID, &p.AccountID)
	})
	if err != nil {
		return err
	}
	for _, p := range stalled {
		if err := EnqueueSync(ctx, s.Pool, p.WorkspaceID, p.AccountID, now.Truncate(time.Minute)); err != nil {
			return err
		}
	}
	if err := s.alertWaiting(ctx); err != nil {
		return err
	}
	var lag *float64
	if err := s.Pool.QueryRow(ctx, `SELECT EXTRACT(EPOCH FROM now() - min(COALESCE(synced_at, sync_started_at)))::float8 FROM telegram_sessions
		WHERE status = 'connected' AND sync_status IN ('importing', 'live') AND brand_id IS NOT NULL`).Scan(&lag); err != nil {
		return err
	}
	if lag != nil {
		metrics.TelegramSyncLag(time.Duration(*lag * float64(time.Second)))
	} else {
		metrics.TelegramSyncLag(0)
	}
	rows, err = s.Pool.Query(ctx, `SELECT status, count(*) FROM telegram_sessions GROUP BY status`)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return err
		}
		counts[st] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	metrics.TelegramAccounts(counts)
	return nil
}
