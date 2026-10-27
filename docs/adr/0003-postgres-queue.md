# 3. Postgres as the job queue

Status: Recommended (22 October 2026)

## Decision

Jobs are rows in Postgres, claimed with `FOR UPDATE SKIP LOCKED`. Enqueue sends `NOTIFY` so idle workers wake at once; workers also poll every second to catch scheduled jobs and missed notifications. No Redis in v1.

## Why

One fewer service to run when self-hosting. Jobs can be enqueued in the same transaction as the write that caused them, so a send job exists exactly when its message row committed. Postgres comfortably handles the job rates a sequencer needs. Redis or a dedicated queue can be added behind the same interface if a deployment outgrows this.
