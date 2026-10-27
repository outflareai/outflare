package health

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/audit"
)

var jsonUnmarshal = json.Unmarshal

// Settings is how a workspace wants unhealthy mailboxes handled.
type Settings struct {
	AutoPause       bool    `json:"auto_pause"`
	PauseBounceRate float64 `json:"pause_bounce_rate"` // percent over 24 hours
	MinSends        int     `json:"min_sends"`
}

var Defaults = Settings{AutoPause: true, PauseBounceRate: 5, MinSends: 20}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func LoadSettings(ctx context.Context, q querier, ws string) (Settings, error) {
	s := Defaults
	err := q.QueryRow(ctx, `SELECT auto_pause, pause_bounce_rate::float8, min_sends FROM health_settings WHERE workspace_id = $1`, ws).
		Scan(&s.AutoPause, &s.PauseBounceRate, &s.MinSends)
	if err == pgx.ErrNoRows {
		return Defaults, nil
	}
	return s, err
}

func SaveSettings(ctx context.Context, tx pgx.Tx, ws string, s Settings) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO health_settings (workspace_id, auto_pause, pause_bounce_rate, min_sends) VALUES ($1, $2, $3, $4)
		ON CONFLICT (workspace_id) DO UPDATE SET auto_pause = $2, pause_bounce_rate = $3, min_sends = $4, updated_at = now()`,
		ws, s.AutoPause, s.PauseBounceRate, s.MinSends)
	return err
}

// Monitor pauses mailboxes whose bounce rate spikes, before the provider
// or the domain's reputation pays for it.
type Monitor struct {
	Pool *pgxpool.Pool
	Log  *slog.Logger
}

func (m *Monitor) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := m.Tick(ctx); err != nil && ctx.Err() == nil {
			m.Log.Error("health tick", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// AutoPaused is a mailbox the monitor paused.
type AutoPaused struct {
	WorkspaceID, MailboxID, Email string
	Sent, Bounced                 int
	Rate                          float64
}

// Tick pauses every active mailbox over its workspace's bounce threshold
// and records why, in the mailbox's status_reason and the audit log.
func (m *Monitor) Tick(ctx context.Context) ([]AutoPaused, error) {
	rows, err := m.Pool.Query(ctx, `
		WITH stats AS (
			SELECT mb.id, mb.workspace_id, mb.email,
			       (SELECT count(*) FROM messages WHERE mailbox_id = mb.id AND status = 'sent' AND created_at > now() - interval '24 hours')::int AS sent,
			       (SELECT count(*) FROM enrollments WHERE mailbox_id = mb.id AND status = 'bounced' AND updated_at > now() - interval '24 hours')::int AS bounced,
			       COALESCE(h.auto_pause, true) AS auto_pause, COALESCE(h.pause_bounce_rate, 5)::float8 AS threshold, COALESCE(h.min_sends, 20) AS min_sends
			FROM mailboxes mb LEFT JOIN health_settings h ON h.workspace_id = mb.workspace_id
			WHERE mb.status = 'active')
		SELECT id, workspace_id, email, sent, bounced FROM stats
		WHERE auto_pause AND sent >= min_sends AND bounced * 100.0 / sent >= threshold`)
	if err != nil {
		return nil, err
	}
	hits, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (AutoPaused, error) {
		var p AutoPaused
		err := r.Scan(&p.MailboxID, &p.WorkspaceID, &p.Email, &p.Sent, &p.Bounced)
		p.Rate = pct(p.Bounced, p.Sent)
		return p, err
	})
	if err != nil {
		return nil, err
	}
	var done []AutoPaused
	for _, p := range hits {
		reason := fmt.Sprintf("Paused automatically: %d of %d emails bounced in 24 hours (%.1f%%). Verify the leads and check the domain, then resume.", p.Bounced, p.Sent, p.Rate)
		err := pgx.BeginFunc(ctx, m.Pool, func(tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, `UPDATE mailboxes SET status = 'paused', status_reason = $3 WHERE workspace_id = $1 AND id = $2 AND status = 'active'`, p.WorkspaceID, p.MailboxID, reason)
			if err != nil || tag.RowsAffected() == 0 {
				return err
			}
			done = append(done, p)
			return audit.Record(ctx, tx, audit.Event{WorkspaceID: p.WorkspaceID, ActorType: "system", Action: "mailbox.auto_paused", TargetType: "mailbox", TargetID: p.MailboxID,
				Data: map[string]any{"email": p.Email, "sent_24h": p.Sent, "bounced_24h": p.Bounced, "bounce_rate": p.Rate, "reason": reason}})
		})
		if err != nil {
			return done, err
		}
		m.Log.Warn("mailbox auto-paused", "mailbox", p.Email, "bounce_rate", p.Rate)
	}
	return done, nil
}
