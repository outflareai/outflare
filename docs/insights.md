# Insights: funnels, trends, retention, paths and dashboards

Insights answer product analytics questions over everything Outflare knows about a brand's people: ad networks' numbers, website visits and page views, product events, conversions and payments. A **funnel** is how many people got through each step, where the rest dropped off and how long each step took: ad clicks from Google, then website visitors, then sign-ups, then a first purchase within 3 days of signing up. A **trend** is any number over time: visits, sign-ups, revenue, ad spend, or a formula between them such as cost per sign-up. **Retention** is who came back: of the people who first did something in a week, how many did it again (or paid) in each week after. A **dashboard** puts saved funnels, trends and retention on one page, all over the same dates.

A funnel counts **people**, not events. Only people who did step 1 in the date range are counted in step 2, only those in step 2 are counted in step 3, and so on, so each step shows its own drop-off. The web app's **Funnels** page (Analytics) builds and saves them; agents use the MCP tools `run_funnel`, `list_funnel_people` and `save_insight`.

## Where they show

Besides the Funnels, Trends and Dashboards pages, Outflare builds panels from the same engine on its own pages, from what the brand has (its first conversion definition, or a `signed_up` event, as the sign-up step): **Paid ads** shows the people funnel from an ad click to a first payment under the networks' clicks, spend beside visits and sign-ups over time, and the cost per sign-up; **Visitors** shows visits and people over time and the funnel from a visit to a first payment; **Home** shows the brand's visits, sign-ups, first payments and ad spend for the last 28 days against the 28 before. Each panel opens in Funnels or Trends with the same spec, to change and save.

## Who is a person

A step matches a customer record (a person), or a browser that isn't tied to one yet. A visit made before someone signed up counts for them once their browser is identified (the tracker's `identify` ties a browser's earlier visits to the person), so "visited from a Google ad, then signed up" works across that moment. A browser that never signed up stays its own anonymous person, and the people list links it to its visitor profile.

## Steps

Each step has a `kind`:

| Kind | Matches | Filters on |
|---|---|---|
| `visit` | A website visit (a session), with its source and the ad click it carried | `network` (the ad network whose click id the visit carried: `google`, `meta`, `microsoft`, `linkedin`, `reddit`, `x`, `tiktok`), `channel`, `source`, `campaign`, `utm_*`, `landing_path`, `landing_url`, `referrer_host`, `device`, `country`, `region`, `city`, `isp`, `asn`, `browser`, `browser_version`, `os`, `os_version`, `device_brand`, `device_model`, `language`, `partner`, `ad_campaign`, `ad_set`, `ad` |
| `page` | A page view | `url`, `path`, `title`, `referrer`, `host`, `utm_*`, `source`, `param:<name>` (a query parameter, such as `param:ref`) |
| `event` | A product event by name (`event`, such as `signed_up`) | The same as pages, plus any property of the event (`plan` or `property:plan`) |
| `conversion` | One of the brand's [conversion definitions](conversions.md) (`conversion`, by id or key), with its own event and filters | As events |
| `payment` | A successful payment ([transactions](transactions.md)) | `amount` (in the brand's currency), `product`, `source`, `currency` |

Filters use `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `in`, `contains`, `not_contains`, `exists` and `not_exists`; text compares ignore case. All of a step's filters must match.

- `first: true` counts only the person's first ever one, such as a first payment: someone who paid before the funnel's dates never has a first payment in it.
- `within` is a step's time limit, such as `{"value": 3, "unit": "day", "from": 2}`: within 3 days of step 2. `from` defaults to the step before. Units are `minute`, `hour`, `day` and `week`, up to a year.
- The funnel's own `window` limits the whole funnel from step 1. Both can apply; the earlier deadline wins.
- A step counts only after the step before it, and each person counts once per step: their first step 1 in the range, then the first match of each later step after it.

## Options

Every insight counts days, weeks and months in the brand's reporting time
zone (`timezone` on the brand, UTC unless set): a preset's "today" is the
brand's, a `from`/`to` date is the brand's day, and a period's `start` carries
the brand's offset (`2026-10-26T00:00:00+01:00`). `?timezone=` on a run reads
it in another zone. Saved insights, dashboard tiles, shared dashboards, alerts
on a number and the digest use the brand's zone too.

- `date_range`: when people did step 1. A `preset` (`today`, `yesterday`, `last_7_days` … `last_365_days`, `this_month`, `last_month`, `this_year`) moves with today, so a saved funnel stays current; or fixed `from` and `to` dates. Up to 366 days. Default: the last 30 days.
- `breakdown`: split every step by a property of step 1, such as `utm_campaign` or `network`. The 25 largest groups are shown and the rest are `$other`.
- **By A/B test version.** `experiment:<flag key>` (in a breakdown or a filter) is the version of that [A/B test](ab-tests.md) each person was first shown, from the website script's Experiment Viewed, or empty when they weren't in the test. Split a funnel by it to see where each version wins or loses people, or keep a trend to one version with a filter such as `{"property": "experiment:pricing-headline", "op": "eq", "value": "b"}`. In the app, pick the test under **Break down by → A/B test version**. It works for every step kind except ad spend.
- `interval`: also show each step's count by the `day`, `week` or `month` people entered.
- `ad_clicks`: show the ad networks' own clicks, impressions and spend for the same dates in front of step 1, for one `network` and campaign name if given. Ad networks report clicks as daily totals, not people, so this bar sits outside the people count; the people start at the visit that carried the network's click id.
- `exclusions`: drop from the whole funnel everyone who did something between two of its steps, such as `{"kind": "event", "event": "support_contacted", "from": 1, "to": 2}` for people who contacted support between signing up and paying. An exclusion is described like a step (kind, event or conversion, filters). Someone who never reached step `to` is dropped when they did it before step 1's deadline. Up to 5. Every step's count is then of the same, smaller group.
- `order`: `strict` (the default) or `any`. In any order, people do the steps in whichever order they like: step 1 is whichever step a person did first in the dates, and step N means N of the steps done within the window, so the chart counts how many of the steps people got through (the steps are labelled "1 step", "2 steps" and so on, and the last bar is everyone who did all of them). An any-order funnel takes no exclusions and no per-step `within`, and a `breakdown` needs every step to be of the same kind, since any step can be the first.
- `by`: `person` (the default) or `company`. A company funnel counts each company once, through the people linked to it (the `companies` relationship): a company reaches a step when any of its people does, and its people's visits, events and payments all count for it. Browsers nobody has identified belong to no company, so they drop out. The people behind a step are then companies.

## The API

```http
POST /v1/brands/{brand}/insights/funnel
{
  "date_range": {"preset": "last_30_days"},
  "steps": [
    {"kind": "visit", "filters": [{"property": "network", "op": "eq", "value": "google"}]},
    {"kind": "event", "event": "signed_up", "within": {"value": 7, "unit": "day"}},
    {"kind": "payment", "first": true, "within": {"value": 3, "unit": "day", "from": 2}}
  ],
  "breakdown": "utm_campaign",
  "ad_clicks": {"network": "google"}
}
```

The answer has each step's `count`, `conversion_from_start` and `conversion_from_previous`, `dropped`, and `median_seconds` and `average_seconds` from the step before; `breakdown` and `over_time` give the same per group and per period. Needs `analytics.read`.

`POST /v1/brands/{brand}/insights/funnel/people` with `{"funnel": …, "step": 3}` lists the people who reached step 3 (with `"dropped": true`, the ones who reached step 2 and not step 3; with `"breakdown"`, one group's), each with their record or anonymous browser id and when they did each step. Needs `customers.read`.

`POST /v1/brands/{brand}/insights/funnel/people/save` with the same body plus a `name` puts those people into a new static [list](smart-lists.md), to mail, score or compare: `{"list_id", "added", "skipped_anonymous"}`. Only people with a record go in; browsers nobody has identified are counted as skipped. At most 10,000 people. A company funnel can't be saved, since lists hold people. Needs `customers.manage`, and is in the audit log as `insight.people_saved`. In the web app, the people sheet behind any step or drop-off has **Save as a list**.

`POST /v1/brands/{brand}/insights/funnel/correlation` with `{"funnel": …, "step": 3}` says what the people who reached step 3 did differently from everyone at step 1 who didn't: for each event and page (between their step 1 and that step, or step 1's deadline for those who dropped off) and each property of the identified people (their record's short text, number and yes/no attributes as `key: value`, never identifying ones such as email, phone or name), how many of each group did or had it, as counts and shares, and an odds ratio (`odds_ratio` over 1 goes with converting, under 1 with dropping off; smoothed, so a count of 0 doesn't make it infinite). `positive` lists what went with finishing, strongest first, `negative` what went with dropping off; `limit` (default 10) each way. Things fewer than 3 people did say nothing and would name individuals, so they are counted in `skipped` instead; the funnel's own step events are left out too. For funnels of people, not companies. Needs `analytics.read`. The Funnels page has it as **What did people who finished do differently?**, run on request.

`GET /v1/brands/{brand}/insights/catalog` lists the event names seen in the last 90 days, the conversions, the fields and breakdowns each kind of step can use, and the A/B tests to split by (`experiments`).

A funnel runs read-only with a 30-second limit; one that runs out answers `query_timeout`, and a shorter date range or fewer steps help.

## Trends

A trend has up to six **series**, each a number per `day`, `week` or `month` (`interval`), with its total for the whole range. The web app's **Trends** page (Analytics) builds them; agents use `run_trend`.

| Series kind | Counts | Measures |
|---|---|---|
| `event` | A product event by name | `count` (default), `people` (distinct people), `sum` or `average` of a `property` |
| `page` | Page views | `count`, `people` |
| `visit` | Website visits | `count`, `people` |
| `conversion` | One of the brand's conversion definitions | As events |
| `payment` | Successful payments | `count`, `people`, `sum` (revenue, in the brand's currency) or `average` |
| `ad` | The ad networks' own daily numbers, for one `network` or all, and campaigns whose name contains `campaign` | `clicks` (default), `impressions`, `spend` (in the account's currency; `mixed` when accounts differ), `conversions` |

Series take the same `filters` and `first` as funnel steps. Options:

- `date_range` as for funnels. `breakdown` splits every series by a property (`utm_campaign`, `network`, `country`, a page's `path`, an event property; for ad series `network` or `campaign`); the 10 largest groups are shown.
- `formula` is a sum between series by their letters, such as `A / B` (ad spend ÷ sign-ups is cost per sign-up) or `(B + C) / A * 100`, with `+ - * /` and brackets. A division by zero is an empty point.
- `compare: true` also runs the period before, the same length, as `previous`.
- `display` is how the team sees it: `line` (default), `bar`, `number` (the totals, as a tile) or `table`.

```http
POST /v1/brands/{brand}/insights/trend
{
  "date_range": {"preset": "last_90_days"}, "interval": "week", "compare": true,
  "series": [
    {"kind": "ad", "measure": "spend", "network": "google", "label": "Google spend"},
    {"kind": "event", "event": "signed_up", "first": true, "label": "Sign-ups"}
  ],
  "formula": "A / B"
}
```

The answer has each series' `label`, `total`, `currency` and `points` (`start`, `value`), the `formula` series, `breakdown` groups with the same per group, and `previous` with the same for the period before. Needs `analytics.read`, runs read-only with the same 30-second limit.

## Retention

A retention question has a **start** (what puts a person in a cohort: the day, week or month of their first one in the dates) and a **return** (what counts as coming back; the start again when left out), both funnel steps without a time limit. By `interval` (`day`, `week` or `month`, default week) for `periods` after the start (default 14 days, 8 weeks or 6 months, up to 24). The web app's **Retention** page (Analytics) builds them; agents use `run_retention`.

```http
POST /v1/brands/{brand}/insights/retention
{
  "start": {"kind": "event", "event": "signed_up"},
  "return": {"kind": "visit"},
  "date_range": {"preset": "last_90_days"}, "interval": "week", "periods": 8
}
```

The answer has one `cohorts` entry per period with its `size` and `counts` (how many came back in period 0, the start period itself, then 1, 2 …; `null` for a period that hasn't begun), and `average`, the share who came back in each period over the cohorts whose period had begun. Each person counts once per period. Needs `analytics.read`, runs read-only with the same 30-second limit.

## Lifecycle

A lifecycle question takes one **step** (what counts as being active, without a time limit) and shows, in each `day`, `week` or `month` (default week) of the dates, how many of the people who did it were doing it for the first time ever (**new**), had also done it the period before (**returning**), or came back after a gap (**resurrected**), and how many who did it the period before did not this time (**dormant**). New, returning and resurrected add up to everyone active in the period; dormant sits below the line as the people lost. Whether someone is new looks at their whole history, not only the dates. The web app's **Retention** page (Analytics) has it as the **Lifecycle** view; agents use `run_lifecycle`.

```http
POST /v1/brands/{brand}/insights/lifecycle
{"step": {"kind": "event", "event": "opened_app"}, "date_range": {"preset": "last_90_days"}, "interval": "week"}
```

The answer has one `periods` entry per period, in order, each with `start`, `new`, `returning`, `resurrected` and `dormant` (0 for a period that hasn't begun). Needs `analytics.read`, runs read-only with the same 30-second limit.

## Paths

A paths question shows where people went step by step: the pages they viewed and the events they caused, in order, as a graph. Paths run **from a start** (`start`: every path begins at that page or event), **towards an end** (`end`: every path ends there, and the steps before it are shown), or, with neither, from each person's first page or event in the dates. `steps` is how many steps to show, start or end included (2 to 6, default 4); `kinds` says what counts as a step (`page`, `event` or both); `window` is how long a whole path may take from its first step (default a day); past the `top` pages or events of a step (default 8, up to 20) the rest fold into `other`; `exclude` lists pages or events to ignore as if they never happened (a cookie banner's event, a redirect page). The web app's **Paths** page (Analytics) builds them; agents use `run_paths`.

```http
POST /v1/brands/{brand}/insights/paths
{
  "date_range": {"preset": "last_30_days"},
  "start": {"kind": "page", "name": "/pricing"},
  "steps": 4, "kinds": ["page"], "window": {"value": 1, "unit": "hour"}
}
```

The answer has `people` (how many people's paths were counted), `nodes` (per `step`: a `page`, `event` or `other` with its `name`, `count`, and `drop_off`, the people who went nowhere within the window) and `links` (how many people went from a node to each node at the next step). Towards an end, the end is the last step and shorter paths start later. Each person counts once per step. Needs `analytics.read`, runs read-only with the same 30-second limit. Paths can be saved (`kind` `paths`) and put on dashboards like any insight.

## Annotations

An annotation marks a day on every chart of a brand with a note: a launch, a campaign, an outage, a price change. `GET /v1/brands/{brand}/annotations?from=&to=` lists them (dates; the last year when left out), `POST` with `{"day": "2026-11-04", "text": "New pricing page live"}` adds one (up to 300 characters, `analytics.manage`) and `DELETE /v1/brands/{brand}/annotations/{annotation}` removes it. On the Trends page they show as markers on the chart, in the hover, and in **Notes on these dates** under it, where anyone with `analytics.manage` adds or removes them. Trend tiles on a brand's dashboards carry the same markers; a dashboard shared by link shows none. Adding and removing is in the audit log (`annotation.created`, `annotation.deleted`).

## Saved insights

`GET`, `POST /v1/brands/{brand}/insights` and `GET`, `PATCH`, `DELETE /v1/brands/{brand}/insights/{insight}` keep a brand's funnels, trends, retention, paths and lifecycle questions (`kind`) with a name and description (`analytics.manage` to change them). A saved insight keeps only its spec, never results: every view runs it again. Saving, changing and deleting one is in the audit log (`insight.created`, `insight.updated`, `insight.deleted`).

## Dashboards

A dashboard is a brand's page of **tiles** on a 12-column grid: each a saved insight, run with the dashboard's `settings.date_range` in place of its own (or its own, when the dashboard sets none) and `settings.compare` for trends, or a Markdown note. The web app's **Dashboards** page (Analytics) shows them, with arrows to move and resize tiles; agents use `list_dashboards`, `get_dashboard`, `create_dashboard`, `update_dashboard`, `add_dashboard_tile` and `remove_dashboard_tile`.

`GET`, `POST /v1/brands/{brand}/dashboards`; `GET`, `PATCH`, `DELETE /v1/brands/{brand}/dashboards/{dashboard}` (`PATCH` takes `name`, `description`, `settings` (`date_range`, `compare`, `digest`) and a `layout` of tile places); `POST .../tiles` with `insight_id` or `note` and an optional place (`x` 0 to 11, `y`, `w` 1 to 12, `h` 1 to 24; a new tile goes below everything), `PATCH` and `DELETE .../tiles/{tile}`. Reading needs `analytics.read`, changing `analytics.manage`. Deleting an insight leaves its tiles with no insight, so the dashboard says so instead of breaking. The audit log has `dashboard.created`, `dashboard.updated` (with the `fields` that changed, `layout` and `tiles` among them) and `dashboard.deleted`.

### Sharing a dashboard by link

A manager (`analytics.manage`) makes a dashboard's link with `POST /v1/brands/{brand}/dashboards/{dashboard}/share`, with an optional `password` (8 to 200 characters) and `expires_in_days` (up to 365). The answer carries the `token` and the `url` (`/share/<token>` on the app) once; a dashboard keeps one link, so making another replaces it and the old one dies. `DELETE .../share` takes it back. The dashboard's `share` field says whether a link exists, whether it has a password, when it expires and how often it was opened. Audited as `dashboard.shared` and `dashboard.share_revoked`.

Anyone with the link, signed in or not, opens the page: `POST /v1/shared/dashboards/{token}` with `{}` answers the dashboard's name, settings and tiles without their specs, or `401 password_required` for a link with a password until `{"password": …}` comes, which also gives a `key` to run the tiles with instead of the password. `POST /v1/shared/dashboards/{token}/tiles/{tile}` runs one tile with the dashboard's dates (or `date_range`, when the viewer picks other dates) and answers the tile's result by its kind. Wrong passwords are limited per link and network like sign-ins (`429`); unknown, expired and revoked links are `404`. Only the token is kept, hashed, and only the password's argon2id hash; the link opens the dashboard and nothing else of the brand. Opening is the event `dashboard_share.viewed`; a wrong password `dashboard_share.password_failed`.

### Dashboards in the digest

A dashboard with `settings.digest` on puts its trend tiles' numbers in the workspace's [digest](digests.md), in a "Dashboards" section: each series' total (the formula's, when the trend has one) over the digest's period, against the period before, labelled by the dashboard and the series (and the brand, when the workspace has several). At most five dashboards and twelve tiles each; a tile that can't run is left out rather than stopping the digest.

## Alerts on a number

An alert on a number watches a saved trend: every hour its number (the formula's total, else the first series' total, over the trend's own dates) is checked against a line, and an [alert](alerts.md) of kind `insight.threshold` is raised once when it crosses, so it reaches the app, the `alert.raised` webhook, email and Slack like every other alert. It is raised again only after the number came back over the line. The web app's **Trends** page has them under a saved trend ("Alert on this number"); agents use `create_insight_alert`, `list_insight_alerts` and `delete_insight_alert`.

`GET`, `POST /v1/brands/{brand}/insight-alerts` (`insight_id` of a saved trend, `condition` `above`, `below`, `rises_by` or `falls_by`, and a `threshold`: a value, or for the last two a percentage against the period before); `PATCH`, `DELETE /v1/brands/{brand}/insight-alerts/{alert}`. Each alert has a `state` (`unknown` before the first check or when the trend had no number, `ok`, `firing` while over the line), `last_value`, `last_checked_at` and `fired_at`. A changed condition or threshold starts over from `unknown`. Reading needs `analytics.read`, changing `analytics.manage`; the audit log has `insight_alert.created`, `insight_alert.updated` and `insight_alert.deleted`. The alert goes to whoever set it up (else the owners and admins), as the alerts settings say for the kind. Checks run as `insight_alerts.check` jobs, queued every five minutes for the rules that are due; a check that fails with a broken spec or the 30-second limit leaves the rule `unknown` until the next hour.
