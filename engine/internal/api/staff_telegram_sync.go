package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/tgsession"
)

// Syncing a signed-in company Telegram account into one brand's CRM
// (migration 0492). The copying runs in jobs (tgsession.KindSync).

func (s *Server) startTelegramSync(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	id := r.PathValue("id")
	var req struct {
		BrandID     *string `json:"brand_id"`
		HistoryDays *int    `json:"history_days"`
	}
	if r.ContentLength != 0 && !decode(w, r, &req) {
		return
	}
	if !validUUID(id) {
		s.allocError(w, r, errNotFound)
		return
	}
	if req.HistoryDays != nil && (*req.HistoryDays < 0 || *req.HistoryDays > 3650) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "history_days is how many days of history to copy, 0 to 3650")
		return
	}
	ctx := r.Context()
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := telegramAccount(ctx, tx, a.WorkspaceID, id, true); err != nil {
			return err
		}
		var status string
		var brand *string
		var days int
		err := tx.QueryRow(ctx, `SELECT status, brand_id::text, history_days FROM telegram_sessions WHERE account_id = $1 AND workspace_id = $2 FOR UPDATE`,
			id, a.WorkspaceID).Scan(&status, &brand, &days)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && status != "connected" {
			return errInvalid("sign this account in to Telegram first")
		}
		if err != nil {
			return err
		}
		if req.BrandID != nil {
			if !validUUID(*req.BrandID) {
				return errInvalid("brand_id is not a brand in this workspace")
			}
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM brands WHERE id = $1 AND workspace_id = $2)`, *req.BrandID, a.WorkspaceID).Scan(&ok); err != nil {
				return err
			}
			if !ok {
				return errInvalid("brand_id is not a brand in this workspace")
			}
			var elsewhere bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_chats WHERE workspace_id = $1 AND account_id = $2 AND brand_id <> $3)`,
				a.WorkspaceID, id, *req.BrandID).Scan(&elsewhere); err != nil {
				return err
			}
			if elsewhere {
				return errInvalid("this account's chats are already in another brand's CRM")
			}
			brand = req.BrandID
		}
		if brand == nil {
			return errInvalid("brand_id: pick the brand whose CRM this account's chats go into")
		}
		if req.HistoryDays != nil {
			if *req.HistoryDays > days {
				// Further back than before: the import carries on from
				// where each chat stopped.
				if _, err := tx.Exec(ctx, `UPDATE telegram_chats SET history_done = false, updated_at = now() WHERE workspace_id = $1 AND account_id = $2`,
					a.WorkspaceID, id); err != nil {
					return err
				}
			}
			days = *req.HistoryDays
		}
		if _, err := tx.Exec(ctx, `UPDATE telegram_sessions SET brand_id = $3, history_days = $4, sync_status = 'importing',
			sync_started_at = COALESCE(sync_started_at, now()), sync_error = NULL, updated_at = now()
			WHERE account_id = $1 AND workspace_id = $2`, id, a.WorkspaceID, *brand, days); err != nil {
			return err
		}
		if err := tgsession.EnqueueSync(ctx, tx, a.WorkspaceID, id, time.Now()); err != nil {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.sync_started", "staff_account", id,
			map[string]any{"brand_id": *brand, "history_days": days}, map[string]any{"history_days": days})
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}

func (s *Server) pauseTelegramSync(w http.ResponseWriter, r *http.Request) {
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
		tag, err := tx.Exec(ctx, `UPDATE telegram_sessions SET sync_status = 'paused', updated_at = now()
			WHERE account_id = $1 AND workspace_id = $2 AND sync_status IN ('importing', 'live')`, id, a.WorkspaceID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return recordWithProps(ctx, tx, &a.principal, a.WorkspaceID, "telegram_account.sync_paused", "staff_account", id, nil, nil)
	})
	if s.allocError(w, r, err) {
		return
	}
	s.writeTelegramSession(w, r, http.StatusOK)
}
