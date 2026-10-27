# Scaling

One process runs all of Outflare: the app, the API, website event collection, the job workers and the background loops. That is all a small install needs. A larger one runs the same image several times, each with a role, so the parts deploy, restart and scale apart:

| `OUTFLARE_ROLE` | Answers | Runs jobs and loops | Use it for |
| --- | --- | --- | --- |
| `all` (default) | Everything | Yes | A single server. |
| `api` | The app, API, MCP and website events | No | The service people and agents use. Run two or more for deploys without downtime. |
| `ingest` | `POST /v1/site/events`, `/site.js` and health checks | No | Collecting website events on their own address (for example `t.example.com`), so a busy or deploying app never drops a visit. |
| `worker` | Health checks only | Yes | Sending, syncs, crawls, analytics copies and every other job. |

Every role:

- runs the migrations at start-up, under a lock, so starting several at once is safe;
- serves `/healthz` and `/readyz`;
- keeps the metrics listener (`OUTFLARE_METRICS_ADDR`), with the role as a label on `outflare_build_info` ([monitoring](monitoring.md)).

A request a role doesn't answer gets a `404` with error code `not_found`.

## Website events never wait on the app

`POST /v1/site/events` does as little as it can before it answers: it checks the site key, the origin, the rate and daily limits and each event's shape, writes the batch to the `site_event_inbox` table as one row, and answers. Storing the events (sessions, people, touches, the catalog) happens afterwards, in a worker. So a slow query, a big import or a worker deploy only makes events appear a little later; it never makes a site's events fail.

- Each workspace's events are stored in the order they arrived, by one worker at a time, as of the time they arrived. With the worker in the same process they appear at once; on a split install, within a second.
- A row that fails is tried again up to 5 times, then set aside with its error (`failed_at`, `last_error`) instead of blocking the rows after it. Each failure is a `site_events.store_failed` event and an error log line.
- `node scripts/load-site-events.mjs <URL> <site key> [seconds] [concurrency]` sends events to your own test install as fast as it can and prints throughput, status codes and latency. On a 4-CPU test machine with 15 database connections, one server took about 1,000 requests (2,000 events) a second at 50 at once, answering in under 80 ms at p95, and its worker stored about 500 events a second for one workspace. Storing events during the request, as before, managed about 10 a second at that concurrency, with deadlocks failing a quarter of the requests.
- `outflare_inbox_pending` and `outflare_inbox_oldest_seconds` say how far behind the workers are; if no worker runs, events wait in the inbox and are stored when one starts ([monitoring](monitoring.md)).

## A split install

1. Run one `worker` service. The job queue is safe to share, but not every background loop has been checked for running twice, so keep a single worker for now.
2. Run one or more `api` services behind your load balancer, at the app's address (`PUBLIC_URL`).
3. Optionally, run one or more `ingest` services at a tracking address, and load the snippet from it (`<script src="https://t.example.com/site.js" ...>`). The `api` services still take website events, so nothing breaks before the snippet moves.
4. Give each service its own share of database connections with `pool_max_conns` in `DATABASE_URL`, so the total stays under the database's `max_connections`. For example, with a limit of 100: api 2 × 15, ingest 2 × 5, worker 1 × 45 (the worker needs about `WORKERS` plus 10). [Query](query.md) opens its own connections on top, up to `OUTFLARE_QUERY_CONNECTIONS` (default 3) per api server, so count those too or lower `pool_max_conns` by as many.

All services share the same settings. `OUTFLARE_SECRET_KEY` must be the same everywhere.
