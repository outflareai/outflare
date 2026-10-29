# Query

Query runs read-only SQL over the workspace's own data, so a question such as "win rate by source this quarter" or "people who visited pricing and have no deal" gets an exact answer in one call, instead of being paged together from lists. People and agents use the same tool, and it sees exactly what the caller may see through the API.

```sh
curl -X POST "$OUTFLARE_URL/v1/workspaces/$WS/query" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"sql": "select stage, count(*), sum(amount_cents) from deals group by stage order by 2 desc"}'
```

```json
{
  "columns": [{"name": "stage", "type": "text"}, {"name": "count", "type": "int8"}, {"name": "sum", "type": "numeric"}],
  "rows": [["qualified", 12, 4800000], ["won", 5, 2100000]],
  "row_count": 2, "truncated": false, "duration_ms": 14
}
```

Agents use the MCP tools `list_query_tables` and `run_query`.

## Tables

`GET /v1/workspaces/{ws}/query/tables` (MCP `list_query_tables`) lists the tables with their columns, the read scope each needs, and whether you hold it.

| Table | Scope | What it holds |
| --- | --- | --- |
| `people`, `companies` | `customers.read` | Customer records of each type, without merged or archived ones, with their main email or domain |
| `records` | `customers.read` | Every record of every type, including merged (`merged_into`) and archived ones |
| `record_identities`, `record_relationships` | `customers.read` | Emails, phones, ids and domains; links between records |
| `customer_events`, `consents`, `subscriptions` | `customers.read` | Product events, the consent log, payment subscriptions (`monthly_minor` is MRR in minor units) |
| `brands` | `workspace.read` | The workspace's brands |
| `projects` | `projects.read` | Former projects (removed), kept until a later release drops them |
| `leads` | `leads.read` | Leads for outreach |
| `sequences`, `enrollments`, `messages` | `sequences.read` | Sequences, who is in them, and the emails they sent (without bodies) |
| `replies` | `inbox.read` | Mail received, with its AI label, without bodies |
| `deals`, `meetings`, `spend` | `outcomes.read` | Outcomes and money spent |
| `emails`, `email_events`, `broadcasts` | `emails.read` | Lifecycle email, without bodies or addresses |
| `visits` | `analytics.read` | Website visits from the Outflare site snippet |
| `social_posts` | `social.read` | Posts on the workspace's own social accounts |
| `reviews`, `review_page_days` | `social.read` | [Reviews](reviews.md) of the brands on Trustpilot, G2, Capterra and Google, and each review page's numbers day by day |

jsonb columns such as `attributes` are read with `->>`: `select attributes->>'plan' as plan, count(*) from people group by 1`.

## What a query can see

- **This workspace only.** Every table shows only the workspace's own rows.
- **Your brands.** A caller kept to some brands (a brand token, or a member limited to brands) sees those brands' rows and the shared ones, as in the API. Where a row's brand comes from elsewhere, it is the same as on a record's timeline: a message's, meeting's or deal's sequence, a reply's mailbox, a record's own brand for its identities and consents. `leads` has no brand, so it is hidden from brand-limited callers.
- **Your scopes.** Each table needs its read scope. A query naming a table you can't read is refused with `forbidden`, naming the scope.

## What a query can do

- One `SELECT` (or `WITH ... SELECT`). Anything else, or a second statement, fails with `query_invalid` and the database's own message.
- Nothing is ever changed: the query runs in a read-only transaction that is always rolled back, so an agent kept to missions may run it outside one.
- Up to `limit` rows come back (default 100, at most 1,000, and at most 4 MB); `truncated` says there were more.
- A query running over 15 seconds is stopped with `query_timeout`. Count and group rather than list.

## How it is kept safe

Query runs as a database role of its own, one per workspace, that can read only the views in the `query` schema (migration `0199`) and no table. Each view filters by the workspace, brands and scopes of the request, which the server writes for the query's database connection just before it runs and removes after. The role can neither read nor change that. Every query opens a fresh connection that is closed afterwards, so nothing a query sets lasts. Because each workspace has its own role, the database never shows one workspace's running query to another. Functions that change settings, take locks, signal other sessions or run SQL from a string are refused too. That gives a clear message, but the boundary is the role and the read-only transaction.

**Self-hosting:** Query needs the database user in `DATABASE_URL` to be able to create roles (`CREATEROLE`, or a superuser, as with the Docker Compose and most managed Postgres). The role's password is derived from `OUTFLARE_SECRET_KEY`, and the role logs in with the same host and options as `DATABASE_URL`, so `pg_hba.conf` must allow password logins for it. Without that, Query answers `query_unavailable` and the log says why. Each server runs at most 3 queries at once (`OUTFLARE_QUERY_CONNECTIONS`, 0 turns Query off), each on its own connection, so allow for that many more connections per API server.

## Events

Each query is an `api.called` event (`runQuery`), and a successful one also records `query.ran` with the tables it read, the row count, whether it was truncated and how long it took. The SQL itself is never sent. A failure to set up the role is `query.setup_failed`. Metrics: `outflare_query_runs_total{outcome}`, `outflare_query_duration_seconds` and `outflare_query_rows_total`, on the Grafana dashboard's Query row.
