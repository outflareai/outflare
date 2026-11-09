# Query

Query runs read-only SQL over one brand's data, so a question such as "win rate by source this quarter" or "people who visited pricing and have no deal" gets an exact answer in one call, instead of being paged together from lists. People and agents use the same tool, and it sees exactly what the caller may see through the API.

```sh
curl -X POST "$OUTFLARE_URL/v1/brands/$BRAND/query" \
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

`$BRAND` is the brand's id. Agents use the MCP tools `list_query_tables` and `run_query`, which take the brand's handle or id.

A query belongs to one brand (`plans/brands/README.md`): different brands are different businesses, so their data is never queried together. The workspace routes `GET /v1/brands/{brand}/query/tables` and `POST /v1/brands/{brand}/query` are deprecated and stay until the brands contract step: they query the brand `?brand=` picks (a handle or id), else the workspace's first brand (a member or token kept to some brands gets its own), and `?brand=all` queries every brand you can see, as before.

## Tables

`GET /v1/brands/{brand}/query/tables` (MCP `list_query_tables`) lists the tables with their columns, the read scope each needs, and whether you hold it.

| Table | Scope | What it holds |
| --- | --- | --- |
| `people`, `companies` | `customers.read` | Customer records of each type, without merged or archived ones, with their main email or domain |
| `records` | `customers.read` | Every record of every type, including merged (`merged_into`) and archived ones |
| `record_identities`, `record_relationships` | `customers.read` | Emails, phones, ids and domains; links between records |
| `customer_events`, `consents`, `subscriptions` | `customers.read` | Product events, the consent log, payment subscriptions (`monthly_minor` is MRR in minor units) |
| `rules`, `rule_firings` | `customers.read` | The brand's [rules](rules.md), and each time one fired for a record |
| `products`, `lifecycle_states`, `lifecycle_moves`, `lifecycle_overall` | `customers.read` | The brand's [products](product-lifecycle.md), where each company and person stands with each, every move between stages, and each company's overall status |
| `transactions` | `customers.read` | [Payments, refunds, chargebacks and credits](transactions.md), in their currency and the brand's |
| `smart_list_changes` | `customers.read` | Who joined and left each [smart or static list](lists.md#history), and when |
| `metrics`, `metric_values` | `customers.read` | The brand's [live metrics](metrics.md), and each company's and person's value, overall and per product |
| `bulk_edits` | `customers.read` | [Bulk edits](bulk-edits.md) owners ran, what they changed and how many records |
| `journeys`, `journey_runs` | `customers.read` | The brand's journeys (`steps` counts the live version's steps), and each person's way through one |
| `brands` | `workspace.read` | The brand itself |
| `leads` | `leads.read` | The brand's leads for outreach |
| `sequences`, `enrollments`, `messages` | `sequences.read` | Sequences, who is in them, and the emails they sent (without bodies) |
| `replies` | `inbox.read` | Mail received, with its AI label, without bodies |
| `deals`, `meetings`, `spend` | `outcomes.read` | Outcomes and money spent |
| `emails`, `email_events`, `broadcasts` | `emails.read` | Lifecycle email, without bodies or addresses |
| `visits` | `analytics.read` | Website visits from the Outflare site snippet, with where each came from (`country`, `region`, `city`, and the network: `asn` and `isp`) and the device (`device`, `device_brand`, `device_model`, `os`, `os_version`, `browser`, `browser_version`, `language`) |
| `social_posts` | `social.read` | Posts on the workspace's own social accounts |
| `reviews`, `review_page_days` | `social.read` | [Reviews](reviews.md) of the brands on Trustpilot, G2, Capterra, SourceForge and Google, and each review page's numbers day by day |
| `backlinks`, `link_prospects` | `seo.read` | [Link building](seo.md): links from other sites to the brands' websites, live or lost, with each linking domain's strength, traffic and spam score; and the pages that could link, with how each was found (`method`), its score and where its pitch stands (`stage`), such as `select method, count(*) filter (where live_at is not null) from link_prospects group by 1` |

jsonb columns such as `attributes` are read with `->>`: `select attributes->>'plan' as plan, count(*) from people group by 1`.

### A table per record type

Each of the brand's own record types is a table too, named by its key: a `subscription` type is `select plan, seats from subscription`, and the default `deal` type is `deal` (beside `deals`, the outcomes table). A type's table has `id`, `brand_id`, `name`, one column per active field (numbers and ratings as `numeric`, checkboxes as `boolean`, currencies, locations and multi-value fields as `jsonb`, the rest as `text`; a value of another kind reads as null), then `attributes`, `origin`, `created_at` and `updated_at`, without merged or archived records. It needs `customers.read`, and `GET .../query/tables` lists it with its columns. A type whose key another table already has (such as `people`) isn't added, and `person` and `account` are `people` and `companies`.

## What a query can see

- **This workspace only.** Every table shows only the workspace's own rows.
- **One brand.** Every customer table (`records`, `people`, `companies`, the type tables, `record_identities`, `record_relationships`, `customer_events`, `consents`, `subscriptions`) and `brands` hold only rows whose own `brand_id` is the query's brand; a row with no brand is not the brand's and is left out too. No SQL text changes that: a `brand_id` filter, a subquery, a CTE or a join only ever sees the brand's rows, and the tables underneath can't be read at all. Another brand's token or a member kept to other brands gets `404` for the brand's query routes.
- **Outreach and activation.** `leads`, `sequences`, `enrollments`, `messages`, `replies`, `meetings` and `deals` (outreach, brands step 5), and the rule, journey and email tables (step 4), hold only rows whose own `brand_id` is the query's brand.
- **Areas not yet moved to brands.** The website, social and spend tables keep the rule they had for a caller kept to some brands: the brand's rows and the ones no brand owns. Each area becomes strict as it moves (`plans/brands/README.md`, "Order").
- **Your scopes.** Each table needs its read scope. A query naming a table you can't read is refused with `forbidden`, naming the scope.

## What a query can do

- One `SELECT` (or `WITH ... SELECT`). Anything else, or a second statement, fails with `query_invalid` and the database's own message.
- Nothing is ever changed: the query runs in a read-only transaction that is always rolled back, so an agent kept to missions may run it outside one.
- Up to `limit` rows come back (default 100, at most 1,000, and at most 4 MB); `truncated` says there were more.
- A query running over 15 seconds is stopped with `query_timeout`. Count and group rather than list.

## How it is kept safe

Query runs as a database role of its own, one per workspace, that can read only the views in the `query` schema (migration `0199`) and no table. Each view filters by the workspace, brand and scopes of the request, which the server writes for the query's database connection just before it runs and removes after (the brand since migration `0342`). The role can neither read nor change that, and the views are security barriers, so the query's own conditions never see a row before the view's filter has dropped it. The type tables are temporary views the query's own connection makes just before the query, over `query.records`, so they hold no more than it does; the query's transaction is read-only, so it can't make or change one, and they go when the connection closes. Every query opens a fresh connection that is closed afterwards, so nothing a query sets lasts. Because each workspace has its own role, the database never shows one workspace's running query to another. Functions that change settings, take locks, signal other sessions or run SQL from a string are refused too. That gives a clear message, but the boundary is the role and the read-only transaction.

**Self-hosting:** Query needs the database user in `DATABASE_URL` to be able to create roles (`CREATEROLE`, or a superuser, as with the Docker Compose and most managed Postgres). The role's password is derived from `OUTFLARE_SECRET_KEY`, and the role logs in with the same host and options as `DATABASE_URL`, so `pg_hba.conf` must allow password logins for it. Without that, Query answers `query_unavailable` and the log says why. Each server runs at most 3 queries at once (`OUTFLARE_QUERY_CONNECTIONS`, 0 turns Query off), each on its own connection, so allow for that many more connections per API server.

## Query Studio

The Query page (in CRM, next to People) is Query for the brand picked in the switcher: pick a brand first, since a query belongs to one. On the left are your saved queries (yours and those shared with the brand, with a search), the tables with their columns (from `GET .../query/tables`, searchable; a click inserts a name at the cursor), and the brand's catalog: each record type with its fields, which are its table's columns. The editor highlights Postgres SQL and completes table and column names; Cmd/Ctrl+Enter runs the query. Results come back as a table, with the row count, the time it took and a notice when there were more rows than shown (the page asks for up to 1,000), or as a chart: bars, lines or areas over one column along the bottom and up to eight number columns. Anyone who can run queries can download the results as CSV.

### Saved queries

Saving needs `customers.manage`; reading and running them `customers.read`, the owner's rule in `plans/brands/README.md`: anyone with `customers.read` runs queries over the brands they can see, limited by their scopes, and saving and sharing queries needs `customers.manage`.

- A saved query belongs to one brand and is **private** to the person who saved it (for an API token, the person who made the token) until they **share** it with the brand (`visibility: brand`). Then everyone who can query the brand sees and runs it, and anyone with `customers.manage` can change it. Only the person who saved it shares or unshares it. Another person's private query is `404`, as is another brand's.
- **Versions.** Every change to the SQL or the chart is a new version, with an optional note; renaming it or changing its description isn't. Every version is kept: open one, run it, or restore it, which makes a new version from the old one (`restored_from` says which), so the history is never rewritten.
- **Running** a saved query runs exactly what the ad hoc query runs, through the same role, limits and scopes: its SQL is checked against what you may read when it runs, not when it was saved, and the brand comes from the route, so a saved query never reads another brand whatever its SQL says.
- **Archiving** takes a query out of the list; it can't be changed or run, and its versions stay. You archive your own and shared ones; a caller with `workspace.manage` (owners and admins) archives anyone's.

| | |
| --- | --- |
| `GET /v1/brands/{brand}/queries` | Saved queries you can see (`q` searches name and description, `visibility=private` only yours, `brand` only shared ones), newest first, at most 500 |
| `POST /v1/brands/{brand}/queries` | Save one: `name`, `sql`, optional `description`, `chart` (`{"type": "bar"\|"line"\|"area", "x": "plan", "y": ["n"]}`), `visibility` and `note` |
| `GET`, `PATCH /v1/brands/{brand}/queries/{saved_query}` | Read one with its current SQL and chart; change it (new `sql` or `chart` is a new version) |
| `POST .../queries/{saved_query}/archive` | Archive it |
| `GET .../queries/{saved_query}/versions`, `.../versions/{version}` | Its versions, newest first (the newest 200), and one version |
| `POST .../queries/{saved_query}/versions/{version}/restore` | Make a new version from an old one |
| `POST .../queries/{saved_query}/run` | Run the current version, or `version`; `limit` as for Query. It only reads, so an agent kept to missions may run it outside one |

Agents use the MCP tools `list_saved_queries`, `get_saved_query`, `run_saved_query` and `save_query`.

### Published endpoints

Owners and admins can publish a saved query as an endpoint that another system calls with a brand API token holding only `query.endpoints`, with parameters bound as `$1`, `$2`, …, a row cap, a rate limit per token and every call logged. See [Published query endpoints](query-endpoints.md).

### CSV downloads

`POST /v1/brands/{brand}/query/csv` (with `sql`) and `POST .../queries/{saved_query}/csv` (optionally `version`) run the query and answer a CSV file (`Content-Disposition: attachment`): a header row of column names, then the rows, jsonb as JSON, times as RFC 3339 and null as an empty cell. A download holds up to 10,000 rows and 16 MB (`limit` asks for fewer), more than the 1,000 a JSON answer carries, since it is for a spreadsheet rather than a screen or an agent's context; `Outflare-Query-Rows` says how many it holds and `Outflare-Query-Truncated` whether there were more. A text cell that starts like a formula (`=`, `+`, `-`, `@`) gets a `'` in front, so a spreadsheet doesn't run it. A download needs `customers.read` and the read scopes of the tables it reads. It is personal data leaving the app in bulk, so every download is a `query.exported` entry in the activity log (with its row and column counts, never the SQL or the rows), and an agent kept to missions downloads only inside one.

## Events

Each query is an `api.called` event (`runBrandQuery`, or `runQuery` on the workspace route), and a successful one also records `query.ran` with the tables it read (type tables are counted in `type_tables`, not named), `brand_route`, the row count, whether it was truncated and how long it took. A saved query's run adds its `saved_query_id`, `version` and `current_version`. The SQL itself is never sent. Saved queries record `query.saved`, `query.version_created`, `query.updated` (name or description), `query.shared` (with `visibility`), `query.restored`, `query.archived` and `query.exported` in the activity log, each a product event of the same name with ids, versions and counts only, never the SQL or the name. A failure to set up the role is `query.setup_failed`, and a database that refuses the type tables (temporary views need the `TEMP` privilege, which Postgres gives every role by default) is `query.type_tables_failed`: the query still runs without them. Metrics: `outflare_query_runs_total{outcome}`, `outflare_query_duration_seconds`, `outflare_query_rows_total` and `outflare_query_type_tables_failed_total` (alert `OutflareQueryTypeTablesFailing`), plus `outflare_query_saved_runs_total{outcome}`, `outflare_query_exports_total{outcome}` and `outflare_query_export_rows_total` (alert `OutflareQueryExportsFailing`), on the Grafana dashboard's Query row. Saved queries live in `saved_queries` and `saved_query_versions` (migration `0345`).
