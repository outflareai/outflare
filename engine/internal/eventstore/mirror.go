package eventstore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/track"
)

// Mirror copies customer_events to ClickHouse. One server runs it at a
// time (an advisory lock); the others skip their turn.
type Mirror struct {
	Pool *pgxpool.Pool
	CH   *ClickHouse
	Log  *slog.Logger
	// Lag is how far behind now the mirror reads, so a transaction that
	// started earlier but committed later isn't skipped. Default a minute.
	Lag time.Duration
	// Batch is the most rows copied per round. Default 5,000.
	Batch int
	// Every is how often it looks for new rows. Default 5 seconds.
	Every time.Duration

	lastReport time.Time
	copied     int64
	tombstones int64
}

const (
	mirrorStore = "clickhouse"
	mirrorLock  = 7_140_149 // pg_try_advisory_lock key
)

// Run copies until ctx ends.
func (m *Mirror) Run(ctx context.Context) {
	every := m.Every
	if every <= 0 {
		every = 5 * time.Second
	}
	_, fresh, err := m.CH.migrate(ctx)
	if err == nil && fresh {
		// A new or emptied database: copy every event again.
		err = Reset(ctx, m.Pool)
	}
	if err != nil && ctx.Err() == nil {
		track.Fail(ctx, m.Log, track.Event{Name: "event_mirror.failed", ActorType: "system", Surface: "system"}, "unavailable", err)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		more, err := m.Step(ctx)
		if err != nil && ctx.Err() == nil {
			track.Fail(ctx, m.Log, track.Event{Name: "event_mirror.failed", ActorType: "system", Surface: "system"}, "unavailable", err)
			_, _ = m.Pool.Exec(ctx, `UPDATE event_mirror SET last_error = $2, last_run_at = now() WHERE store = $1`, mirrorStore, err.Error())
		}
		m.report(ctx)
		if more && err == nil {
			continue // a full batch: there is more waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// report sends what the mirror did at most once an hour.
func (m *Mirror) report(ctx context.Context) {
	if time.Since(m.lastReport) < time.Hour || m.copied+m.tombstones == 0 {
		return
	}
	track.Emit(ctx, track.Event{Name: "event_mirror.synced", ActorType: "system", Surface: "system",
		Props: map[string]any{"store": mirrorStore, "rows": m.copied, "deletions": m.tombstones}})
	m.lastReport, m.copied, m.tombstones = time.Now(), 0, 0
}

// Step copies one batch of changed rows and one of deletions. more is true
// when a batch was full.
func (m *Mirror) Step(ctx context.Context) (more bool, err error) {
	lag, batch := m.Lag, m.Batch
	if lag <= 0 {
		lag = time.Minute
	}
	if batch <= 0 {
		batch = 5000
	}
	conn, err := m.Pool.Acquire(ctx)
	if err != nil {
		return false, err
	}
	defer conn.Release()
	var locked bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, mirrorLock).Scan(&locked); err != nil || !locked {
		return false, err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, mirrorLock) //nolint:errcheck

	if _, err := conn.Exec(ctx, `INSERT INTO event_mirror (store) VALUES ($1) ON CONFLICT DO NOTHING`, mirrorStore); err != nil {
		return false, err
	}
	var at *time.Time
	var id *string
	if err := conn.QueryRow(ctx, `SELECT cursor_at, cursor_id::text FROM event_mirror WHERE store = $1`, mirrorStore).Scan(&at, &id); err != nil {
		return false, err
	}
	rows, err := conn.Query(ctx, `
		SELECT id::text, workspace_id::text, record_id::text, account_id::text, brand_id::text, kind, name, raw_name,
			coalesce(user_id, ''), coalesce(anonymous_id, ''), properties, traits, context, source, occurred_at, received_at, updated_at
		FROM customer_events
		WHERE updated_at < now() - $1 * interval '1 second'
			AND ($2::timestamptz IS NULL OR (updated_at, id) > ($2, $3::uuid))
		ORDER BY updated_at, id LIMIT $4`, lag.Seconds(), at, id, batch)
	if err != nil {
		return false, err
	}
	var out []map[string]any
	var lastAt time.Time
	var lastID string
	for rows.Next() {
		var (
			eid, ws, kind, name, rawName, user, anon, source string
			rec, acc, brand                                  *string
			props, traits, ctxObj                            json.RawMessage
			occurred, received, updated                      time.Time
		)
		if err := rows.Scan(&eid, &ws, &rec, &acc, &brand, &kind, &name, &rawName, &user, &anon, &props, &traits, &ctxObj, &source, &occurred, &received, &updated); err != nil {
			rows.Close()
			return false, err
		}
		out = append(out, map[string]any{
			"id": eid, "workspace_id": ws, "record_id": rec, "account_id": acc, "brand_id": brand,
			"kind": kind, "name": name, "raw_name": rawName, "user_id": user, "anonymous_id": anon,
			"properties": string(props), "traits": string(traits), "context": string(ctxObj), "source": source,
			"occurred_at": chTime(occurred), "received_at": chTime(received), "updated_at": chTime(updated), "deleted": 0,
		})
		lastAt, lastID = updated, eid
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if err := m.CH.Insert(ctx, "events", out); err != nil {
		return false, err
	}
	if len(out) > 0 {
		if _, err := conn.Exec(ctx, `UPDATE event_mirror SET cursor_at = $2, cursor_id = $3, rows_copied = rows_copied + $4,
			last_run_at = now(), last_error = '' WHERE store = $1`, mirrorStore, lastAt, lastID, len(out)); err != nil {
			return false, err
		}
		m.copied += int64(len(out))
	}

	n, err := m.deletions(ctx, conn.Conn(), batch)
	m.tombstones += int64(n)
	return len(out) == batch || n == batch, err
}

// deletions sends a tombstone for each deleted event, then forgets them.
func (m *Mirror) deletions(ctx context.Context, conn *pgx.Conn, batch int) (int, error) {
	rows, err := conn.Query(ctx, `SELECT seq, id::text, from_workspace_id::text, name, occurred_at, deleted_at
		FROM customer_event_deletions ORDER BY seq LIMIT $1`, batch)
	if err != nil {
		return 0, err
	}
	var out []map[string]any
	var last int64
	for rows.Next() {
		var seq int64
		var id, ws, name string
		var occurred, deleted time.Time
		if err := rows.Scan(&seq, &id, &ws, &name, &occurred, &deleted); err != nil {
			rows.Close()
			return 0, err
		}
		// A tombstone has the row's sort key and a newer version than any
		// copy, so ClickHouse drops the row.
		out = append(out, map[string]any{"id": id, "workspace_id": ws, "name": name, "occurred_at": chTime(occurred),
			"received_at": chTime(deleted), "updated_at": chTime(deleted.Add(time.Microsecond)), "deleted": 1,
			"kind": "", "raw_name": "", "user_id": "", "anonymous_id": "", "properties": "{}", "traits": "{}", "context": "{}", "source": ""})
		last = seq
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(out) == 0 {
		return 0, err
	}
	if err := m.CH.Insert(ctx, "events", out); err != nil {
		return 0, err
	}
	_, err = conn.Exec(ctx, `DELETE FROM customer_event_deletions WHERE seq <= $1`, last)
	return len(out), err
}

// Reset makes the mirror copy every event again, for a new or emptied
// ClickHouse database.
func Reset(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `UPDATE event_mirror SET cursor_at = NULL, cursor_id = NULL, rows_copied = 0 WHERE store = $1`, mirrorStore)
	return err
}

// Status is how far the mirror has got.
type Status struct {
	Store      string     `json:"store"`
	CursorAt   *time.Time `json:"cursor_at"`
	RowsCopied int64      `json:"rows_copied"`
	LastRunAt  *time.Time `json:"last_run_at"`
	LastError  string     `json:"last_error"`
}

// MirrorStatus reads the mirror's row; ok is false before it first ran.
func MirrorStatus(ctx context.Context, pool *pgxpool.Pool) (Status, bool, error) {
	s := Status{Store: mirrorStore}
	err := pool.QueryRow(ctx, `SELECT cursor_at, rows_copied, last_run_at, last_error FROM event_mirror WHERE store = $1`, mirrorStore).
		Scan(&s.CursorAt, &s.RowsCopied, &s.LastRunAt, &s.LastError)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, false, nil
	}
	return s, err == nil, err
}

func chTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000000") }
