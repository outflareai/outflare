package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/auth"
	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/jobs"
	"github.com/outflareai/outflare/engine/internal/platforms"
)

func (s *Server) routesWarmup(mux *http.ServeMux) {
	read, manage := auth.ScopeMailboxesRead, auth.ScopeMailboxesManage
	mux.HandleFunc("POST /v1/workspaces/{ws}/mailboxes/warmup", s.inWorkspace(manage, s.bulkWarmup))
	mux.HandleFunc("GET /v1/workspaces/{ws}/mailboxes/{id}/warmup", s.inWorkspace(read, s.warmupHistory))
}

// warmupSettings changes warm-up on one or more mailboxes. Unset fields
// are left as they are. Outflare doesn't send warm-up mail itself: a
// connected sequencer with its own warm-up (EmailBison, Instantly,
// Smartlead) runs it.
type warmupSettings struct {
	Enabled *bool `json:"enabled,omitempty"`
	// DailyTarget and ReplyRate were for Outflare's own warm-up, which is
	// gone. They are still accepted and stored, and do nothing.
	DailyTarget *int `json:"daily_target,omitempty"`
	ReplyRate   *int `json:"reply_rate,omitempty"`
	// Provider is the id of the connected sequencer that runs the
	// warm-up. "outflare" only goes with enabled false, and detaches the
	// mailbox from its sequencer's warm-up. Turning warm-up on without a
	// provider keeps the mailbox's sequencer, or picks one it is in.
	Provider *string `json:"provider,omitempty"`

	// capable is the workspace's connections that can run warm-up, set
	// by resolveWarmup when enabling without a provider.
	capable []string
}

const noWarmupProvider = "Outflare doesn't send warm-up mail itself. Connect EmailBison, Instantly or Smartlead and warm up there"

func (w *warmupSettings) validate() string {
	switch {
	case w.Enabled == nil && w.DailyTarget == nil && w.ReplyRate == nil && w.Provider == nil:
		return "set enabled or provider"
	case w.DailyTarget != nil && (*w.DailyTarget < 1 || *w.DailyTarget > 100):
		return "daily_target must be 1 to 100"
	case w.ReplyRate != nil && (*w.ReplyRate < 0 || *w.ReplyRate > 100):
		return "reply_rate must be 0 to 100"
	case w.Provider != nil && *w.Provider == "outflare" && (w.Enabled == nil || *w.Enabled):
		return noWarmupProvider
	case w.Provider != nil && *w.Provider != "outflare" && !validUUID(*w.Provider):
		return "provider must be the id of a connected sequencer"
	}
	return ""
}

// platform returns the integration id when warm-up is to run on a
// connected sequencer, or "".
func (w *warmupSettings) platform() string {
	if w.Provider == nil || *w.Provider == "outflare" {
		return ""
	}
	return *w.Provider
}

// resolveWarmup finds the sequencer for each mailbox when warm-up is
// turned on without a provider: the one it already has, or a connected
// sequencer it is in that can run warm-up. Mailboxes with neither can't be
// warmed up. For the given ids that is an error; for every mailbox in the
// workspace (ids nil) they are left out, and the ids to change returned.
func (s *Server) resolveWarmup(ctx context.Context, q pgx.Tx, ws string, set *warmupSettings, ids []string) ([]string, error) {
	if set.Enabled == nil || !*set.Enabled || set.Provider != nil {
		return ids, nil
	}
	rows, err := q.Query(ctx, `SELECT id::text, connector FROM integrations WHERE workspace_id = $1`, ws)
	if err != nil {
		return nil, err
	}
	type conn struct{ id, connector string }
	conns, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (conn, error) {
		var c conn
		return c, r.Scan(&c.id, &c.connector)
	})
	if err != nil {
		return nil, err
	}
	set.capable = []string{}
	for _, c := range conns {
		if def, ok := s.integrations.Registry.Get(c.connector); ok {
			if _, can := def.(connectors.WarmupController); can {
				set.capable = append(set.capable, c.id)
			}
		}
	}
	rows, err = q.Query(ctx, `
		SELECT m.id::text, m.warmup_integration_id IS NOT NULL OR EXISTS (
			SELECT 1 FROM mailbox_platforms p WHERE p.mailbox_id = m.id AND p.integration_id = ANY($3))
		FROM mailboxes m WHERE m.workspace_id = $1 AND ($2::uuid[] IS NULL OR m.id = ANY($2))`, ws, ids, set.capable)
	if err != nil {
		return nil, err
	}
	type found struct {
		id string
		ok bool
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (found, error) {
		var f found
		return f, r.Scan(&f.id, &f.ok)
	})
	if err != nil {
		return nil, err
	}
	can, missing := []string{}, 0
	for _, f := range list {
		if f.ok {
			can = append(can, f.id)
		} else {
			missing++
		}
	}
	switch {
	case ids == nil:
		return can, nil
	case missing == 1 && len(ids) == 1:
		return nil, errInvalid("this mailbox isn't in a connected sequencer that runs warm-up. " + noWarmupProvider)
	case missing > 0:
		return nil, errInvalid(fmt.Sprintf("%d of these mailboxes aren't in a connected sequencer that runs warm-up. %s", missing, noWarmupProvider))
	}
	return ids, nil
}

// checkProvider says what is wrong, if anything, with running warm-up for
// the given mailboxes (nil: all of them) on the settings' provider: it must
// be a sequencer connected to the workspace that can switch warm-up, and
// every mailbox must be in that account.
func (s *Server) checkProvider(ctx context.Context, q jobs.Querier, ws string, set *warmupSettings, ids []string) (string, error) {
	id := set.platform()
	if id == "" {
		return "", nil
	}
	var connector, name string
	err := q.QueryRow(ctx, `SELECT connector, name FROM integrations WHERE id = $1 AND workspace_id = $2`, id, ws).Scan(&connector, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return "provider is not a connection in this workspace", nil
	}
	if err != nil {
		return "", err
	}
	c, ok := s.integrations.Registry.Get(connector)
	if _, can := c.(connectors.WarmupController); !ok || !can {
		return name + " can't run warm-up for Outflare", nil
	}
	var missing int
	if err := q.QueryRow(ctx, `
		SELECT count(*) FROM unnest($2::uuid[]) AS want(id)
		WHERE NOT EXISTS (SELECT 1 FROM mailbox_platforms p WHERE p.mailbox_id = want.id AND p.integration_id = $1)`,
		id, ids).Scan(&missing); err != nil {
		return "", err
	}
	switch {
	case missing == 1 && len(ids) == 1:
		return "this mailbox is not in " + name + ", so " + name + " can't warm it up", nil
	case missing > 0:
		return fmt.Sprintf("%d of these mailboxes are not in %s, so %s can't warm them up", missing, name, name), nil
	}
	return "", nil
}

// apply updates the given mailboxes, or every mailbox in the workspace
// when ids is nil, after resolveWarmup. A
// change to whether warm-up is on, or who runs it, queues a job for each
// sequencer involved (the one running it now and the one before) to
// switch its own warm-up to match.
func (w *warmupSettings) apply(ctx context.Context, tx pgx.Tx, ws string, ids []string) (int64, error) {
	var provider *string
	if w.Provider != nil {
		p := w.platform()
		provider = &p
	}
	rows, err := tx.Query(ctx, `
		WITH old AS (
			SELECT id, warmup_integration_id FROM mailboxes
			WHERE workspace_id = $1 AND ($2::uuid[] IS NULL OR id = ANY($2)) FOR UPDATE
		)
		UPDATE mailboxes m SET
			warmup_started_at = CASE WHEN $3 AND NOT m.warmup_enabled THEN now() ELSE m.warmup_started_at END,
			warmup_enabled = COALESCE($3, m.warmup_enabled),
			warmup_daily_target = COALESCE($4, m.warmup_daily_target),
			warmup_reply_rate = COALESCE($5, m.warmup_reply_rate),
			warmup_integration_id = CASE
				WHEN $6::text IS NOT NULL THEN NULLIF($6, '')::uuid
				WHEN $3 AND m.warmup_integration_id IS NULL THEN (
					SELECT p.integration_id FROM mailbox_platforms p
					WHERE p.mailbox_id = m.id AND p.integration_id = ANY($7)
					ORDER BY p.warmup_enabled DESC, p.observed_at DESC NULLS LAST LIMIT 1)
				ELSE m.warmup_integration_id END,
			updated_at = now()
		FROM old WHERE m.id = old.id
		RETURNING m.id::text, old.warmup_integration_id::text, m.warmup_integration_id::text`,
		ws, ids, w.Enabled, w.DailyTarget, w.ReplyRate, provider, w.capable)
	if err != nil {
		return 0, err
	}
	type changed struct {
		id       string
		was, now *string
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (changed, error) {
		var c changed
		return c, r.Scan(&c.id, &c.was, &c.now)
	})
	if err != nil || w.Enabled == nil && w.Provider == nil {
		return int64(len(list)), err
	}
	for _, c := range list {
		for _, integration := range []*string{c.was, c.now} {
			if integration == nil || integration == c.was && c.now != nil && *c.now == *c.was {
				continue
			}
			if err := platforms.RequestWarmup(ctx, tx, ws, c.id, *integration); err != nil {
				return 0, err
			}
		}
	}
	return int64(len(list)), nil
}

func (s *Server) bulkWarmup(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	var req struct {
		warmupSettings
		MailboxIDs []string `json:"mailbox_ids"`
		PoolID     *string  `json:"pool_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if msg := req.validate(); msg != "" {
		writeError(w, r, http.StatusBadRequest, "invalid_request", msg)
		return
	}
	if req.MailboxIDs != nil && req.PoolID != nil {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "give mailbox_ids or pool_id, not both")
		return
	}
	for _, id := range req.MailboxIDs {
		if !validUUID(id) {
			writeError(w, r, http.StatusBadRequest, "invalid_request", "mailbox_ids must be UUIDs")
			return
		}
	}
	if req.PoolID != nil && !validUUID(*req.PoolID) {
		writeError(w, r, http.StatusBadRequest, "invalid_request", "pool not found")
		return
	}

	ctx := r.Context()
	var updated int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		ids := req.MailboxIDs
		if req.PoolID != nil {
			rows, err := tx.Query(ctx, `SELECT id::text FROM mailboxes WHERE workspace_id = $1 AND pool_id = $2`, a.WorkspaceID, *req.PoolID)
			if err != nil {
				return err
			}
			if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return err
			}
			if ids == nil {
				ids = []string{}
			}
		}
		// Moving every mailbox to a sequencer's warm-up means every
		// mailbox that sequencer has.
		if ids == nil && req.platform() != "" {
			rows, err := tx.Query(ctx, `SELECT mailbox_id::text FROM mailbox_platforms WHERE workspace_id = $1 AND integration_id = $2`,
				a.WorkspaceID, req.platform())
			if err != nil {
				return err
			}
			if ids, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
				return err
			}
			if ids == nil {
				ids = []string{}
			}
		}
		msg, err := s.checkProvider(ctx, tx, a.WorkspaceID, &req.warmupSettings, ids)
		if err != nil {
			return err
		}
		if msg == "" {
			if ids, err = s.resolveWarmup(ctx, tx, a.WorkspaceID, &req.warmupSettings, ids); err != nil {
				return err
			}
		}
		if msg != "" {
			return errInvalid(msg)
		}
		if updated, err = req.apply(ctx, tx, a.WorkspaceID, ids); err != nil {
			return err
		}
		data := map[string]any{"mailboxes": updated, "settings": req.warmupSettings}
		if req.PoolID != nil {
			data["pool_id"] = *req.PoolID
		}
		return record(ctx, tx, &a.principal, a.WorkspaceID, "mailbox.warmup_updated", "workspace", a.WorkspaceID, data)
	})
	var bad errInvalid
	switch {
	case errors.As(err, &bad):
		writeError(w, r, http.StatusBadRequest, "invalid_request", string(bad))
	case err != nil:
		s.internal(w, r, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"updated": updated})
	}
}

type warmupDay struct {
	Date    string `json:"date"`
	Sent    int    `json:"sent"`
	Inbox   int    `json:"inbox"`
	Spam    int    `json:"spam"`
	Missing int    `json:"missing"`
	// Replies is how many warm-up replies this mailbox received.
	Replies int `json:"replies"`
}

// warmupHistory is a mailbox's results from Outflare's own warm-up for
// each of the last 14 days (UTC), oldest first. That warm-up is gone, so
// the days run down to zero; the route stays for clients that call it.
func (s *Server) warmupHistory(w http.ResponseWriter, r *http.Request) {
	a := accessFrom(r.Context())
	w.Header().Set("Deprecation", "@1792972800") // 2026-10-26
	id := r.PathValue("id")
	if _, ok := s.findMailbox(w, r, a.WorkspaceID, id); !ok {
		return
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	rows, err := s.pool.Query(r.Context(), `
		SELECT to_char(d AT TIME ZONE 'UTC', 'YYYY-MM-DD'),
			count(g.id) FILTER (WHERE g.from_mailbox_id = $1 AND g.status = 'sent'),
			count(g.id) FILTER (WHERE g.from_mailbox_id = $1 AND g.landed = 'inbox'),
			count(g.id) FILTER (WHERE g.from_mailbox_id = $1 AND g.landed = 'spam'),
			count(g.id) FILTER (WHERE g.from_mailbox_id = $1 AND g.landed = 'missing'),
			count(g.id) FILTER (WHERE g.to_mailbox_id = $1 AND g.depth > 0 AND g.status = 'sent')
		FROM generate_series($2::timestamptz - interval '13 days', $2::timestamptz, interval '1 day') d
		LEFT JOIN warmup_messages g ON (g.from_mailbox_id = $1 OR g.to_mailbox_id = $1)
			AND g.created_at >= d AND g.created_at < d + interval '1 day'
		GROUP BY d ORDER BY d`, id, today)
	if err != nil {
		s.internal(w, r, err)
		return
	}
	days, err := pgx.CollectRows(rows, pgx.RowToStructByPos[warmupDay])
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		s.internal(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": days})
}
