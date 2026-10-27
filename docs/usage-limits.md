# Usage limits for paid connections

Some connections cost money every time Outflare calls them: SEO data (Ahrefs, DataForSEO), lead data (Apollo and the other data providers), email verifiers, and page reads and web searches (Geonode). Those calls run on the workspace's own account with each provider, with its own key, and the provider bills the workspace (AGENTS.md, "Bring your own keys"). Usage limits stop Outflare from making more of them once a workspace has used what it chose. AI models are not here: their spend and limits are under Settings → AI usage ([ai-usage.md](ai-usage.md)).

## What counts

Usage is summed from the spend ledger (`spend_entries`), by connection (`integration_id`), so every feature that books a call's cost there counts toward the same limits, whichever feature made it:

| Feature | Unit it books |
| --- | --- |
| SEO reports, weekly snapshots, links and competitors (`seosync`), rank checks (`ranks`) | US dollars for DataForSEO; API units for Ahrefs, estimated from its price list |
| Lead list builds, refreshes and enrichment (`lists`) | the provider's credits |
| Email verification (`verify`) | one credit per address checked by a paid verifier, estimated |
| Site crawl page reads through Geonode (`crawl`) | Geonode tokens |
| Extensions' web searches and page reads (`outflare.Connection`) | the provider's tokens, else US dollars |

A limit counts one unit: `USD` (the ledger's dollar amounts) or one of the provider's own (`credit`, `API unit`, `token`). Days and months are UTC.

## Limits

Each connection can have a **daily** and a **monthly** limit (`connection_limits`, migration 0205), and **stop calls at the limit** (on by default) or only warn. A limit left empty is no limit; 0 stops every call. Ahrefs (10,000 API units) and DataForSEO (US$10) start with a monthly limit so a new connection can't run away; that monthly limit is the same thing as the budget in their SEO settings. Other connections start with none.

In the app the limits are on the connection's page under Connectors (**Usage and limits**), with today's and this month's usage, the last 30 days and the warnings sent; the Connectors list shows a line of usage for each paid connection. Over the API: `GET /v1/workspaces/{ws}/connection-usage`, `GET` and `PUT .../integrations/{id}/usage/limits` (`DELETE` goes back to the starting limits); over MCP: `list_connection_usage`, `get_connection_usage` and `update_connection_limits`.

## How a limit stops a call

Before a paid call, the feature calls `usagelimits.Check` with the call's estimated cost (0 when it can't be told). With stopping on, a call that would pass a limit is not made:

- SEO reports answer `402 budget_reached`; snapshots, links and competitors stop for the period and record `budget_reached`.
- Lead list runs stop with the limit as their stop reason.
- Email verification checks as many addresses as the limit allows with the paid verifier and the rest with the free built-in checks, so sending is never held up.
- Crawls stop reading pages through Geonode for that step and keep the site's own answer.
- Extensions get an error whose `outflare.ErrorCode` is `usage_limit_reached`.

## Warnings

After a call is booked, `outcomes.Record` calls `usagelimits.Booked`, which checks whether today's or this month's usage reached 80% or all of a limit. The first time each day or month it records `connection_limit.warned` or `connection_limit.reached` (audit actions, product events and webhooks) and queues a `connection_limit.alert` email to each owner and admin with a confirmed address (`connection_limit_alerts` keeps it to once a period per limit and level). The email is Outflare's own account email (`sysmail`) and says the provider bills the workspace.

## Adding a paid connector

Book every paid call with `outcomes.Record`, with the connection's `IntegrationID`, its units and `UnitName` (or `AmountCents` for money), and call `usagelimits.Check` before the call. That's all: limits, warnings, the connection's page and the metrics follow. If the connector's capability is new, add its usual unit to `usageUnits` in `engine/internal/api/connection_usage.go`.

## Metrics

`outflare_connection_limits_alerts_total` (by `level` and `period`) and `outflare_connection_limits_stops_total` (by `connector` and `period`), on the "Paid connection limits" row of the dashboard. An info alert fires when a limit is reached, and a warning when over 500 calls an hour through one connector are refused at a limit for 30 minutes, which means a feature is retrying into the limit instead of stopping ([monitoring.md](monitoring.md)).
