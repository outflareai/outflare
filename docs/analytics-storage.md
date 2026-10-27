# Analytics storage

Product events (`customer_events`: site visits, tracked events, your server's own usage) live in Postgres, which is all a small install needs. Two optional pieces let the same server grow to billions of events. The design and its reasons are in [ADR 0010](adr/0010-analytics-storage.md).

| Piece | Setting | What it does |
| --- | --- | --- |
| ClickHouse | `OUTFLARE_CLICKHOUSE_URL` | Keeps a copy of every product event for fast reports over large volumes. |
| Object storage | `OUTFLARE_S3_*` | Holds archived events and, later, session recordings. Cloudflare R2, Amazon S3, MinIO or anything S3-compatible. |
| Retention | `OUTFLARE_EVENT_RETENTION_DAYS` | How long events stay in Postgres. |

None of these are workspace features: they run your server, like Postgres does, so the operator sets them in the environment.

## ClickHouse

```sh
OUTFLARE_CLICKHOUSE_URL=http://user:password@clickhouse.example.com:8123/outflare
```

The server talks to ClickHouse over its HTTP interface (port 8123, or 8443 with `https://`). On start-up it creates its tables in the database named in the path (or `?database=`), then copies events in the background:

- New and changed events are copied about a minute after they are written, in order, so a late commit is never skipped.
- Edits (such as a customer merge moving events to another record) copy the newer version; ClickHouse keeps the latest.
- Deleted events, including a deleted workspace's, are removed from ClickHouse too.
- One server copies at a time, so running several is safe.

Problems are logged and recorded as `event_mirror.failed`; the copy picks up where it stopped. To copy everything again, point the URL at a new, empty database and restart: when the server creates its tables it starts the copy from the first event.

## Object storage

```sh
OUTFLARE_S3_ENDPOINT=https://<account>.r2.cloudflarestorage.com
OUTFLARE_S3_REGION=auto            # R2; your bucket's region on S3
OUTFLARE_S3_BUCKET=outflare
OUTFLARE_S3_ACCESS_KEY_ID=...
OUTFLARE_S3_SECRET_ACCESS_KEY=...
```

Set all of them or none. Give the key read, write, list and delete on that one bucket only. At start-up the server writes, reads and deletes a small `.outflare-check` file; if that fails it logs `object_storage.failed` and pauses archiving rather than deleting anything.

## Retention

```sh
OUTFLARE_EVENT_RETENTION_DAYS=400   # 0 (the default) keeps everything
```

Every six hours, events older than this are moved out of Postgres:

- With a bucket, they are first written as gzipped newline-delimited JSON under `events/<workspace>/<yyyy-mm>/`, one row per file in `event_archives`. Deleting a workspace removes its files too.
- With ClickHouse, only events it has already copied are moved, and ClickHouse keeps them, so reports still cover all time.
- With neither, old events are deleted.

Each run records `events.archived` per workspace with the number of events and files.
