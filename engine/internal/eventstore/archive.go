package eventstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/outflareai/outflare/engine/internal/blobs"
	"github.com/outflareai/outflare/engine/internal/track"
)

// Archiver keeps product events in Postgres for Days (OUTFLARE_EVENT_
// RETENTION_DAYS). Older events are first written to object storage, when
// a bucket is set up, as gzipped newline-delimited JSON under
// events/<workspace>/<yyyy-mm>/, then removed from Postgres. ClickHouse
// keeps its copy: archiving deletes without tombstones.
type Archiver struct {
	Pool  *pgxpool.Pool
	Blobs *blobs.Store // nil: old events are only removed
	Log   *slog.Logger
	Days  int
	// Chunk is the most events in one file. Default 50,000.
	Chunk int
}

// Run archives every six hours until ctx ends.
func (a *Archiver) Run(ctx context.Context) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		if err := a.Step(ctx, time.Now()); err != nil && ctx.Err() == nil {
			track.Fail(ctx, a.Log, track.Event{Name: "event_archive.failed", ActorType: "system", Surface: "system"}, "unavailable", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Step archives what is past retention at now, workspace by workspace,
// and removes the files of workspaces that were deleted.
func (a *Archiver) Step(ctx context.Context, now time.Time) error {
	if a.Days <= 0 {
		return nil
	}
	if err := a.dropDeleted(ctx); err != nil {
		return err
	}
	cutoff := now.UTC().AddDate(0, 0, -a.Days)
	rows, err := a.Pool.Query(ctx, `SELECT id::text FROM workspaces ORDER BY id`)
	if err != nil {
		return err
	}
	wss, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	for _, ws := range wss {
		files, events, err := a.workspace(ctx, ws, cutoff)
		if err != nil {
			return fmt.Errorf("workspace %s: %w", ws, err)
		}
		if events > 0 {
			track.Emit(ctx, track.Event{Name: "events.archived", WorkspaceID: ws, ActorType: "system", Surface: "system",
				Props: map[string]any{"events": events, "files": files, "kept_in_storage": a.Blobs != nil, "retention_days": a.Days}})
		}
	}
	return nil
}

func (a *Archiver) workspace(ctx context.Context, ws string, cutoff time.Time) (files, events int, err error) {
	chunk := a.Chunk
	if chunk <= 0 {
		chunk = 50000
	}
	for {
		var oldest *time.Time
		if err := a.Pool.QueryRow(ctx, `SELECT min(occurred_at) FROM customer_events WHERE workspace_id = $1`, ws).Scan(&oldest); err != nil {
			return files, events, err
		}
		if oldest == nil || !oldest.Before(cutoff) {
			return files, events, nil
		}
		month := time.Date(oldest.Year(), oldest.Month(), 1, 0, 0, 0, 0, time.UTC)
		end := month.AddDate(0, 1, 0)
		if end.After(cutoff) {
			end = cutoff
		}
		n, err := a.chunk(ctx, ws, month, end, chunk)
		if err != nil || n == 0 {
			// Nothing movable yet: the ClickHouse mirror hasn't copied it.
			return files, events, err
		}
		files++
		events += n
	}
}

// chunk moves up to limit events of ws between from and to into one file.
func (a *Archiver) chunk(ctx context.Context, ws string, from, to time.Time, limit int) (int, error) {
	n := 0
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, record_id::text, account_id::text, brand_id::text, kind, name, raw_name, user_id, anonymous_id,
				properties, traits, context, source, message_id, occurred_at, received_at, request_id
			FROM customer_events WHERE workspace_id = $1 AND occurred_at >= $2 AND occurred_at < $3
				-- With a ClickHouse mirror, only what it has already copied.
				AND NOT EXISTS (SELECT 1 FROM event_mirror m WHERE m.cursor_at IS NULL OR customer_events.updated_at > m.cursor_at)
			ORDER BY occurred_at, id LIMIT $4`, ws, from, to, limit)
		if err != nil {
			return err
		}
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		enc := json.NewEncoder(zw)
		var ids []string
		var first, last time.Time
		for rows.Next() {
			var (
				id, kind, name, rawName, source, reqID string
				rec, acc, brand, user, anon, msgID     *string
				props, traits, ctxObj                  json.RawMessage
				occurred, received                     time.Time
			)
			if err := rows.Scan(&id, &rec, &acc, &brand, &kind, &name, &rawName, &user, &anon, &props, &traits, &ctxObj, &source, &msgID, &occurred, &received, &reqID); err != nil {
				rows.Close()
				return err
			}
			if err := enc.Encode(map[string]any{"id": id, "workspace_id": ws, "record_id": rec, "account_id": acc, "brand_id": brand,
				"kind": kind, "name": name, "raw_name": rawName, "user_id": user, "anonymous_id": anon, "properties": props,
				"traits": traits, "context": ctxObj, "source": source, "message_id": msgID, "occurred_at": occurred,
				"received_at": received, "request_id": reqID}); err != nil {
				rows.Close()
				return err
			}
			if len(ids) == 0 {
				first = occurred
			}
			last = occurred
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if err := zw.Close(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		key := ""
		if a.Blobs != nil {
			key = fmt.Sprintf("events/%s/%s/%s-%s.ndjson.gz", ws, from.Format("2006-01"), first.UTC().Format("20060102T150405.000000"), ids[0])
			if err := a.Blobs.Put(ctx, key, "application/gzip", buf.Bytes()); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO event_archives (from_workspace_id, month, object_key, events, bytes, first_at, last_at)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`, ws, from, key, len(ids), buf.Len(), first, last); err != nil {
				return err
			}
		}
		// Archiving isn't a deletion: ClickHouse keeps these.
		if _, err := tx.Exec(ctx, `SET LOCAL outflare.archiving = 'on'`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM customer_events WHERE workspace_id = $1 AND id = ANY($2::uuid[])`, ws, ids); err != nil {
			return err
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// dropDeleted removes the archived files of workspaces that no longer
// exist.
func (a *Archiver) dropDeleted(ctx context.Context) error {
	rows, err := a.Pool.Query(ctx, `SELECT id::text, object_key FROM event_archives e
		WHERE NOT EXISTS (SELECT 1 FROM workspaces w WHERE w.id = e.from_workspace_id) LIMIT 1000`)
	if err != nil {
		return err
	}
	type file struct{ ID, Key string }
	files, err := pgx.CollectRows(rows, pgx.RowToStructByPos[file])
	if err != nil {
		return err
	}
	for _, f := range files {
		if a.Blobs != nil {
			if err := a.Blobs.Delete(ctx, f.Key); err != nil {
				return err
			}
		}
		if _, err := a.Pool.Exec(ctx, `DELETE FROM event_archives WHERE id = $1`, f.ID); err != nil {
			return err
		}
	}
	return nil
}
