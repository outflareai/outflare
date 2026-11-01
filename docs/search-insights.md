# Search analysis and recovery tools

SEO → Search insights adds CTR benchmarks and estimated click upside,
complete-week/month content decay, saved content/topic rules, country/device
segments, weekday-seasonal forecasts with growth and revenue assumptions,
property-level data completeness, sustained trend breaks with Google update
and deploy context, GA4 landing-page outcomes and assistant referrals,
site-migration recovery with stored redirect evidence, and CrUX field history.

New routes use `/v1/brands/{brand}/seo-sites/{site}/`: `insights`,
`insights/refresh`, `search-groups`, and `search-migrations`. They require
`seo.read` or `seo.manage`; both source and destination sites of a migration
must belong to the brand. The SDK, CLI contract and MCP expose the same tools.

Search collection now reads at most 50,000 page/query rows per property/day.
Country/device rows and independent property totals are collected separately,
with an independent historical backfill for existing installations. Reaching
the cap is recorded; a missing row is not evidence of no search traffic.
Completeness is property-wide and combines privacy omissions, API omissions
and truncation: it is not a measurement of Google's sampling alone.

GA4 stores daily session landing pages and source/medium, configured key events,
and purchase revenue as integer millionths with its currency. Queries receive estimated outcomes
proportional to their visible Google clicks, using Google organic outcomes
only. Page and query rows must not be added together. Currencies are kept apart.
Known assistant referrers are matched on domain boundaries; visits without
referrers cannot be labelled AI traffic. GA4 key events may occur multiple
times in one session. These are events, not distinct converted sessions.

CrUX needs the workspace's own `google_crux` connection and API key, with the
Chrome UX Report and History APIs enabled. Collection is a queued job. Recurrence is explicit: people choose a daily or
weekly schedule, local hour and timezone in Automations. Pausing or editing
invalidates old queued jobs; provider failures do not stop future ticks. CrUX's overlapping
28-day windows are not daily measurements. Field regressions above good
thresholds join the existing fixes list and webhook workflow. The official
Google Search Status feed is public and can be queued without a CrUX connection;
Search Console syncs also queue its refresh daily.

Forecasts use the stored page/query click series (which can omit private queries),
require 56 consecutive observed days and use weekday seasonality
and a bounded trend; their approximate range is not a promise of growth.
Changing the growth, conversion-rate and order-value assumptions produces a
scenario. Sustained breaks require two consecutive settled weeks on either
side. Update/deploy overlap is timing evidence, not proof of causation.

Migrations compare mapped URLs, or whole sites when no mappings are supplied,
over equal settled windows of up to 28 days around go-live. A restructure
within one site requires mappings. Redirect validation reads the latest
completed crawls of both sites: unchecked URLs stay unknown. It never makes
arbitrary network requests to user-provided mappings. Recovery risk is a
heuristic; it is not a calibrated probability. Run crawls to refresh evidence.

Merchant Center, Yandex, Bing crawl/index diagnostics and IndexNow are excluded.

## Search workspace UX

Search insights is a standalone SEO navigation entry. Overview combines fresh
opportunities, declines and vital regressions with saved work and source health.
Explore groups reports by the task, offers settled-date presets, searchable and
sortable tables, column controls, phone cards, tag filters and bulk saving with
tags. Finding panels retain evidence and recommendations alongside status,
tags and notes. Explicit actions create a shared fix, assign it to the current
person, set a keyword target (feeding internal links), or draft a before/after
experiment. Implementation ownership and status continue in Fixes; work status
is separate and labelled as such.

Work persists the evidence verified on the server, not arbitrary browser data.
Reports store editable definitions and immutable results with exact periods;
failed generations are retained with their recoverable validation reason.
Snapshot bodies load only when opened. The latest run per report is independent
of the recent activity limit. Collection schedules expose local cadence, next
run, latest outcome, edit, pause/resume, run-now and delete. Activity exposes
attempt counts, retry timing, provider errors and explicit retries; stale paused
jobs show skipped. Search work and collection failures join existing daily or
weekly Digests without inventing another delivery system. Reports, work and
schedules are available through the API, generated SDKs and MCP, with writes
excluded from read-only MCP sessions.
