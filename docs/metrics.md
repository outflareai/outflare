# Metrics

A metric is a number Outflare keeps for each company and person and keeps up to date, such as their spend in the last 30 days or how many times they used your product this week. Rules, journeys, smart lists and Query read metrics like any field, so you can act on totals, not only single events: "a company spent over $1,000 this month", or "usage this week fell below half of last week".

Metrics are part of the [customer data platform](customers.md) and belong to one brand. Everything here works the same from the REST API (`/v1/brands/{brand}/metrics`), the MCP server and Query. In the app, People → Metrics turns metrics on, adds your own, changes what one counts, refills one from stored events and archives one (people with `catalog.manage`; everyone else can read them).

## Live, always up to date

Each metric is a running total over a rolling window of UTC days. When an event arrives, Outflare adds it to the total straight away, and a rule watching the metric can fire the moment it crosses a line. At each new UTC day, days that left the window drop out, and anything watching the metric hears of the change.

## The metrics each brand starts with

Turning metrics on (`PATCH /v1/brands/{brand}/metrics` with `{"enabled": true}`, scope `catalog.manage`, MCP `update_metrics`) adds these, which you can change or archive:

| Key | What it is | Kept on |
| --- | --- | --- |
| `spend_30d` | Sum of `payment.succeeded` amounts (`amount_minor`, so cents) in the last 30 days | Companies and people |
| `payments_90d` | How many payments in the last 90 days | Companies and people |
| `refunds_90d` | Sum of `payment.refunded` amounts in the last 90 days | Companies and people |
| `usage_7d` | Events your product sent in the last 7 days | Companies and people |
| `usage_prev_7d` | The same, for the 7 days before that | Companies and people |
| `active_days_30d` | Days in the last 30 with any usage | People |
| `last_seen` | When your product last sent an event about them | Companies and people |

**Usage** is any event your product sends through the event API (`track`, `page` and `screen` calls). Billing syncs, Outflare's own events and website snippet events never count.

A company counts its own events and those of its people (the people whose `companies` include it).

## Per product

Each metric also keeps a value per [product](product-lifecycle.md) unless you switch `per_product` off. A company that pays for two products has `spend_30d` overall and `spend_30d` for each.

## Where the values are

Each record carries its metrics in its attributes under `metrics`:

```json
{"metrics": {"spend_30d": 124000, "usage_7d": 18400, "last_seen": "2026-11-02T08:14:00Z",
  "by_product": {"residential": {"spend_30d": 94000, "usage_7d": 12000}}}}
```

So a [rule](rules.md) of kind `change` with `{"attribute": "metrics.spend_30d", "op": "gt", "value": 100000}` fires when a company crosses $1,000, a [smart list](lists.md#smart-lists) can hold the companies above it, and a journey can branch on it. Product keys appear with any character other than lowercase letters, digits and underscores written as `_`. `GET /v1/brands/{brand}/records/{record}/metrics` (MCP `get_record_metrics`) lists a record's values, and Query has `metrics` and `metric_values`.

## Filling from history, without firing anything

When metrics are turned on, a metric is added, or what a metric counts changes (a new `version`), Outflare fills it from the events it already stored, in the background. The numbers are right straight away instead of after a whole window.

A fill never sets anything off. No rule, journey, email or webhook reacts to it. The values it writes are where rules start from, so a company that was already over $1,000 before the fill counts as having crossed $1,000 already. It doesn't get a "you reached $1,000" email when its next payment lands. Only a record that goes from below the line to above it after the fill sets a rule off, as does one that drops back below and crosses again.

`POST /v1/brands/{brand}/metrics/{metric}/backfill` refills one metric on demand, also quietly. Events that arrive while a fill runs are counted live after it.

## Making your own

`POST /v1/brands/{brand}/metrics` (MCP `create_metric`):

| Field | Meaning |
| --- | --- |
| `key` | Its name under `attributes.metrics`, such as `api_calls_30d`. It never changes, so rules and lists keep working. |
| `aggregate` | `count` events, `sum`, `min` or `max` of a number `property`, `days` with an event, or `last` (when the latest happened). |
| `events`, `usage` | The event names it counts, and whether usage counts too. |
| `property` | For `sum`, `min` and `max`: the number property, such as `amount_minor`. |
| `window_days`, `offset_days` | The last `window_days` UTC days, today included, ending `offset_days` ago. |
| `levels` | `company`, `person` or both. |
| `per_product` | Also keep it per product (default true). |

Changing what a metric counts makes a new version and refills it. `POST .../metrics/{metric}/archive` takes it off every record. Reading needs `customers.read`; changing needs `catalog.manage`.

To compare two periods, keep two metrics and compare them in a rule: `usage_7d` against `usage_prev_7d`. A ratio such as average order value is `spend_30d` divided by `payments_90d`-style totals of the same window. Counting distinct things (how many different people were active) isn't a live metric.

## Monitoring

`outflare_record_metrics_updates_total` counts events added to metrics by outcome, `outflare_record_metrics_backfill_seconds` times fills, `outflare_record_metrics_sweep_seconds` times the daily window moves, and `outflare_record_metrics_due`, `outflare_record_metrics_oldest_due_seconds` and `outflare_record_metrics_backfilling` show how far behind they are ([monitoring](monitoring.md)).
