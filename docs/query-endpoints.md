# Published query endpoints

A published query endpoint lets another system, such as a BI tool, a spreadsheet script or a warehouse job, pull the rows of one saved [Query](query.md) with an API token. It answers the same query every time, with the parameters the caller sends.

- **Only owners and admins publish one**, signed in.
- **Only API tokens call one.** The token holds `query.endpoints` and is made for that brand only. A signed-in session gets 403 `token_required`, and a token for every brand gets 403 `brand_token_required`. There are no secret links.
- **It reads what its publisher could.** The read scopes of the owner or admin who published (or last changed) it are fixed on the endpoint (`scopes`), so the caller's token needs nothing but `query.endpoints`. It never reads another brand.
- **Every call is logged**, allowed or refused: the token, the parameters, the outcome, rows and time. The log is kept 90 days.
- **Each call has a row cap and each token a rate limit**, set per endpoint.

## Publishing

Save the query in Data → Queries first. Use `$1`, `$2`, … where the caller's values go:

```sql
SELECT name, email, created_at FROM people
WHERE country = $1 AND created_at >= $2
ORDER BY created_at DESC
```

Then publish it (owners and admins):

```http
POST /v1/brands/{brand}/query-endpoints
{
  "saved_query_id": "…",
  "slug": "new-signups",
  "name": "New sign-ups by country",
  "params": [
    { "name": "country", "type": "text", "required": true },
    { "name": "since", "type": "date", "default": "2026-01-01" }
  ],
  "row_cap": 1000,
  "rate_per_minute": 60
}
```

- `params` are bound in order as `$1`, `$2`, …, as values: they are never written into the SQL. Types are `text`, `integer`, `number`, `boolean`, `date`, `timestamp` and `uuid`. Publishing fails when the SQL uses a `$n` with no parameter declared for it.
- The endpoint runs the saved query's current version (or `version`) and stays on it: editing the saved query changes nothing for callers until you move the endpoint (`PATCH … {"version": 3}`).
- `row_cap` is 1 to 10,000 (default 1,000); `rate_per_minute` is 1 to 600 calls a minute per token (default 60).
- `description` says what it returns, in plain English. Leave it out and Outflare writes one from the query: the table it reads, its filters, the parameters callers send, its order and its row limit, such as "People where country is the country you send and created is on or after the since you send, newest created first. Returns name, email and created, up to 1,000 rows a call. Callers send country (text) and since (date, optional, 2026-01-01 when left out)." It is written by rules, not by an AI model, so it costs nothing and needs no key. `POST /v1/brands/{brand}/query-endpoints/describe` with `saved_query_id` (and optionally `version`, `params` and `row_cap`) writes one without publishing (MCP: `describe_query_endpoint`). In the app the publish form fills the Description field with it and keeps it current as you name the parameters, until you type in it; Write it again puts the written one back, on the form and on the endpoint's page.
- `PATCH /v1/brands/{brand}/query-endpoints/{endpoint}` changes the version, name, description, parameters, limits, or turns it off (`"status": "disabled"`) and on. Its saved query and slug never change. A change also fixes what it reads to the changer's read scopes.
- `DELETE /v1/brands/{brand}/query-endpoints/{endpoint}` deletes it for good (owners and admins): calls to its address answer 404, the address can be used again, and its call log is deleted with it. Its saved query is kept, and the audit log keeps `query_endpoint.deleted` with what it was. To stop calls for a while, turn it off instead.

## Calling

Make an API token for the brand with only `query.endpoints` (Settings → API tokens), then:

```sh
curl -X POST https://app.outflare.ai/v1/brands/{brand}/query-endpoints/new-signups/run \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"params": {"country": "DE"}}'
```

The answer is the rows, as `POST …/query` returns them (`columns`, `rows`, `row_count`, `truncated` when there were more than the row cap), with `endpoint_id` and the `version` that ran. Over the rate limit it answers 429 `rate_limited` with `Retry-After`; a turned-off endpoint answers 404; one whose saved query was archived answers 409 `endpoint_broken`.

## Watching it

- `GET /v1/brands/{brand}/query-endpoints` lists the brand's endpoints with their calls in the last 24 hours and the last call.
- `GET …/query-endpoints/{endpoint}/calls` is the call log, newest first.
- The audit log has `query_endpoint.published`, `query_endpoint.updated`, `query_endpoint.disabled`, `query_endpoint.enabled` and `query_endpoint.deleted`.
- Operators see calls, errors and time on the Grafana row "Published query endpoints" ([monitoring](monitoring.md)).

MCP has `list_query_endpoints` and `run_query_endpoint` (with a token that holds `query.endpoints`).
