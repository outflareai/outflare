# 10. Analytics storage: ClickHouse and object storage beside Postgres

Status: Accepted (27 October 2026)

## Context

Outflare keeps product events (`customer_events`: page views from website tracking, a product's own track calls, Outflare's own usage events) in Postgres, and is growing PostHog-style analytics on them: trends, funnels, retention, paths and experiments. Those questions scan many rows. Postgres answers them well up to tens of millions of events per workspace with partitions and rollups, and then gets slow; analytics tools such as PostHog and Plausible run on ClickHouse for that reason. Old raw events and large files (exports, recordings, screenshots) also don't belong in a transactional database.

[ADR 3](0003-postgres-queue.md) keeps Postgres as the only dependency, so a self-hosted install is one binary and one database. That stays true.

## Decision

Two optional stores, each switched on by its own setting. Without them Outflare works exactly as before, on Postgres alone.

1. **ClickHouse for analytics** (`OUTFLARE_CLICKHOUSE_URL`). Postgres stays the source of truth: ingest, identity matching, journeys and the timeline keep writing and reading `customer_events` in the same transaction as today. A mirror copies every new or changed event, and a tombstone for every deleted one, to a ClickHouse `events` table (`ReplacingMergeTree`, partitioned by month, ordered by workspace, name, day and id). Analytics queries read ClickHouse when it is configured and Postgres when it isn't, behind one interface, so a feature is written once.
   - The mirror reads `customer_events` by `(updated_at, id)` with a short lag, so rows committed out of order aren't skipped, and keeps its cursor in Postgres. A record merge or deletion changes `updated_at` (a trigger), so ClickHouse gets the new `record_id`. Deletions go through `customer_event_deletions`.
   - The engine creates and upgrades the ClickHouse schema itself, as it does Postgres migrations.
   - Every query filters by `workspace_id`, as in Postgres.
2. **S3-compatible object storage for files** (`OUTFLARE_S3_*`: Cloudflare R2, Amazon S3, MinIO or any S3-compatible store). First use: before events pass a workspace's retention, they are archived to the bucket as compressed newline-delimited JSON per workspace and month, so retention never loses data the operator wants. Later uses: exports, heatmap screenshots, recordings.

Both are infrastructure the operator runs Outflare on, like `DATABASE_URL`, never a workspace's third-party account, so they don't touch the bring-your-own-keys rule. Their credentials live only in the environment.

## Why

- **Scale from the start without forcing it on everyone.** Cloud runs analytics on ClickHouse from day one; a small self-hosted install keeps one database.
- **One write path.** Writing events to Postgres first keeps identity matching, journeys and the audit trail transactional. ClickHouse only ever holds a copy that can be rebuilt by resetting the mirror's cursor.
- **No new client libraries.** ClickHouse is reached over its HTTP interface and object storage with signed S3 requests from the standard library, so no SDK reads ambient credentials.

## Later

If ingest outgrows Postgres (many thousands of events a second), page views and other anonymous events can go straight to ClickHouse and only events tied to a person to Postgres. That is a change behind the same interface, and would get its own ADR.
