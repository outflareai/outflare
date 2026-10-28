# Monitoring

Outflare can report how busy, slow and healthy it is as Prometheus metrics, for your own dashboards and alerts: request rates and latency per route, the job queue, the database connection pool, Postgres itself, and the server's CPU, memory and goroutines. It is off unless you turn it on, and nothing leaves the server: you point your own Prometheus, Grafana Alloy or Grafana Agent at it.

This is separate from [product analytics](product-analytics.md) (what people do, sent to your PostHog) and the [anonymous usage report](telemetry.md). Metrics carry route patterns, job kinds, outcomes and table names only, never workspace ids, people, paths with ids or anything a customer typed.

## Turn it on

Set `OUTFLARE_METRICS_ADDR` to a second, private address, such as `:9091`. The server then serves `GET /metrics` there, on its own listener, never on the public port (it refuses to start on `HTTP_ADDR`'s port).

| Variable | Default | What it does |
| --- | --- | --- |
| `OUTFLARE_METRICS_ADDR` | empty (off) | Where to serve `/metrics`, such as `:9091`. Keep it off the public internet. |
| `OUTFLARE_METRICS_TOKEN` | empty | When set, scrapers must send `Authorization: Bearer <token>`. Use one whenever other services share the network. |
| `OUTFLARE_METRICS_PPROF` | `false` | Also serve Go's profiler at `/debug/pprof/` on the same listener, to see what uses CPU or memory. Profiles can contain request data: only on a private listener. |

If the listener can't start, the server logs it and records `metrics.listen_failed`, and carries on serving the API and running jobs.

## Dashboards and alerts with Docker Compose

`deploy/docker-compose.yml` turns the metrics on inside Docker's own network (`:9091`, never published) and has an optional `monitoring` profile with Prometheus and Grafana, already set up:

```sh
echo "GRAFANA_ADMIN_PASSWORD=$(openssl rand -base64 18)" >> deploy/.env
docker compose -f deploy/docker-compose.yml --profile monitoring up -d
```

Open <http://localhost:3000> (bound to localhost only), sign in as `admin`, and open **Outflare** in the Outflare folder. Prometheus keeps 30 days (`PROMETHEUS_RETENTION`) and evaluates the alert rules in `deploy/monitoring/alerts.yml`; see them under Alerting in Grafana or on Prometheus's Alerts page. To be notified, add a contact point in Grafana or an Alertmanager.

## Grafana Cloud or your own Prometheus

- **Scrape** the metrics address every 15 to 30 seconds with job name `outflare` (the dashboard and rules expect it), with the bearer token if you set one. On Grafana Cloud, run Grafana Alloy next to the server with a `prometheus.scrape` that `forward_to`s a `prometheus.remote_write` for your stack.
- **Dashboard:** import `deploy/monitoring/grafana/dashboards/outflare.json` (Dashboards → New → Import) and choose your Prometheus data source.
- **Alerts:** load `deploy/monitoring/alerts.yml` as they are into Prometheus, or into Grafana Cloud with `mimirtool rules load deploy/monitoring/alerts.yml`.

Every Outflare metric starts with `outflare_`, so it is easy to keep only those if your plan counts series.

## What is measured

| Metric | Labels | Meaning |
| --- | --- | --- |
| `outflare_http_requests_total` | `route`, `code` (`2xx`...`5xx`) | Requests served. `route` is the route pattern (`GET /v1/workspaces/{ws}/leads`), `unmatched` when none matched. |
| `outflare_http_request_duration_seconds` | `route` | Request time histogram. `GET /mcp` is the hosted MCP server's event stream and stays open, so leave it out of latency. |
| `outflare_http_requests_in_flight` | | Requests being served now. |
| `outflare_http_rate_limited_total` | `limit` (`api`, `register`, `site_ip`, `site_key`, `site_visitor`, `site_flags`) | Requests a rate limit refused with 429. |
| `outflare_jobs_processed_total` | `kind`, `outcome` (`succeeded`, `retried`, `dead`) | Jobs this server ran. |
| `outflare_jobs_duration_seconds`, `outflare_jobs_wait_seconds` | `kind` | How long a job ran, and how long it waited after it was due before a worker took it. |
| `outflare_jobs_reaped_total` | `kind` | Running jobs put back in the queue because their worker stopped holding them (a crash, or a job longer than the lease). Such a job may run twice. |
| `outflare_jobs_active`, `outflare_jobs_workers` | | Jobs running in this process, and how many workers it has (`WORKERS` plus the send queue's). |
| `outflare_jobs_queued`, `outflare_jobs_scheduled`, `outflare_jobs_running` | `kind` | Whole-queue depth from the database: due now, waiting for a later time (retries, schedules), and claimed. |
| `outflare_jobs_oldest_due_seconds` | `kind` | How long the longest-waiting due job has waited. The best single sign that workers can't keep up. |
| `outflare_ingest_events_total` | `outcome` (`accepted`, `rejected`, `capped`) | Website events the collector received: queued, refused for their shape, or turned away by a daily limit. |
| `outflare_inbox_events_total` | `outcome` (`stored`, `rejected`, `failed`) | Website events workers took from the inbox. `rejected` is an event the workspace refused (an unapproved name over the limit, a record conflict); `failed` is a batch that will be tried again. |
| `outflare_inbox_batch_seconds` | | Time to store one batch from the inbox. |
| `outflare_inbox_pending`, `outflare_inbox_oldest_seconds`, `outflare_inbox_failed` | | Website event batches waiting for a worker, how long the oldest has waited, and batches set aside after 5 failures. |
| `outflare_db_pool_*` | | The engine's connection pool: connections in use, idle, open and the maximum, and how often and how long callers waited for a free one. |
| `outflare_pg_connections` | `state` | Connections to the database by state (`active`, `idle`, `idle in transaction`), next to `outflare_pg_max_connections`. |
| `outflare_pg_longest_transaction_seconds`, `outflare_pg_lock_waiting_connections` | | The oldest open transaction, and connections waiting on a lock. |
| `outflare_pg_commits_total`, `_rollbacks_total`, `_blocks_hit_total`, `_blocks_read_total`, `_deadlocks_total`, `_temp_bytes_total` | | The database's own counters: throughput, cache hits, deadlocks and queries spilling to disk. |
| `outflare_pg_database_bytes` | | Size of the database. |
| `outflare_pg_table_bytes`, `outflare_pg_table_dead_rows`, `outflare_pg_table_seq_scans_total` | `table` | For the 25 largest tables: size with indexes, rows waiting for vacuum, and sequential scans (a large table scanned often is missing an index). |
| `outflare_pipeline_items` | `pipeline`, `stage`, `state` (`due`, `scheduled`, `working`, `held`) | Pipeline items now, from the database ([pipeline.md](pipeline.md)). |
| `outflare_pipeline_oldest_due_seconds` | `pipeline`, `stage` | How long the longest-waiting due item of a stage has waited. The highest is the bottleneck. |
| `outflare_pipeline_items_total`, `outflare_pipeline_codes_total` | `pipeline`, `stage`, `outcome` or `code` | What stages decided in this process, and why items were skipped, held, retried, made to wait, paused or failed. |
| `outflare_pipeline_batch_seconds`, `outflare_pipeline_batch_items`, `outflare_pipeline_wait_seconds` | `pipeline`, `stage` | Time and size of a batch, and the wait from an item being due to a worker claiming it. |
| `outflare_pipeline_limit_waits_total`, `outflare_pipeline_limit_reports_total` | `account`, `reason` or `outcome` | Calls to outside accounts held back by a rate limit or breaker, and how calls went. `account` is the kind (`integration`, `ai`), never an id. |
| `outflare_pipeline_runs`, `outflare_pipeline_breakers` | `pipeline` and `state`, or `account` and `state` | Runs running or paused, and accounts whose breaker is open or half open. |
| `outflare_ai_calls_total`, `outflare_ai_cost_cents_total` | `provider`, `task` | AI model calls made with workspaces' own keys, and what they cost on the workspaces' own provider bills in US cents, by provider (connector id) and tool (`plan`, `reply_labelling`...). Never by workspace. |
| `outflare_ai_tokens_total` | `provider`, `task`, `kind` (`input`, `output`, `cache_read`, `cache_write`) | Tokens those calls used. |
| `outflare_ai_budget_alerts_total` | `level` (`80`, `100`), `scope` (`workspace`, `tool`) | Workspaces warned that their AI spend reached 80% or all of a monthly limit. |
| `outflare_connection_limits_alerts_total` | `level` (`80`, `100`), `period` (`day`, `month`) | Workspaces warned that a paid connection used 80% of a usage limit, or reached it ([connection limits](usage-limits.md)). |
| `outflare_connection_limits_stops_total` | `connector`, `period` | Paid calls Outflare didn't make because the connection had reached a limit that stops calls. |
| `outflare_hooks_deliveries_total` | `connector`, `outcome` (`accepted`, `duplicate`, `bad_signature`, `not_found`, `invalid`, `error`) | Webhooks connectors sent to a connection's live updates address (`/v1/hooks/...`). |
| `outflare_hooks_events_total` | `connector`, `result` (`handled`, `ignored`, `retried`, `failed`) | What workers did with each event a webhook carried. |
| `outflare_hooks_handle_lag_seconds` | `connector` | Time from a webhook arriving to its event being handled. |
| `outflare_query_runs_total` | `outcome` (`success`, `invalid`, `timeout`, `unavailable`, `error`) | [Queries](query.md) run. |
| `outflare_query_duration_seconds`, `outflare_query_rows_total` | | How long queries took, and the rows they returned. |
| `outflare_smart_lists_lists`, `outflare_smart_lists_oldest_refresh_seconds`, `outflare_smart_lists_failing` | | [Smart lists](lists.md#smart-lists): how many there are, how long since the least recently refreshed one was brought up to date (normal is under 20 minutes), and how many failed their last refresh. |
| `outflare_ad_conversions_total` | `connector`, `outcome` (`sent`, `skipped`, `failed`) | Conversions sent back to ad platforms ([paid-media.md](paid-media.md#sending-conversions-back)). |
| `outflare_ad_conversions_oldest_queued_seconds` | | How long the oldest conversion waiting to be sent has waited. |
| `outflare_flags_requests_total` | `surface` (`site`, `api`), `outcome` (`ok`, `refused`, `error`) | Requests to decide a visitor's [feature flags](feature-flags.md): from the website script or a server. |
| `outflare_flags_request_seconds` | `surface` | Time to decide them. |
| `outflare_flags_cache_total` | `result` (`hit`, `miss`) | Lookups of a workspace's flags answered from memory or read from Postgres (at most every 10 seconds per process). |
| `outflare_reviews_page_reads_total` | `platform`, `outcome` (`success`, `not_a_review_page`, `connector_error`) | Reads of brands' [review pages](reviews.md). |
| `outflare_reviews_new_total` | `platform` | Reviews kept for the first time. |
| `outflare_reviews_pages` | `platform`, `state` (`ok`, `failing`) | Active review pages, and how many failed their last read. |
| `outflare_reviews_oldest_read_seconds` | `platform` | Time since the least recently read active review page was read. |
| `outflare_build_info` | `version`, `role` | The running version and role (`OUTFLARE_ROLE`); the dashboard marks each change of version as a deploy. |
| `outflare_metrics_scrape_errors`, `outflare_metrics_scrape_duration_seconds` | | Database queries that failed in the last scrape, and how long it took. |
| `go_*`, `process_*` | | Go runtime and process: CPU seconds, resident memory, heap, goroutines, garbage collection, open files. |

The database numbers are read when Prometheus scrapes, at most once every 10 seconds whatever the number of scrapers, with a 5-second limit, using the partial indexes on queued and running jobs.

## Reading it

- **Requests slow, pool full.** `outflare_db_pool_acquired_conns` at `outflare_db_pool_max_conns` with `empty_acquire_wait_seconds` rising means requests and jobs queue for a connection. The pool's size comes from `DATABASE_URL` (`?pool_max_conns=20`); by default it is the larger of 4 and the number of CPUs, which is small next to 32 workers. Keep the total across servers under `outflare_pg_max_connections`.
- **Queue backing up.** `outflare_jobs_oldest_due_seconds` climbing for one kind while `outflare_jobs_active` equals `outflare_jobs_workers` means more `WORKERS` (or more servers); climbing while workers are idle means jobs are failing and retrying (`outcome="retried"`) or waiting on the pool.
- **Pipeline stage behind.** The stage with the highest `outflare_pipeline_oldest_due_seconds` is the bottleneck. If its batches are slow (`outflare_pipeline_batch_seconds`) and "Why items stop" shows rate limits or open breakers, the outside account is the limit. If batches are fast but small, the per-workspace cap is binding. Otherwise it needs more workers.
- **Website events behind.** `outflare_inbox_oldest_seconds` over a few seconds means workers aren't keeping up with the collector (or none is running); the events are safe in the inbox and are stored once they catch up. A rising `outflare_inbox_failed` means batches are being set aside: look for `site_events.store_failed` in the logs.
- **Webhooks failing.** A rise in `outflare_hooks_deliveries_total{outcome="bad_signature"}` for one connector means its signing secret changed: make a new address or paste the new secret on the connection. `not_found` means a tool still sends to an address that was replaced or turned off. Failed events are listed under Logs → Incoming webhooks with their error code, and can be run again.
- **Database pressure.** A cache hit ratio under 99%, rising `temp_bytes`, long transactions or many dead rows point at the query or table to look at.

## Alerts

`deploy/monitoring/alerts.yml` has rules for: the server not answering; over 2% of requests failing with a 5xx; p95 latency over 2 seconds; a due job waiting over 10 minutes; website events waiting over 5 minutes to be stored, or batches set aside; jobs of one kind dying; jobs put back after their lease ran out; the pool over 90% in use or callers waiting on it; Postgres over 80% of `max_connections`; a transaction open over 5 minutes; deadlocks; over 20,000 goroutines; a pipeline stage with an item waiting over 30 minutes, over 10% of a stage's items retrying or failing, items failing for good, or an outside account's breaker open for 10 minutes; the database growing over 25% in a day (once it is over 1 GB); database metrics that can't be read; one tool making over 5 AI calls a second for 15 minutes (a loop spending workspaces' money); over 20 AI spend limits reached in an hour; a paid connection reaching its daily or monthly usage limit (info: its workspace's admins are emailed); over 500 calls an hour through one connector refused at a usage limit for 30 minutes (a feature retrying into the limit); over 20 webhook deliveries with a bad signature for one connector in 15 minutes; over 5 webhook events failing in 30 minutes; webhook events waiting over 5 minutes to be handled; over half of Domain Connect lookups failing (a DNS provider's API down, or the signing key's TXT record at outflare.ai missing); Query unable to set up its database role; over a quarter of queries timing out; meaning-based search on in workspaces while the database has no pgvector; most embedding jobs failing; a smart list not refreshed for over an hour; smart lists whose last refresh failed (info); over a quarter of conversions sent to one ad platform failing; a conversion waiting over 30 minutes to be sent; over 2% of feature flag requests failing; flags taking over 250 ms to decide at the 95th percentile; a review page not read for two days; and over half of review page reads failing. The thresholds suit one server; adjust them in your copy.

## Metrics from extensions

An extension registers its own Prometheus collectors with `Host.Metrics()`, so they are served on the same metrics address and reach the same Prometheus. Name them `outflare_<extension>_*`. The dashboards and alerts for them ship with the extension, not here.
