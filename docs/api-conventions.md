# API conventions

How the Outflare API, SDK, MCP server, CLI and webhooks are designed, so each new endpoint, tool, report or paid feature fits what is already there. `AGENTS.md` has the step-by-step rules for a change; this explains the design behind them.

Where today's API does not follow a convention yet, the **Now** line says so. New work follows the convention; old endpoints move over in additive steps, never by breaking a client.

## One contract, four surfaces

`api/openapi.yaml` is the single source of truth. Everything else is derived from it:

| Surface | How it follows the contract |
| --- | --- |
| REST API | The contract test (`engine/internal/api/contract_test.go`) validates every response the test suite receives against the document, and checks each route's `x-outflare-access` against the route's real wrapper. |
| TypeScript SDK | Types are generated (`pnpm --filter @outflare/sdk generate`); CI fails if they are stale. |
| CLI | Reads the document built into the binary at run time, so every operation is a command with no code per endpoint. |
| MCP | Hand-written tools, because agents need fewer, task-shaped tools with good descriptions, not one per endpoint. Each tool calls the SDK, so the types catch a wrong path or body. |
| Webhooks | Event types are listed in `webhooks.Catalog`, the `WebhookEvent` schema and the document's `webhooks:` section; a test checks all three match. |

So adding an endpoint is: write it in the document, implement it, test it. The SDK and CLI get it for free; add an MCP tool only when an agent needs it.

## Versioning

- The version is in the path: `/v1`. Within v1, changes are additive only: new endpoints, new optional request fields, new response fields, new enum values, new event types.
- Clients must ignore unknown response fields and treat enums as open (the SDK types say which values exist today; a later server may send more).
- Never rename or remove a field, endpoint, scope or event type in v1, never make an optional request field required, and never change what a field means.
- To retire something, keep it working, mark it `deprecated: true` in the document, and send `Deprecation` and `Sunset` headers (RFC 9745, RFC 8594) with a link to the replacement. Remove it only in a new major version.
- If a breaking change is ever unavoidable, prefer a date-pinned `Outflare-Version` request header (as Stripe does) over `/v2`, so clients upgrade one change at a time.

## Resources and names

- Paths are plural nouns in kebab case under the workspace: `/v1/workspaces/{ws}/sending-domains/{domain}`. Ids are UUIDs, except natural keys such as a domain name.
- Fields are `snake_case`. Times are RFC 3339 in UTC, named `*_at`; dates are `YYYY-MM-DD`. Booleans read as statements (`enabled`, `may_take_mailboxes`).
- `operationId` is `verbNoun` in camel case (`listLeads`, `rotateWebhookSecret`). It becomes the SDK name, the CLI command (`list-leads`) and usually the MCP tool (`list_leads`), so choose it for people.
- CRUD uses `GET`, `POST`, `PATCH` (send only what changes) and `DELETE` (answers `204`). An action that is not CRUD is `POST /resource/{id}/verb`: `/test`, `/rotate-secret`, `/approve`.
- A list answers `{"data": [...]}`, never a bare array, so it can grow pagination or totals later.
- Nullable fields are `type: [T, "null"]` (OpenAPI 3.1), always present in the answer, so clients can tell "none" from "not sent".
- Money is an integer in the currency's minor units (`amount_cents`), or millionths for unit prices (`unit_price_micros`), always with a `currency`. Never floats.

## One name for each thing

Each concept has one name across the API, events, MCP and docs, so a new module reuses it instead of coining another:

| Concept | Name | Not |
| --- | --- | --- |
| A person or company Outflare knows | record (`person`, `account` shown as "company") | contact, attendee, recipient |
| What someone did in Outflare | activity (`audit_events`, the `/events` feed, webhooks) | event alone |
| What a customer did in their product | customer event | event alone |
| Permission to market | consent | subscription |
| Never email this address or domain | suppression | unsubscribe list |
| A set of people | audience (a static list or a segment) | list, segment, broadcast list |
| Something to fix or decide | fix | blocker, issue |
| A connected account | integration (a "connection" on screen) | provider, platform, supplier, oauth connection |

Rows about a person or company carry `record_id` (AGENTS.md, Rules). **Now:** leads and outcomes keep their own columns beside it until they move onto record attributes.

## Who may call it

Every operation declares `x-outflare-access`: `scope` (the token scope it needs in the workspace), `brand_limited` (whether a caller kept to some brands may call it), `signed_in` (people only), or `authenticated` (any caller). Operations without it are public. The contract test checks each value against the server.

- A new area gets `area.read` and `area.manage` scopes. Any scope in an area includes `area.read` (`auth.Grants`), so check the narrowest scope a route needs and never require both. Split out a scope only when the risk differs, as `suppressions.manage` and `infrastructure.approve` do (one can email people who opted out, the other spends money).
- Roles are bundles of scopes. The starting roles' defaults are in `engine/internal/auth`: viewers get every `.read` unless it exposes secrets, operators (Members) get the marketing work, admins get everything but `members.remove`. A workspace can change them and add its own ([roles.md](roles.md)), so check scopes (`access.has`), never a role name. A new scope goes into the admin default automatically; add it to the operator or viewer default when they should have it, and to `web/src/lib/rolePermissions.ts` so the role editor shows it in plain words.
- `GET /v1/whoami` tells any token what it is and what it reaches, so clients need only a URL and a token.

## Pagination and filtering

**Convention:** a list that can grow past a few hundred items takes `limit` and `cursor`, and answers `{"data": [...], "next_cursor": "..." | null}`. Call again with `cursor=next_cursor` until it is null. Cursors are opaque base64url strings; clients never build or read them. Filters are query parameters named after the field (`status=failed`, `event_type=lead.replied`); time ranges are `from` and `to`.

**How it works:** underneath, a list pages by `before` (an id or time, newest first), `after` (oldest first) or `offset`, and keeps that parameter and its `next_before`/`next_after`/`next_offset` field for older clients. `writeJSON` adds `next_cursor` beside whichever `next_` field a list answers, and a middleware turns `?cursor=` back into that parameter, so a handler only ever reads its own. A new list therefore needs no cursor code: answer one `next_` field (null on the last page) and declare `#/components/parameters/Cursor` and `next_cursor: { $ref: "#/components/schemas/NextCursor" }` in the spec. A cursor this API did not give out answers 400 `invalid_cursor`. Both SDKs (`paginate`), the CLI's `--all` and the MCP tools all page by cursor, and both SDKs retry 429 and 502 to 504 with one `Idempotency-Key` per write. Small lists (mailboxes, sequences, brands) are not paged.

**Field selection:** any GET takes `fields=id,email,status` and answers only those fields of each item in `data` (paging fields stay), or of the object itself; `id` always comes back. A name no item has answers 400 `invalid_request` with `param: fields` and the names that exist, which is reliable because every field is always present. A middleware (`withFields`) does it after the handler, so a new endpoint needs nothing. Paged lists declare `#/components/parameters/Fields` so the SDKs, the CLI (`--fields`) and the MCP list tools offer it.

## Errors

Every error answers `{"error": {"code", "message", "request_id"}}`, the operation's `default` response.

- `code` is stable and machine-readable; `message` is for people and may change. Clients branch on `code` and status, never on `message`.
- `400 invalid_request` for a bad request, `401`, `403 forbidden` (missing scope), `404 not_found` (including things the caller cannot see), `409` for conflicts and retries in progress, `422` only for a reused idempotency key, `429 rate_limited`, `5xx`.
- The message says how to fix it ("events must list 1 to 100 event types; use [\"*\"] for every event"), and starts with the field's name when it is about one, so `param` fills in by itself.
- Every code is in the catalog in `engine/internal/api/errorcodes.go`, served at `GET /v1/error-codes` with its statuses, meaning and whether a retry can succeed. A test fails if a handler answers a code the catalog does not list. A field error is always `invalid_request` with `param`, never a new `invalid_<field>` code; add a code only for a case a client would handle differently. Codes are only added, never renamed.

- A `400` names the field at fault in `param` when the message is about one (`"url must be ..."`, an unknown JSON field, a wrong type), so an agent can fix its own request. The SDK exposes it as `OutflareError.param`; MCP tool errors include it.

**Next:** a documented catalog of every `code` (about 70 today), generated from the server, on the website.

## Writes, retries and background work

- Every workspace write takes `Idempotency-Key`. A repeat within 24 hours replays the first answer (`Idempotent-Replayed: true`). The CLI sets one on every write and retries safely; SDK users pass one.
- Work that runs later answers `202` with a JSON body, `{"queued": true}` at least. Inside a mission, any workspace write may instead answer `202` with `MissionHeld`; the SDK types this only for mission clients, and `isHeld()` tells them apart.
- Work that runs as one background job answers `202` with `{"queued": true, "job": {"id", "url"}}` (the `QueuedJob` schema) and a `Location` header pointing at `GET /v1/workspaces/{ws}/jobs/{id}`. Build the reference with `jobRef`. Callers poll that one endpoint until `status` is `succeeded` or `failed`, whatever the feature, and `GET .../jobs?status=failed` shows what went wrong anywhere in the workspace. Finished jobs are pruned after 14 days.
- **Next:** list builds and imports keep their own run records (they carry progress and results); when they move onto jobs, add `progress` and `result` to `Job` rather than a new status shape.

## Events and webhooks

- An event is an audit action, recorded with `record(...)`/`audit.Record` in the transaction that made the change. So an event is never sent for a change that rolled back, or lost for one that committed.
- To deliver an action as a webhook, add it to `webhooks.Catalog` and to the document. A listed event is a promise: add freely, never rename or remove, and only add fields to its `data`.
- Event names are `noun.past_tense_verb` (`lead.replied`, `mailbox.auto_paused`). Categories group them for `lead.*` style subscriptions.
- The envelope is fixed: `id`, `type`, `created_at`, `workspace_id`, `actor`, `target`, `request_id`, `data`. `target` points at the record; receivers fetch it for current state rather than trusting a copy.
- Delivery follows Standard Webhooks: signed, retried with backoff, logged per attempt, redeliverable, and turned off after `410 Gone` or five days of failures.
- `GET .../events?after=` serves the same events as a feed, oldest first, for agents and scripts that cannot receive webhooks. **Next:** a hook so extensions receive events in-process (below).

## Reports

Reports share one shape, so dashboards, agents and exports read all of them the same way. `GET .../reports` and `GET .../outcomes/report` follow it:

```json
{
  "from": "2026-10-01T00:00:00Z",
  "to": "2026-11-01T00:00:00Z",
  "group_by": "sequence",
  "totals": { "sent": 1200, "replied_leads": 31, "reply_rate": 0.026 },
  "rows": [{ "key": "6f1c…", "name": "Q4 agencies", "sent": 600, "replied_leads": 19, "reply_rate": 0.032 }]
}
```

- `from` and `to` take a date (which includes the whole day) or an RFC 3339 time, default to the last 30 days, and come back as the exact times used, `to` exclusive.
- `group_by` picks the rows. Each row has `key` (the id, day or bucket, stable to join on) and `name` (what to show), then the same metrics as `totals`.
- Metrics are counts and minor units (`_cents`), never pre-formatted strings. A rate is a fraction from 0 to 1, or null when there is nothing to divide by, and the counts it comes from are in the answer too, so clients can re-aggregate.
- Caveats a reader needs are plain-language strings in `notes`, not footnotes in the docs.
- An expensive report from a paid provider says so (`cost` in the answer) and sits behind a budget, as the SEO reports do.
- Reports that also export take the same parameters on `.../export?dataset=report`, as CSV or NDJSON.

**Now:** the analytics endpoints (`analytics/summary`, `/history`, `/compare`) answer `from` and `to` as dates and their own `data`; they move to this shape when next changed. `GET .../stats` is a live overview, not a report, and has no period. `label` on report rows is the old name for `name` and goes away once clients have moved.

## Test workspaces

A workspace created with `"test": true` is the sandbox. Code that talks to the outside world checks for it in one place and does nothing real: the mailbox dialer (`mailbox.Simulated`, host `simulated.outflare.invalid`), domain checks (`mailbox.SimulatedDomain`), verification (`verify.Simulated`) and purchases (`409 test_workspace`). Anything new that sends, buys or calls a vendor on a workspace's behalf must check `access.Test` (or `mailbox.IsTestWorkspace` in a job) and do the same. Endpoints that only make sense in the sandbox live under `/v1/workspaces/{ws}/test/` and answer `409 live_workspace` elsewhere. Answers from a test workspace carry `Outflare-Test-Workspace: true`.

## Rate limits

Each API token may make `API_RATE_LIMIT` requests a minute (600 by default). Every answer to a token carries the IETF `RateLimit-Policy` and `RateLimit` headers; over the limit it is `429 rate_limited` with `Retry-After`, and the CLI backs off on it. Sessions (the web app) are not limited this way; sign-in and account emails have their own limits. Counts are per server process. **Next:** shared counts across processes, and per-workspace limits for the Cloud's plans.

## Security

- Secrets are stored sealed (`secrets.Box`, keyed to the row) or hashed (API tokens), and shown once or behind a `*.manage` scope (`GET .../webhooks/{id}/secret`). They never go in audit data, event payloads, logs or error messages.
- Every answer carries `X-Content-Type-Options: nosniff`, `Referrer-Policy`, `frame-ancestors 'none'`, and HSTS once `PUBLIC_URL` is https. `/v1` answers are `Cache-Control: no-store` unless the handler opts in to caching (public logos).
- Anything Outflare calls on a user's behalf goes through the SSRF guard (`webhooks.CheckURL` plus the dial-time check): public https only, no redirects, no credentials in URLs.
- Inbound webhooks are verified by signature before anything is read, and deduplicated by event id; ids are kept 30 days.
- Field values reach SQL only as parameters; cursors and other client strings are validated like any other input.

## Extending the engine (Cloud, billing and plugins)

The engine stays free and complete (AGPL). Paid features and billing live in builds on top of it, such as the hosted Cloud, through `engine/outflare.Extension` and its `Host`: extra routes, middleware, jobs and capabilities.

- **Routes** an extension adds go under `/v1/...`, follow these conventions and are described in the extension's own OpenAPI document, so its SDK, CLI and docs can be generated the same way.
- **Metering and billing:** the engine never decides prices. It records what happened (sends, list builds, provider calls, spend in the outcomes ledger, events in the audit log). An extension meters from those records, and enforces limits with middleware that answers `402` with code `quota_exceeded` and a message saying what to do. **Next:** `Host.OnEvent`, so an extension receives the same events as webhooks in-process, in the same transaction, without polling tables.
- **Capabilities** in `GET /v1/meta` tell clients what a server can do, so the same SDK, CLI and MCP server work against a plain self-hosted engine and the Cloud.

## MCP tools

- Names are `verb_noun` in snake case and match the operation where one exists (`create_webhook`). Descriptions say when to use the tool and what to do next ("Then call test_webhook").
- Every tool carries annotations: `readOnlyHint` for reads (only these load in read-only mode), `destructiveHint` for deletes and ends, `openWorldHint` for anything that emails people, spends money or calls outside services. Clients use them to ask before acting.
- Answers are compact JSON, also sent as `structuredContent`; errors carry the status, `code`, message and request id. **Next:** an `outputSchema` per tool from the document's response schema.

## Adding something: checklist

1. Write the operation in `api/openapi.yaml`: `operationId`, `summary`, `x-outflare-access`, request and response schemas, `default` error, `Idempotency-Key` and `202 MissionHeld` for a workspace write.
2. Implement it with the right wrapper (`s.inWorkspace(scope, ...)`), recording the change with `record(...)` in its transaction.
3. Test it. The contract test then checks the answers and access for free, and `TestDocumentFollowsConventions` checks the operation against this guide (a `default` error, snake_case parameters, and a list with `limit` that pages by cursor and takes `fields`).
4. Run `pnpm --filter @outflare/sdk generate`. The CLI needs nothing.
5. Add an MCP tool if agents need it, an event to the catalog if receivers need it, and a scope if it is a new area.
6. Update the README, and open the matching docs PR in `outflareai/website`.
