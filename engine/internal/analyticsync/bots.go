package analyticsync

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/outflareai/outflare/engine/internal/connectors"
	"github.com/outflareai/outflare/engine/internal/crawl"
	"github.com/outflareai/outflare/engine/internal/track"
)

// AI crawlers in request logs: a connection whose tool keeps request logs
// (Cloudflare) also stores, once a day, how many requests each known AI
// crawler (crawl.AIBots) made to each host. A failure here, such as a
// token without the zone permissions, is kept on ai_bot_syncs and never
// fails the analytics sync.

const (
	botsEvery    = 20 * time.Hour
	botsBackfill = 3 // days read on the first run
	botsRecent   = 2 // complete days re-read on later runs
)

// BotToken names the AI crawler a user agent belongs to, or "".
func BotToken(userAgent string) string {
	ua := strings.ToLower(userAgent)
	for _, b := range crawl.AIBots {
		if strings.Contains(ua, strings.ToLower(b.Token)) {
			return b.Token
		}
	}
	return ""
}

func (s *Syncer) syncBots(ctx context.Context, o *connectors.Opened, r connectors.BotLogReader, res *Result) error {
	ws, id := o.WorkspaceID, o.ID
	var last *time.Time
	err := s.Pool.QueryRow(ctx, `SELECT last_run_at FROM ai_bot_syncs WHERE integration_id = $1`, id).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return dbError{err}
	}
	if last != nil && s.now().Sub(*last) < botsEvery {
		return nil
	}
	days := botsBackfill
	if last != nil {
		days = botsRecent
	}
	agents := make([]string, len(crawl.AIBots))
	for i, b := range crawl.AIBots {
		agents[i] = b.Token
	}
	type key struct{ host, day, token string }
	sums := map[key]int64{}
	var runErr error
	today := s.now().UTC().Truncate(24 * time.Hour)
	for d := days; d >= 1; d-- {
		day := today.AddDate(0, 0, -d).Format(time.DateOnly)
		res.Calls++
		hits, err := r.BotHits(ctx, o.Client, o.Creds, agents, day)
		if err != nil {
			runErr = err
			break
		}
		for _, h := range hits {
			if t := BotToken(h.UserAgent); t != "" && h.Host != "" {
				sums[key{h.Host, h.Day, t}] += h.Requests
			}
		}
	}
	return pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		for k, n := range sums {
			if _, err := tx.Exec(ctx, `
				INSERT INTO ai_bot_hits (workspace_id, integration_id, host, day, token, requests) VALUES ($1, $2, $3, $4, $5, $6)
				ON CONFLICT (integration_id, host, day, token) DO UPDATE SET requests = EXCLUDED.requests, synced_at = now()`,
				ws, id, k.host, k.day, k.token, n); err != nil {
				return dbError{err}
			}
		}
		status, code, msg := "ok", "", ""
		if runErr != nil {
			status, code, msg = "error", botErrorCode(runErr), runErr.Error()
			track.Fail(ctx, s.Log, track.Event{Name: "ai_bot_logs.sync_failed", WorkspaceID: ws, ActorType: "system", Surface: "job",
				Props: map[string]any{"integration_id": id, "connector": o.Connector.Definition().ID}}, code, runErr)
		} else {
			track.Emit(ctx, track.Event{Name: "ai_bot_logs.synced", WorkspaceID: ws, ActorType: "system", Surface: "job", Outcome: track.Success,
				Props: map[string]any{"integration_id": id, "connector": o.Connector.Definition().ID, "rows": len(sums), "days": days}})
		}
		res.Rows += len(sums)
		_, err := tx.Exec(ctx, `
			INSERT INTO ai_bot_syncs (integration_id, workspace_id, status, error_code, message, rows, last_run_at) VALUES ($1, $2, $3, $4, $5, $6, now())
			ON CONFLICT (integration_id) DO UPDATE SET status = EXCLUDED.status, error_code = EXCLUDED.error_code, message = EXCLUDED.message,
				rows = EXCLUDED.rows, last_run_at = now()`, id, ws, status, code, clipText(msg, 500), len(sums))
		if err != nil {
			return dbError{err}
		}
		return nil
	})
}

func botErrorCode(err error) string {
	switch connectors.KindOf(err) {
	case connectors.ErrAuth:
		return "connector_auth"
	case connectors.ErrUnavailable:
		return "connector_unavailable"
	}
	return "connector_invalid"
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
