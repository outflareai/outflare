package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"rsc.io/qr"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors/tgapp"
	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// Company Telegram accounts signed in to Outflare as a linked device
// (migration 0491). Someone with the account's phone scans a QR code once,
// on the workspace's own Telegram app connection; the work itself runs in
// jobs (engine/internal/tgsession).

func (s *Server) routesStaffTelegram(mux *http.ServeMux) {
	read, manage := auth.ScopeMembersRead, auth.ScopeMembersManage
	base := "/v1/workspaces/{ws}/staff-accounts/{id}/telegram"
	mux.HandleFunc("GET "+base, s.inWorkspace(read, s.getTelegramSession))
	mux.HandleFunc("PATCH "+base, s.inWorkspace(manage, s.updateTelegramSession))
	mux.HandleFunc("POST "+base+"/connect", s.inWorkspace(manage, s.connectTelegram))
	mux.HandleFunc("POST "+base+"/password", s.inWorkspace(manage, s.telegramPassword))
	mux.HandleFunc("POST "+base+"/check", s.inWorkspace(manage, s.checkTelegram))
	mux.HandleFunc("POST "+base+"/disconnect", s.inWorkspace(manage, s.disconnectTelegram))
	mux.HandleFunc("POST "+base+"/sync", s.inWorkspace(manage, s.startTelegramSync))
	mux.HandleFunc("POST "+base+"/sync/pause", s.inWorkspace(manage, s.pauseTelegramSync))
}

// telegramChecklist are the lock-down steps for a company Telegram
// account, ticked by hand. Two-step password and login email are also read
// from Telegram on each check.
var telegramChecklist = []string{
	"number_owned",      // the number is on the company's own account, not a person's
	"two_step_password", // a two-step password is set
	"login_email",       // a company login email is set for sign-in codes
	"passkey",           // a passkey is added
	"password_saved",    // the password is in the company's password manager
	"other_sessions",    // sessions on unknown devices are ended
	"auto_delete_off",   // the account isn't set to delete itself after months away
}

type telegramSessionView struct {
	AccountID      string          `json:"account_id"`
	Status         string          `json:"status"`
	IntegrationID  *string         `json:"integration_id"`
	LoginURL       *string         `json:"login_url"`
	LoginQR        *string         `json:"login_qr"`
	LoginExpiresAt *time.Time      `json:"login_expires_at"`
	PasswordHint   string          `json:"password_hint"`
	UserID         *string         `json:"telegram_user_id"`
	Username       *string         `json:"telegram_username"`
	Phone          *string         `json:"telegram_phone"`
	Name           *string         `json:"telegram_name"`
	TwoStep        *bool           `json:"two_step"`
	LoginEmail     *bool           `json:"login_email"`
	OtherSessions  *int            `json:"other_sessions"`
	Checklist      map[string]bool `json:"checklist"`
	ErrorCode      *string         `json:"error_code"`
	ConnectedAt    *time.Time      `json:"connected_at"`
	LastCheckedAt  *time.Time      `json:"last_checked_at"`
	UpdatedAt      *time.Time      `json:"updated_at"`
	BrandID        *string         `json:"brand_id"`
	SyncStatus     string          `json:"sync_status"`
	HistoryDays    int             `json:"history_days"`
	SyncStartedAt  *time.Time      `json:"sync_started_at"`
	SyncedAt       *time.Time      `json:"synced_at"`
	SyncError      *string         `json:"sync_error"`
	Chats          int             `json:"chats"`
	Messages       int             `json:"messages"`
}

// telegramAccount checks the account is a Telegram account in the
// workspace and returns its status.
func telegramAccount(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, ws, id string, lock bool) (status string, err error) {
	sql := `SELECT a.kind, a.status FROM staff_accounts a WHERE a.id = $1 AND a.workspace_id = $2`
	if lock {
		sql += ` FOR UPDATE`
	}
	var kind string
	err = q.QueryRow(ctx, sql, id, ws).Scan(&kind, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errNotFound
	}
	if err == nil && kind != "telegram" {
		return "", errInvalid("only a Telegram account can sign in to Telegram")
	}
	return status, err
}

func (s *Server) loadTelegramSession(ctx context.Context, ws, id string, withLink bool) (*telegramSessionView, error) {
	v := telegramSessionView{AccountID: id, Status: "not_connected", Checklist: map[string]bool{}, SyncStatus: "off", HistoryDays: 365}
	var checklist []byte
	err := s.pool.QueryRow(ctx, `
		SELECT status, integration_id::text, login_url, login_expires_at, password_hint, telegram_user_id::text,
		       telegram_username, telegram_phone, telegram_name, two_step, login_email, other_sessions, checklist,
		       error_code, connected_at, last_checked_at, updated_at, brand_id::text, sync_status, history_days,
		       sync_started_at, synced_at, sync_error,
		       (SELECT count(*) FROM telegram_chats c WHERE c.workspace_id = $1 AND c.account_id = $2),
		       (SELECT COALESCE(sum(message_count), 0) FROM telegram_chats c WHERE c.workspace_id = $1 AND c.account_id = $2)
		FROM telegram_sessions WHERE workspace_id = $1 AND account_id = $2`, ws, id).
		Scan(&v.Status, &v.IntegrationID, &v.LoginURL, &v.LoginExpiresAt, &v.PasswordHint, &v.UserID, &v.Username, &v.Phone, &v.Name,
			&v.TwoStep, &v.LoginEmail, &v.OtherSessions, &checklist, &v.ErrorCode, &v.ConnectedAt, &v.LastCheckedAt, &v.UpdatedAt,
			&v.BrandID, &v.SyncStatus, &v.HistoryDays, &v.SyncStartedAt, &v.SyncedAt, &v.SyncError, &v.Chats, &v.Messages)
	if errors.Is(err, pgx.ErrNoRows) {
		return &v, nil
	}
	if err != nil {
		return nil, err
	}
	if len(checklist) > 0 {
		if err := json.Unmarshal(checklist, &v.Checklist); err != nil {
			return nil, err
		}
	}
	// The QR link signs a phone in to this session: only people who may
	// manage the directory see it.
	if !withLink || v.Status != "waiting_scan" {
		v.LoginURL, v.LoginExpiresAt = nil, nil
	}
	if v.LoginURL != nil {
		if code, err := qr.Encode(*v.LoginURL, qr.M); err == nil {
			code.Scale = 6
			img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
			v.LoginQR = &img
		}
	}
	return &v, nil
}

func (s *Server) writeTelegramSession(w http.ResponseWriter, r *http.Request, status int) {
	a := accessFrom(r.Context())
	v, err := s.loadTelegramSession(r.Context(), a.WorkspaceID, r.PathValue("id"), a.has(auth.ScopeMembersManage))
	if err != nil {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, status, v)
}

func (s *Server) getTelegramSession(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	if _, err := telegramAccount(r.Context(), s.pool, a.WorkspaceID, id, false); s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}

// telegramApp picks the workspace's Telegram app connection: the one
// named, or the only one.
func (s *Server) telegramApp(ctx context.Context, ws string, id *string) (string, error) {
	all, err := s.integrations.List(ctx, s.pool, ws, tgapp.ID)
	if err != nil {
		return "", err
	}
	if id != nil {
		for _, i := range all {
			if i.ID == *id {
				return i.ID, nil
			}
		}
		return "", errInvalid("integration_id is not a Telegram app connection in this workspace")
	}
	switch len(all) {
	case 0:
		return "", errInvalid("connect your company's Telegram app first (Connectors, Telegram), with the api_id and api_hash from my.telegram.org")
	case 1:
		return all[0].ID, nil
	}
	return "", errInvalid("there is more than one Telegram app connection: pass integration_id")
}

func (s *Server) connectTelegram(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		IntegrationID *string `json:"integration_id"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		status, err := telegramAccount(ctx, tx, a.WorkspaceID, id, true)
		if err != nil {
			return err
		}
		if status == "retired" {
			return errInvalid("this account is retired: make it active first")
		}
		integration, err := s.telegramApp(ctx, a.WorkspaceID, req.IntegrationID)
		if err != nil {
			return err
		}
		var current string
		err = tx.QueryRow(ctx, `SELECT status FROM telegram_sessions WHERE account_id = $1 AND workspace_id = $2`, id, a.WorkspaceID).Scan(&current)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if current == "connected" || current == "disconnecting" {
			return errInvalid("this account is already signed in: sign it out first to sign in again")
		}
		// A fresh sign-in: any earlier attempt's job stops, and its
		// half-made session is dropped.
		var attempt int
		err = tx.QueryRow(ctx, `
			INSERT INTO telegram_sessions (account_id, workspace_id, integration_id, status)
			VALUES ($1, $2, $3, 'starting')
			ON CONFLICT (account_id) DO UPDATE SET integration_id = EXCLUDED.integration_id, status = 'starting',
				attempt = telegram_sessions.attempt + 1, login_url = NULL, login_expires_at = NULL, password_hint = '',
				session_sealed = NULL, password_sealed = NULL, error_code = NULL, updated_at = now()
			RETURNING attempt`, id, a.WorkspaceID, integration).Scan(&attempt)
		if err != nil {
			return err
		}
		if err := tgsession.Enqueue(ctx, tx, tgsession.KindLogin, a.WorkspaceID, id, attempt, time.Now()); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.connect_started", "staff_account", id, nil, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}

func (s *Server) telegramPassword(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	if req.Password == "" || len(req.Password) > 256 {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "password is the account's two-step password")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := telegramAccount(ctx, tx, a.WorkspaceID, id, true); err != nil {
			return err
		}
		var attempt int
		err := tx.QueryRow(ctx, `
			UPDATE telegram_sessions SET status = 'checking_password', password_sealed = $3, error_code = NULL, updated_at = now()
			WHERE account_id = $1 AND workspace_id = $2 AND status = 'needs_password' RETURNING attempt`,
			id, a.WorkspaceID, s.box.Seal([]byte(req.Password), []byte(id))).Scan(&attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("this account isn't waiting for its two-step password")
		}
		if err != nil {
			return err
		}
		if err := tgsession.Enqueue(ctx, tx, tgsession.KindPassword, a.WorkspaceID, id, attempt, time.Now()); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.password_entered", "staff_account", id, nil, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}

func (s *Server) checkTelegram(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := telegramAccount(ctx, tx, a.WorkspaceID, id, false); err != nil {
			return err
		}
		var attempt int
		err := tx.QueryRow(ctx, `SELECT attempt FROM telegram_sessions WHERE account_id = $1 AND workspace_id = $2 AND status = 'connected'`,
			id, a.WorkspaceID).Scan(&attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalid("this account isn't signed in")
		}
		if err != nil {
			return err
		}
		return tgsession.Enqueue(ctx, tx, tgsession.KindCheck, a.WorkspaceID, id, attempt, time.Now())
	})
	if s.allocError(w, r, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"queued": true})
}

// signOutTelegram starts signing an account out, when it is signed in or
// part way through signing in. It reports whether there was anything to
// sign out.
func signOutTelegram(ctx context.Context, tx pgx.Tx, ws, id string) (bool, error) {
	var attempt int
	err := tx.QueryRow(ctx, `
		UPDATE telegram_sessions SET status = 'disconnecting', attempt = attempt + 1, login_url = NULL, password_sealed = NULL, updated_at = now()
		WHERE account_id = $1 AND workspace_id = $2 AND status NOT IN ('signed_out', 'disconnecting')
		RETURNING attempt`, id, ws).Scan(&attempt)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, tgsession.Enqueue(ctx, tx, tgsession.KindLogout, ws, id, attempt, time.Now())
}

func (s *Server) disconnectTelegram(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := telegramAccount(ctx, tx, a.WorkspaceID, id, true); err != nil {
			return err
		}
		started, err := signOutTelegram(ctx, tx, a.WorkspaceID, id)
		if err != nil || !started {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.signed_out", "staff_account", id,
			map[string]any{"by": "person"}, map[string]any{"by": "person"})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}

func (s *Server) updateTelegramSession(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		Checklist map[string]bool `json:"checklist"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	for k := range req.Checklist {
		if !slices.Contains(telegramChecklist, k) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "unknown checklist step "+k)
			return
		}
	}
	patch, _ := json.Marshal(req.Checklist)
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := telegramAccount(ctx, tx, a.WorkspaceID, id, true); err != nil {
			return err
		}
		// The checklist is kept from the start, before the first sign-in.
		_, err := tx.Exec(ctx, `
			INSERT INTO telegram_sessions (account_id, workspace_id, status, attempt, checklist)
			VALUES ($1, $2, 'signed_out', 0, $3::jsonb)
			ON CONFLICT (account_id) DO UPDATE SET checklist = telegram_sessions.checklist || $3::jsonb, updated_at = now()`,
			id, a.WorkspaceID, string(patch))
		if err != nil {
			return err
		}
		done := 0
		for _, v := range req.Checklist {
			if v {
				done++
			}
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.checklist_updated", "staff_account", id, nil,
			map[string]any{"steps": len(req.Checklist), "ticked": done})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}
