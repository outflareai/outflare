# Insights: funnels, trends, retention, paths and dashboards

Insights answer product analytics questions over everything Outflare knows about a brand's people: ad networks' numbers, website visits and page views, product events, conversions and payments. A **funnel** is how many people got through each step, where the rest dropped off and how long each step took: ad clicks from Google, then website visitors, then sign-ups, then a first purchase within 3 days of signing up. A **trend** is any number over time: visits, sign-ups, revenue, ad spend, or a formula between them such as cost per sign-up. **Retention** is who came back: of the people who first did something in a week, how many did it again (or paid) in each week after. A **dashboard** puts saved funnels, trends and retention on one page, all over the same dates.

A funnel counts **people**, not events. Only people who did step 1 in the date range are counted in step 2, only those in step 2 are counted in step 3, and so on, so each step shows its own drop-off. The web app's **Funnels** page (Analytics) builds and saves them; agents use the MCP tools `run_funnel`, `list_funnel_people` and `save_insight`.

## Where they show

Besides the Funnels, Trends and Dashboards pages, Outflare builds panels from the same engine on its own pages, from what the brand has (its first conversion definition, or a `signed_up` event, as the sign-up step): **Paid ads** shows the people funnel from an ad click to a first payment under the networks' clicks, spend beside visits and sign-ups over time, and the cost per sign-up; **Visitors** shows visits and people over time and the funnel from a visit to a first payment; **Home** shows the brand's visits, sign-ups, first payments and ad spend for the last 28 days against the 28 before. Each panel opens in Funnels or Trends with the same spec, to change and save.

## Who is a person

A step matches a customer record (a person), or a browser that isn't tied to one yet. A visit made before someone signed up counts for them once their browser is identified (the tracker's `identify` ties a browser's earlier visits to the person), so "visited from a Google ad, then signed up" works across that moment. A browser that never signed up stays its own anonymous person, and the people list links it to its visitor profile.

## Leaving out your own team

Your own team and your test accounts sign up, click around and "buy" all day, and that inflates every funnel. Open **Analytics**, then **Internal and test people**, list your team's email domains and the email addresses of single test accounts, and switch **Leave them out of reports** on. Funnels, trends, retention, stickiness, lifecycle and paths then count no event, visit or payment of a known person with such an address. The page shows how many people the list matches.

- Nothing is deleted, and nobody's record changes: switch it off, or take an entry out, and they are counted again from the next report. It is off until you turn it on.
- Only people Outflare knows by email are left out. A teammate who visits without signing in looks like any other visitor.
- [A/B tests](ab-tests.md) and the [Visitors](website-visitors.md) page have their own list (Visitors, settings, "Leave out these domains").
- In the API: `GET` and `PUT /v1/brands/{brand}/internal-people` (`enabled`, `domains`, `emails`; MCP `get_internal_people`, `set_internal_people`).

## Steps

A funnel has 2 to 20 steps, so a long journey across several systems (an ad visit, a download, an install, a sign-up, a first use) fits in one funnel. On the Funnels page a funnel of more than 8 steps opens as a **List**, one step a line with its whole name, and **Bars** switches back; the line over the chart names the biggest drop. With more than 6 steps the editor shows each step on one line, and a click opens it.

Each step has a `kind`:

| Kind | Matches | Filters on |
|---|---|---|
| `visit` | A website visit (a session), with its source and the ad click it carried | `network` (the ad network whose click id the visit carried: `google`, `meta`, `microsoft`, `linkedin`, `reddit`, `x`, `tiktok`), `channel`, `source`, `campaign`, `utm_*`, `landing_path`, `landing_url`, `referrer_host`, `device`, `country`, `region`, `city`, `isp`, `asn`, `browser`, `browser_version`, `os`, `os_version`, `device_brand`, `device_model`, `language`, `partner`, `ad_campaign`, `ad_set`, `ad` |
| `page` | A page view | `url`, `path`, `title`, `referrer`, `host`, `utm_*`, `source`, `param:<name>` (a query parameter, such as `param:ref`) |
| `event` | A product event by name (`event`, such as `signed_up`) | The same as pages, plus any property of the event (`plan` or `property:plan`) |
| `conversion` | One of the brand's [conversion definitions](conversions.md) (`conversion`, by id or key), with its own event and filters | As events |
| `payment` | A successful payment ([transactions](transactions.md)) | `amount` (in the brand's currency), `product`, `source`, `currency`, and where the payer first came from: `first_channel` (such as `seo`, `paid_ads`, `ai_answers`), `first_source` and `first_landing_path` of their first website visit, empty when no visit is on record |

Filters use `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `in`, `contains`, `not_contains`, `exists` and `not_exists`; text compares ignore case. All of a step's filters must match.

- `first: true` counts only the person's first ever one, such as a first payment: someone who paid before the funnel's dates never has a first payment in it.
- `within` is a step's time limit, such as `{"value": 3, "unit": "day", "from": 2}`: within 3 days of step 2. `from` defaults to the step before. Units are `minute`, `hour`, `day` and `week`, up to a year.
- The funnel's own `window` limits the whole funnel from step 1. Both can apply; the earlier deadline wins.
- A step counts only after the step before it, and each person counts once per step: their first step 1 in the range, then the first match of each later step after it.
- `optional: true` shows a step without making it a gate. Take ad visit, signed up, first payment: with a required sign-up, someone who already had an account and paid never counts as a payer, so the last bar is too low. With the sign-up optional, the sign-up bar still shows who signed up, everyone who paid after the ad visit counts at the last step, and the sign-up's result carries `skipped`: how many went on without it. The next step's rate, drop-off and time are measured from the last required step (time from the latest step the person did). Not for step 1, the last step or an any-order funnel, and a time limit or an exclusion can't count from an optional step. In the app, pick **Required** or **Optional** on the step. To drop people who did something instead, use an exclusion (below).
- In the app, drag a step by its handle or use its arrows to move it. A time limit that pointed at a step by number follows that step; one that would then point at a later step counts from the step before instead, and the page says so.

## Options

Every insight counts days, weeks and months in the brand's reporting time
zone (`timezone` on the brand, UTC unless set): a preset's "today" is the
brand's, a `from`/`to` date is the brand's day, and a period's `start` carries
the brand's offset (`2026-10-26T00:00:00+01:00`). `?timezone=` on a run reads
it in another zone. Saved insights, dashboard tiles, shared dashboards, alerts
on a number and the digest use the brand's zone too.

- `date_range`: when people did step 1. A `preset` (`today`, `yesterday`, `last_7_days` … `last_365_days`, `this_month`, `last_month`, `this_year`) moves with today, so a saved funnel stays current; or fixed `from` and `to` dates. Up to 366 days. Default: the last 30 days.
- `breakdown`: split every step by a property of step 1, such as `utm_campaign` or `network`. The 25 largest groups are shown and the rest are `$other`.
- **By a later step.** `step:<n>:<field>` splits by a field of step n instead of step 1, such as `step:2:property:plan` for the plan people picked at step 2. Each person's value is the one on the row that took them to that step (their first within its time limit); people who never reached it are grouped under no value, so the step before n is where their group ends. Not for an any-order funnel, which has no step 2. In the app, pick the step under **Break down by a field of**; for an event step, **A property of the event…** takes the property's name.
- **By a field of the person.** `person:<key>` splits by a field of each person's own record as it is now, such as `person:lifecycle_stage` or a field of your own like `person:plan`; a funnel by company uses the company's fields. It takes fields that hold a choice, a yes/no, a number or text (the catalog's `person_fields` and `company_fields`); names, email addresses, phone numbers, links to other records and fields marked confidential or restricted can't be used, because a breakdown shows its values to everyone who can read analytics. Visitors nobody has identified have no value. In the app, pick **The person** under **Break down by a field of**.
- **By A/B test version.** `experiment:<flag key>` (in a breakdown or a filter) is the version of that [A/B test](ab-tests.md) each person was first shown, from the website script's Experiment Viewed, or empty when they weren't in the test. Split a funnel by it to see where each version wins or loses people, or keep a trend to one version with a filter such as `{"property": "experiment:pricing-headline", "op": "eq", "value": "b"}`. In the app, pick the test under **Break down by → A/B test version**. It works for every step kind except ad spend.
- `interval`: also show each step's count by the `day`, `week` or `month` people entered.
- `ad_clicks`: show the ad networks' own clicks, impressions and spend for the same dates beside the steps, for one `network` and campaign name if given. Ad networks report clicks as daily totals, not people, so this is a reference, not a step: it never changes who is counted, and the people start at the visit that carried the network's click id. In the app it is the dashed bar labelled **Ad network clicks**, turned on and off with **Show ad clicks and spend** over the chart.
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

The answer has each step's `count`, `conversion_from_start` and `conversion_from_previous`, `dropped`, and `median_seconds` and `average_seconds` from the step before, with `time_to_convert` from step 2: how many people took under a minute, up to 5 and 30 minutes, 1, 6 and 24 hours, 3, 7 and 30 days, and longer (`from_seconds`, `to_seconds`, `count`; the last has no `to_seconds`). `breakdown` and `over_time` give the counts per group and per period. With an `interval`, the Funnels page draws the share who finished as a line over the periods people started in, and under it **Time to convert** shows the spread for any step. Needs `analytics.read`.

`POST /v1/brands/{brand}/insights/funnel/people` with `{"funnel": …, "step": 3}` lists the people who reached step 3 (with `"dropped": true`, the ones who reached step 2 and not step 3; with `"breakdown"`, one group's), each with their record or anonymous browser id and when they did each step. Needs `customers.read`.

`POST /v1/brands/{brand}/insights/funnel/people/save` with the same body plus a `name` puts those people into a new static [list](smart-lists.md), to mail, score or compare: `{"list_id", "added", "skipped_anonymous"}`. Only people with a record go in; browsers nobody has identified are counted as skipped. At most 10,000 people. A company funnel can't be saved, since lists hold people. Needs `customers.manage`, and is in the audit log as `insight.people_saved`. In the web app, the people sheet behind any step or drop-off has **Save as a list**.

`POST /v1/brands/{brand}/insights/funnel/correlation` with `{"funnel": …, "step": 3}` says what the people who reached step 3 did differently from everyone at step 1 who didn't: for each event and page (between their step 1 and that step, or step 1's deadline for those who dropped off) and each property of the identified people (their record's short text, number and yes/no attributes as `key: value`, never identifying ones such as email, phone or name), how many of each group did or had it, as counts and shares, and an odds ratio (`odds_ratio` over 1 goes with converting, under 1 with dropping off; smoothed, so a count of 0 doesn't make it infinite). `positive` lists what went with finishing, strongest first, `negative` what went with dropping off; `limit` (default 10) each way. Things fewer than 3 people did say nothing and would name individuals, so they are counted in `skipped` instead; the funnel's own step events are left out too. For funnels of people, not companies. Needs `analytics.read`. The Funnels page has it as **What did people who finished do differently?**, run on request.

### Can I trust the drop-off?

A funnel across several systems only works when each one reports under an id that leads back to the same person, and keeps reporting. When one doesn't, the chart shows a drop that never happened, and every step after it is too low as well: a tracking problem compounds. So each step is also looked at on its own.

`POST /v1/brands/{brand}/insights/funnel/checks` with `{"funnel": …}` (MCP `check_funnel_data`) answers `steps`, one per step:

- `people`: everyone who did the step in the dates, whether or not they did the steps before it. Set beside the step's `count` in the funnel, the difference is people who did the step and were lost earlier.
- `from_previous`: the two-step funnel of this step and the one before it, with nothing else in the way (`started` did the step before, `reached` then did this one in time). It is what you would get by building a funnel of only those two steps.
- `nothing_before`: how many of `people` have no visit, page view or event on record before this step. Up to 5,000 people a step are looked up and the number is scaled from them.
- `last_received_at` and `per_day`: when the step's data last arrived, and about how many arrived a day in the two weeks up to then.

and `data`, what looks wrong:

| Kind | Meaning |
|---|---|
| `never_received` | Nothing matching the step has arrived in the dates or the 90 days before them: a misspelt event, or a system that isn't sending |
| `stopped` | The step's data used to arrive and hasn't for at least 36 hours, in which at least 10 would have arrived at its usual rate, and the same stretch of the two weeks before wasn't quiet too. Only for dates that reach today |
| `unlinked` | At least 20 people, and 15% of the step's people, did it with nothing on record before it: the step is probably sent under a different id than the earlier ones |

The check for `unlinked` finds people who appear from nowhere at a step. It doesn't find an id that is wrong for a few people only. For funnels in order; needs `analytics.read`.

On the Funnels page, the line over the steps says which steps need a look, or that nothing was found. **Check this step** on a step also shows it **starting fresh**: as if the funnel began there, with everyone who did the step counted whatever they did before it, how many went on to the next step, and this funnel's rate for the same step beside it. **Open a funnel that starts at this step** opens the rest of the journey in a tab over the chart, beside **Whole funnel**, with the steps keeping their numbers, so you can switch between them; a problem higher up can't lower its numbers. From the tab, **Open it as a funnel of its own** makes it a new, unsaved funnel. Each step in the list has **Check this step** with these numbers, open already where something was found, and says on the row when many more people did the step than the funnel counts. A step with a problem offers **Make this step optional**, so the people who skip it still count at the next step and one broken step stops lowering every step after it. In the editor, an event step warns when nothing with that name has arrived in 90 days.

`GET /v1/brands/{brand}/insights/catalog` lists the event names seen in the last 90 days, the conversions, the fields and breakdowns each kind of step can use, and the A/B tests to split by (`experiments`).

A funnel runs read-only with a 30-second limit; one that runs out answers `query_timeout`, and a shorter date range or fewer steps help.

## Trends

A trend has up to six **series**, each a number per `day`, `week` or `month` (`interval`), with its total for the whole range. The web app's **Trends** page (Analytics) builds them; agents use `run_trend`.

| Series kind | Counts | Measures |
|---|---|---|
| `event` | A product event by name | `count` (default), `people` (distinct people), a `property`'s `sum`, `average`, `median`, `p90`, `p99`, `min` or `max`, and the measures per person and of active people below |
| `page` | Page views | `count`, `people`, per person, active people |
| `visit` | Website visits | `count`, `people`, per person, active people |
| `conversion` | One of the brand's conversion definitions | As events |
| `payment` | Successful payments | `count`, `people`, the amount's `sum` (revenue, in the brand's currency), `average`, `median`, `p90`, `p99`, `min` or `max`, per person, active people |
| `ad` | The ad networks' own daily numbers, for one `network` or all, and campaigns whose name contains `campaign` | `clicks` (default), `impressions`, `spend` (in the account's currency; `mixed` when accounts differ), `conversions` |
| `analytics` | The daily totals of the brand's search and analytics tools (Search Console, Bing Webmaster Tools, Google Analytics and the other analytics connectors), for one `connector` or all | `clicks` (default), `impressions`, `ctr` (clicks ÷ impressions), `position` (average, weighted by impressions), `visitors` (daily visitors added up), `pageviews`, `sessions`, `conversions` |
| `search` | Search Console's own detail for the brand's sites, with `filters` and a `breakdown` on `query`, `page`, `country` (Search Console's three-letter codes, such as `usa`) and `device` | `clicks` (default), `impressions`, `ctr`, `position` (weighted by impressions) |
| `backlinks` | The brand's referring domains, from its stored backlinks | `referring_domains` (default: domains with a live link at the end of each period; the total is at the end of the range), `dofollow_referring_domains`, `new_referring_domains` (first seen in the period), `lost_referring_domains` (last live link lost in the period) |
| `link_orders` | The brand's paid [link orders](link-orders.md) | `spend` (default: US dollars marked Paid, the ECB equivalent for an invoice in another currency), `committed` (US dollars approved), `published`, `paid` (orders) |

Measures beyond counting:

- **Spread of a property.** `median`, `p90` and `p99` are the value half, 90% or 99% of rows are under (page load time, order size); `min` and `max` the smallest and biggest.
- **Per person.** `per_person_average`, `per_person_median` and `per_person_max` count how many times each person did it in the period, then take the average, the middle or the most: how often a typical person uses a feature.
- **Active people.** `people` by day is daily active people. `weekly_active` and `monthly_active` count the distinct people in the 7 or 30 days up to the end of each period (rolling WAU and MAU), so a daily chart of `weekly_active` moves smoothly; their total is the distinct people over the whole range.

Totals are exact: a total of `people` counts each person once, even when they show up in several periods or groups, and an average or percentile is over every row, never an average of averages.

Series take the same `filters` and `first` as funnel steps. Options:

- `date_range` as for funnels. `breakdown` splits every series by a property (`utm_campaign`, `network`, `country`, a page's `path`, an event property; for ad series `network` or `campaign`); the 25 largest groups are shown and the rest are `$other`. `breakdown_2` splits each group again by a second property (country, then plan): each group then has `values` (both values) and a `value` that joins them with ` / `. Not for ad series.
- **By the person or their company.** In a trend's `breakdown`, `breakdown_2` or a series' filters, `person:<key>` is a field of the person's own record as it is now (`person:country`, or your own `person:plan`), and `company:<key>` is a field of their company (`company:industry`): the record itself when the row is about a company, otherwise the person's first company. It works for event, page view, conversion, visit and payment series, with the same fields as a funnel's breakdown (the catalog's `person_fields` and `company_fields`). Funnel step filters take them too. In the app they are under **Break down by → The person's details** and **Their company's details**, and in a filter's field list.
- **By product.** `product_key` and `product_category` are the [product](product-lifecycle.md) an event is about and that product's category, from the brand's products (payments have `product` and `product_category`). Split usage by `product_category` to see each family of products, or keep a series to one with a filter. With a breakdown, the web app's chart draws one line per group (of the formula, else the first series), six at a time: click a group under the chart to put it on or take it off.
- `formula` is a sum between series by their letters, such as `A / B` (ad spend ÷ sign-ups is cost per sign-up) or `(B + C) / A * 100`, with `+ - * /` and brackets. A division by zero is an empty point.
- `compare: true` also runs the period before, the same length, as `previous`.
- `display` is how the web app draws it: `line` (default), `area` (stacked areas), `bar`, `stacked_bar`, `horizontal_bar` (each series' or group's total as a bar across), `pie` (a ring of each one's share of the total), `map` (a world map shaded by each country's total; needs the breakdown `country`), `number` (totals only) or `table`. The engine checks these and otherwise ignores them, as it does the next five.
- `cumulative: true` draws a running total. `percent: true` draws each group as its share of the whole (with a breakdown). `smoothing` (3, 7, 14 or 28) draws a moving average over that many periods. `value_labels: true` writes the number on each point or bar. `right_axis` (`["B"]`) reads those series against a second axis on the right, for numbers of a different size such as spend beside sign-ups; at least one series stays on the left.
- `forecast: false` hides the forecast the web app draws after the last period of a line or bar chart (it shows by default). The engine ignores the field: the app works the forecast out in the browser from the numbers on the chart, for up to three series with six or more finished periods. It continues each line as a lighter dashed line for about a quarter of the dates shown (up to 14 days, 8 weeks or 6 months), inside a shaded band where the real number is about 80% likely to fall, following the trend (damped, so growth levels off) and, for daily numbers over three weeks or more, the weekly pattern. A period that hasn't ended yet is drawn dashed as "so far", with its expected total in the hover card.
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

A `search` series answers what the Search Console report does: clicks for one country, the top queries of a page, positions by device. Search Console keeps queries apart from countries and devices, so one series uses `query` (with `page`), or `country` and `device` (with `page`), never both; a breakdown returns the 25 biggest groups by clicks, and the series across every group still counts them all. A property read through two connections counts once.

`analytics`, `backlinks` and `link_orders` read data Outflare already stores, so a dashboard can show a brand's search and link-building numbers beside its sign-ups; they never call a provider. A property read through two connections of the same tool counts once (the latest sync). They take no filters; an `analytics` series can be broken down by `connector`, the others not at all. The catalog lists their measures (`source_metrics`) and the tools with numbers stored for the brand (`analytics_connectors`).

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

## Stickiness

A stickiness question takes one **step** (what counts as being active, without a time limit) and counts, for each person who did it in the dates, how many different `day`s, `week`s or `month`s (`interval`, default day) they did it in. It answers "do people come back often, or once?": a product people use daily has a long tail to the right, one they try and leave has almost everyone at 1. The web app's **Retention** page (Analytics) has it as the **Stickiness** view; agents use `run_stickiness`.

```http
POST /v1/brands/{brand}/insights/stickiness
{"step": {"kind": "event", "event": "opened_app"}, "date_range": {"preset": "last_30_days"}, "interval": "day"}
```

The answer has `people` (everyone who did the step in the dates), `average_periods` (how many periods the average person was active in, `null` with nobody) and one `buckets` entry per number of periods from 1 up, each with `periods`, `people` and `share` of everyone; past 31 the last bucket holds everyone with that many or more (`or_more`). Needs `analytics.read`, runs read-only with the same 30-second limit. Stickiness can be saved (`kind` `stickiness`) and put on dashboards like any insight.

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

## Ask a question

The box at the top of Dashboards turns a question in plain English ("How many people opened the app each week, by plan?") into a chart. The answer is an ordinary insight spec, checked like any other, so you can open it in the builder, change it and save it.

- **Your own AI model.** The question goes to the model the workspace chose for the "Analytics questions" task, on the workspace's own key. With no model connected the box says so and links to Connectors; nothing else answers it.
- **What the model sees.** The question, and the brand's event names with their dictionary names, descriptions and property keys, conversion names, and the fields a chart can break down by. Never people, property values or numbers: Outflare runs the chart itself.
- **When it can't.** A question the data can't answer comes back with a sentence saying why instead of a chart. An answer that isn't a valid spec is sent back to the model once; a second failure ends the question with an error.

```http
POST /v1/brands/{brand}/insights/ask        {"question": "…"}   → 202, the question (status queued)
GET  /v1/brands/{brand}/insights/ask/{id}                        → status queued, running, ready or failed
```

A finished question carries `kind`, `name`, `spec` and `explanation`; pass `kind` and `spec` to the run endpoints above or to `POST /insights` to save it. Questions are 500 characters at most and need `analytics.read`.

## Annotations

An annotation marks a day on every chart of a brand with a note: a launch, a campaign, an outage, a price change. `GET /v1/brands/{brand}/annotations?from=&to=` lists them (dates; the last year when left out), `POST` with `{"day": "2026-11-04", "text": "New pricing page live"}` adds one (up to 300 characters, `analytics.manage`) and `DELETE /v1/brands/{brand}/annotations/{annotation}` removes it. On the Trends page they show as markers on the chart, in the hover, and in **Notes on these dates** under it, where anyone with `analytics.manage` adds or removes them. Trend tiles on a brand's dashboards carry the same markers; a dashboard shared by link shows none. Adding and removing is in the audit log (`annotation.created`, `annotation.deleted`).

## Saved insights

In the app, what is on screen in Funnels, Trends, Retention (cohorts) and Paths is a draft. The chart follows every change, but the saved insight changes only on **Save changes**; **Cancel** goes back to the saved version, **Save as a copy** keeps both, and the bar over the builder says **Unsaved changes** until one of them is pressed. Leaving with unsaved changes asks first.

The Funnels, Trends, Retention and Paths pages each open on a table of what is saved (name, what it holds, when it last changed, and a search box once there are more than eight), with a button to start a new one; clicking a row opens that one in the builder, and **All funnels** in the builder's bar goes back. Each one has its own address, so a link, a reload and Back all return to it: `/{workspace}/{brand}/funnels` for the list, `/{workspace}/{brand}/funnels/{insight id}` for a saved funnel and `/{workspace}/{brand}/funnels/new` for a new one, with `?draft=` holding changes that are not saved yet. **Copy link** copies it. A link to an insight that was deleted says so and offers the way back.

`GET`, `POST /v1/brands/{brand}/insights` and `GET`, `PATCH`, `DELETE /v1/brands/{brand}/insights/{insight}` keep a brand's funnels, trends, retention, paths, lifecycle and stickiness questions (`kind`) with a name and description (`analytics.manage` to change them). A saved insight keeps only its spec, never results: every view runs it again. Saving, changing and deleting one is in the audit log (`insight.created`, `insight.updated`, `insight.deleted`).

## Dashboards

A dashboard is a brand's page of **tiles** on a 12-column grid: each a saved insight, run with the dashboard's `settings.date_range` in place of its own (or its own, when the dashboard sets none) and `settings.compare` for trends, a saved report (below), or a Markdown note. The web app's **Dashboards** page (Analytics) shows them. Anyone with `analytics.manage` picks the dates and **Compare with the period before** at the top, and clicks a tile's title or **Edit** to change it in the same builder as the Trends, Funnels, Retention, Paths, Lifecycle and Stickiness pages (events, filters, breakdown, chart type, name), full screen inside the dashboard, with the same bar as those pages: **Save changes** changes the saved insight everywhere it shows, **Cancel** puts it back, and **Save as a copy** saves a new insight and adds it to the dashboard beside the original. The menu on each tile has **Edit**, **Rename**, **Duplicate** (a copy on the same dashboard), **Open in Trends** (or Funnels, Retention, Paths: the insight's own address) and **Take off the dashboard**. A tile can **keep its own dates** (`own_dates` on the tile): it then runs over its insight's date range whatever the dashboard's is, on share links too, and shows those dates as a label; the builder's **Dates on this dashboard** choice and the tile's menu switch it. **+ Filter** next to the dates adds a condition on an event property or page field (`plan` is `pro`, `utm_source` is `google`; up to five) that every tile takes, as `settings.filters`: it goes on each event and page view series or step of trends, funnels, retention, lifecycle and stickiness, on share links and in the digest too; visits, payments, conversions, ad numbers and paths keep their own filters only. **Edit dashboard** shows the name, description, digest, share link and delete, lets you drag a tile by its bar to move it and by its corner to resize it (tiles in the way move down, and gaps close up), **Add tile** builds a new chart or note or picks a saved one, and arrows move and resize tiles; agents use `list_dashboards`, `get_dashboard`, `create_dashboard`, `update_dashboard`, `add_dashboard_tile` and `remove_dashboard_tile`.

`GET`, `POST /v1/brands/{brand}/dashboards`; `GET`, `PATCH`, `DELETE /v1/brands/{brand}/dashboards/{dashboard}` (`PATCH` takes `name`, `description`, `settings` (`date_range`, `compare`, `digest`, `filters`) and a `layout` of tile places); `POST .../tiles` with `insight_id`, `report_view_id` or `note`, `own_dates` for an insight tile that keeps its own dates, and an optional place (`x` 0 to 11, `y`, `w` 1 to 12, `h` 1 to 24; a new tile goes below everything), `PATCH` and `DELETE .../tiles/{tile}`. Reading needs `analytics.read`, changing `analytics.manage`. Deleting an insight leaves its tiles with no insight, so the dashboard says so instead of breaking. The audit log has `dashboard.created`, `dashboard.updated` (with the `fields` that changed, `layout` and `tiles` among them) and `dashboard.deleted`.

### A saved report as a tile

A report saved on Tracker → Report (a [saved view](analytics.md#custom-columns-and-saved-views) of the drill-down report: site visits or tracker clicks) goes on a dashboard too: **Add tile** lists them under **Saved reports**, or `POST .../tiles` with `report_view_id`. The tile shows the report's first level, as the report sorts it (up to 25 rows, tracker campaigns, traffic sources, offers and landers by name), with visits, conversions, revenue and, where the report has cost, cost, profit and ROI, and a total row. It keeps the dates saved with the report: the dashboard's dates, comparison and filters don't change it.

Everyone who opens the dashboard runs the report, so only a report shared with the team can be added (the app offers to share it), and while it is on a dashboard it can't be made private again (`409`). Deleting the report takes its tiles with it. On a share link the tile shows the report's name and nothing else: its numbers, cost and profit among them, are for the brand's signed-in team, so adding a report never puts them behind a link that is already out.

### Sharing a dashboard by link

A manager (`analytics.manage`) makes a dashboard's link with `POST /v1/brands/{brand}/dashboards/{dashboard}/share`, with an optional `password` (8 to 200 characters) and `expires_in_days` (up to 365). The answer carries the `token` and the `url` (`/share/<token>` on the app) once; a dashboard keeps one link, so making another replaces it and the old one dies. `DELETE .../share` takes it back. The dashboard's `share` field says whether a link exists, whether it has a password, when it expires and how often it was opened. Audited as `dashboard.shared` and `dashboard.share_revoked`.

Anyone with the link, signed in or not, opens the page: `POST /v1/shared/dashboards/{token}` with `{}` answers the dashboard's name, settings and tiles without their specs, or `401 password_required` for a link with a password until `{"password": …}` comes, which also gives a `key` to run the tiles with instead of the password. `POST /v1/shared/dashboards/{token}/tiles/{tile}` runs one tile with the dashboard's dates (or `date_range`, when the viewer picks other dates) and answers the tile's result by its kind. Wrong passwords are limited per link and network like sign-ins (`429`); unknown, expired and revoked links are `404`. Only the token is kept, hashed, and only the password's argon2id hash; the link opens the dashboard and nothing else of the brand. Opening is the event `dashboard_share.viewed`; a wrong password `dashboard_share.password_failed`.

### Dashboards in the digest

A dashboard with `settings.digest` on puts its trend tiles' numbers in the workspace's [digest](digests.md), in a "Dashboards" section: each series' total (the formula's, when the trend has one) over the digest's period, against the period before, labelled by the dashboard and the series (and the brand, when the workspace has several). At most five dashboards and twelve tiles each; a tile that can't run is left out rather than stopping the digest.

## Alerts on a number

An alert on a number watches a saved trend: every hour its number (the formula's total, else the first series' total, over the trend's own dates) is checked against a line, and an [alert](alerts.md) of kind `insight.threshold` is raised once when it crosses, so it reaches the app, the `alert.raised` webhook, email and Slack like every other alert. It is raised again only after the number came back over the line. The web app's **Trends** page has them under a saved trend ("Alert on this number"); agents use `create_insight_alert`, `list_insight_alerts` and `delete_insight_alert`.

`GET`, `POST /v1/brands/{brand}/insight-alerts` (`insight_id` of a saved trend, `condition` `above`, `below`, `rises_by` or `falls_by`, and a `threshold`: a value, or for the last two a percentage against the period before); `PATCH`, `DELETE /v1/brands/{brand}/insight-alerts/{alert}`. Each alert has a `state` (`unknown` before the first check or when the trend had no number, `ok`, `firing` while over the line), `last_value`, `last_checked_at` and `fired_at`. A changed condition or threshold starts over from `unknown`. Reading needs `analytics.read`, changing `analytics.manage`; the audit log has `insight_alert.created`, `insight_alert.updated` and `insight_alert.deleted`. The alert goes to whoever set it up (else the owners and admins), as the alerts settings say for the kind. Checks run as `insight_alerts.check` jobs, queued every five minutes for the rules that are due; a check that fails with a broken spec or the 30-second limit leaves the rule `unknown` until the next hour.
