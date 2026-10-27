# Search intelligence

The SEO module's search intelligence answers what Google sees on each page
of a site and what to do about it. It works from what Outflare already
stores: the site's crawls ([docs/crawler.md](crawler.md)) and the
workspace's own Google Search Console sign-in
([docs/analytics.md](analytics.md)). It reads nothing that the workspace has
not connected, and nothing here calls Google when you open a page: the sync
stores the data, and the API and the **Keywords** page read it back.

## Search Console page by page

Each Search Console connection's analytics sync (every six hours) also stores,
for every property it syncs:

- **Clicks and impressions by page and query, day by day** (`search_daily`):
  the click-through rate and average position too, up to 5,000 page and query
  rows a day. The first sync stores the last 90 days; each later sync reads the
  last four days again (Search Console keeps revising them) and goes 14 days
  further back, until it reaches 16 months, which is all Search Console keeps.
  Older rows are deleted.
- **How Google indexed each page** (`url_inspections`), from the URL
  Inspection API: indexed or not, Google's reason ("Crawled - currently not
  indexed"), the canonical Google chose against the page's own, robots.txt
  and fetch state, the last crawl and the sitemaps that list it. Each sync
  inspects up to 100 pages a property: the pages OutflareBot crawled on the
  property's SEO sites and the pages Google showed in the last 28 days, never
  inspected first, then the most shown. A page is inspected again after 7 days,
  so a property stays inside Google's limit of about 2,000 inspections a day.
  When a verdict changes, the one before is kept with when it changed.
- **Sitemaps** (`search_sitemaps`), once a day: when each was submitted and
  last read, its errors and warnings, and how many of its URLs are indexed.

An SEO site's data comes from the property that covers its host: a domain
property (`sc-domain:example.com`) first, then the closest URL-prefix
property. Only pages on the site's own host (and its `www.` twin) count, so
`docs.example.com` is not mixed into `www.example.com`. Switching a property
off on the Analytics page deletes what was stored for it.

```sh
# Pages by clicks over the 28 days Search Console last sent
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/search"
# Queries, or the pages Google shows for one query
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/search?group=query&sort=impressions"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/search?group=page_query&keyword=proxy%20pricing"
# One page day by day
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/search/history?page=https%3A%2F%2Fwww.example.com%2Fpricing"
# Pages Google has not indexed, and the sitemaps
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/indexing?state=not_indexed"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/sitemaps"
```

Every answer has `source`, the property the numbers come from, or `null` when
no Search Console property covers the site; connect Google Search Console
then. Reading needs `seo.read`; brand-limited members and tokens see only
their brands' sites. The Search Console sign-in is read-only
(`webmasters.readonly`), which covers URL Inspection and sitemaps.

The MCP tools are `get_site_search`, `get_site_search_history`,
`get_site_indexing` and `get_site_sitemaps`.

## A keyword target for each page

Each page of an SEO site can have a **target keyword**: the search it should
rank for, plus up to ten secondary keywords. Set one by hand, or accept the
one Search Console suggests: the query that brought the page the most clicks
over the last 28 days (shown at least 10 times). Pages with no target get a
suggestion; dismiss a page to stop them.

Every target is checked against the site's latest finished crawl: is every
word of the keyword (in any order, plurals counting) in the page's title, its
H1 and its meta description? What is missing goes on the workspace's fixes
list with source `seo_keywords`, one fix per rule and page:

| Rule | Severity |
| --- | --- |
| `target_not_in_title` | warning |
| `target_not_in_h1` | notice |
| `target_not_in_description` | notice |

The check runs when a target is set, changed or removed, and again at the end
of every crawl, in the same transaction as the crawl's own audit. A crawl that
still finds a fix someone marked done opens it again; one that no longer does
confirms it. `POST .../fixes/{fix}/verify` works for these fixes too, and the
coding-agent prompt (`GET .../fixes/{fix}/prompt`) names the page and its
keyword. Removing a target marks its fixes done. Each target also shows what
its keyword brought the page in Search Console over 28 days: clicks,
impressions and average position.

```sh
# Targets, then suggestions
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/targets"
# Set one, or accept every suggestion
curl -X PUT -H "Authorization: Bearer $TOKEN" -d '{"url": "https://www.example.com/pricing", "keyword": "proxy pricing"}' \
  "$URL/v1/workspaces/$WS/seo-sites/$SITE/targets"
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/targets/accept"
```

Changing targets needs `seo.manage`; each change is an audit event
(`page_target.set`, `page_target.dismissed`, `page_target.updated`,
`page_target.removed`, `page_target.accepted`). The MCP tools are
`list_page_targets`, `set_page_target`, `accept_page_targets`,
`update_page_target` and `delete_page_target`. Targets work without Search
Console; suggestions and the numbers need it.

## Internal links worth adding

For each page with a target keyword, Outflare looks in the site's latest crawl
for up to five other pages about the same keyword that don't link to it yet.
A page is about the keyword when Google showed it for that search in the last
28 days of Search Console, or its title or H1 has every word of it. Only pages
that answered 200, are HTML and may be indexed count, and the most linked to
come first, since a link from a page with links of its own passes on more.

Each target page with suggestions is a fix (rule `internal_link_opportunity`,
notice, one per target page). The next crawl that finds the links confirms
it; the prompt lists the pages to link from. Target pages the latest crawl
did not read are left as they are, and listed in `not_crawled`.

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/internal-links"
```

The MCP tool is `get_site_internal_links`.

## Cannibalisation

Cannibalisation is when Google shows several pages of one site for the same
search and splits its clicks, so none of them ranks as well as one strong page
would. Outflare looks for it in the 28 days to the newest day Search Console
sent. A search counts when it was shown at least 100 times and two or more
pages each had at least a tenth of its impressions (10 or more) at an average
position of 30 or better. Searches that name the site's brand are left out,
because a brand's home page and its other pages showing for its name is how it
should be.

Each competing search names the page to keep: the page whose target keyword
it is, otherwise the page with the most clicks. The 50 most shown are fixes
(rule `keyword_cannibalised`, warning, one per search), checked again after
every Search Console sync: a sync that still finds one marked done opens it
again, and one that no longer does confirms it. Pages that share one target
keyword are a fix too (rule `target_shared`, warning, one per keyword),
checked whenever a target changes. The fix prompt lists the pages with their
share of impressions and the page to keep.

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/cannibalisation"
```

The MCP tool is `get_site_cannibalisation`. When the check after a sync
fails, the sync still counts as done, and the product event
`search_insights.failed` (with `error_code` `internal`) and an error log say
so.

## Rank tracking

Search Console reports an average position over every search a page was
shown for. Rank tracking answers a narrower question: where does the site
rank on Google today for the keywords you care about. Track up to 100
keywords at a time, or every page's target keyword, for one country and
device (desktop or mobile). Each is checked weekly, or daily if you choose.

Checks read Google's top 100 organic results through the workspace's own
DataForSEO connection (see [SEO tools](seo.md)), never an account of
Outflare's. With no DataForSEO connection, rank tracking stays off and the
Keywords page (Rankings) says to connect one. A check costs about $0.02. It
counts towards the connection's monthly budget, stops when the budget is
spent (the keyword's `last_error` says so) and is recorded on Outcomes.

Each tracked keyword shows the site's latest position (null when it is not in
the top 100), the page that ranks, the position at the check before, the
best so far, and the page whose target it is. When another page ranks than
the target, that is worth a look. The history keeps one check a day with the
top 10 results, so you can see who outranks you.

```sh
# Track keywords, and every target keyword, on mobile
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"keywords": ["proxy pricing"], "from_targets": true, "device": "mobile"}' \
  "$URL/v1/workspaces/$WS/seo-sites/$SITE/rankings"
# Where each one ranks, and one keyword's history
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/rankings"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/rankings/$TRACKED/history"
# Check now instead of waiting for the schedule
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/rankings/check"
```

`PATCH .../rankings/{tracked}` pauses, resumes or changes how often a keyword
is checked, and `DELETE` stops tracking it along with its history. Changes
are audit events (`tracked_keyword.added`, `tracked_keyword.updated`,
`tracked_keyword.removed`, `rank.check_requested`). The MCP tools are
`list_rankings`, `track_keywords`, `check_rankings`, `get_rank_history`,
`update_tracked_keyword` and `delete_tracked_keyword`.

## Conversions by landing page

Search Console says which pages people find; it doesn't say which pages
bring people who sign up or buy. With the workspace's own PostHog connected,
the Keywords page (Conversions) counts, for each page of the site, the visits
that started on it and how many went on to a conversion event, overall and
for visits from a search engine's results. Search Console's clicks and
position for the same page, and its target keyword, sit beside them, so you
can see pages that rank but don't convert, and pages that convert and deserve
more search traffic.

A visit is a PostHog session with a page view; it counts under the page of
its first page view, on the day it started, and as a search visit when that
page view's referrer is a search engine (Google, Bing, DuckDuckGo, Yahoo,
Yandex, Baidu, Ecosia, Naver or Brave Search). It is a conversion when the
session has one of the events chosen as conversions. Choose them on the page,
or with `conversion_events` on `PATCH .../analytics/{id}/sites/{site}` (up to
20 event names); until then the page shows visits only. Each sync makes one
query on PostHog: the last 90 days the first time and whenever the events
change, then the last three days again. Rows come from the project with the
most visits to the site's host (and its `www.` twin), so a test project
does not add to production's.

```sh
# Count sign-ups and purchases as conversions on PostHog project 1
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"conversion_events": ["signed_up", "purchase"]}' "$URL/v1/workspaces/$WS/analytics/$POSTHOG/sites/1"
# Pages by conversions from search over the last 28 days stored
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/conversions?sort=search_conversions"
```

The MCP tool is `get_site_conversions`; `update_analytics_site` sets the
events.

## Regressions

After every Search Console sync, rank check, conversions sync and crawl,
Outflare compares a site's latest numbers with the ones before and lists
what got worse on the Keywords page (Drops). It only reads what the syncs
stored, so it costs nothing.

| Kind | When it counts |
| --- | --- |
| `search_clicks_dropped` | A page's Search Console clicks in the latest 7 days are 40% or more under its weekly average over the 28 days before, and that average was at least 20. Needs five weeks stored. |
| `rank_dropped` | A tracked keyword that ranked in the top 20 fell 5 or more places, or out of the top 100, at its latest check. |
| `page_deindexed` | URL Inspection had the page indexed (`PASS`) at the inspection before, and not at the latest. |
| `conversions_dropped` | A landing page's conversions in the latest 7 days are half or less of its weekly average over the 28 days before, and that average was at least 5. Needs conversion events and five weeks stored. |
| `health_dropped` | The latest crawl's health score is 10 or more points under the crawl before. |

Each open regression is a fix on the fixes list (source `seo_keywords`,
rule = kind), so the fix webhooks announce it (`fix.found`) and its
recovery (`fix.confirmed`), and the fix prompt says what changed. A check
that no longer finds it resolves it and confirms the fix; one that finds it
again after it was marked done opens it again. Kinds a check cannot judge
(no Search Console, no conversion events, a first crawl) are left as they
are. Resolved regressions stay listed with `status=resolved`.

```sh
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/regressions"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-sites/$SITE/regressions?status=resolved&kind=rank_dropped"
```

The MCP tool is `get_site_regressions`.

## Events

Each property's page by page sync is the product event
`search_console.synced`, with `integration_id`, `days`, `rows`, `inspected`,
`sitemaps` and `backfilled`, and `fixes_new`, `fixes_reopened` and
`fixes_confirmed` when the sync stored new days (never URLs or queries). When Google refuses or
fails, `outcome` is `failure` or `error` with `error_code`
`connector_auth`, `connector_quota`, `connector_invalid` or
`connector_unavailable`, and the error is logged.

Each site's rank checks are the product event `rank.checked`, with
`site_id`, `checked`, `ranked`, `failed` and `cost_usd` (never keywords or
URLs). When a check could not run, `outcome` is `failure` with `error_code`
`budget_reached`, `connector_needed`, `connector_auth`, `connector_quota` or
`connector_invalid`, or `error` with `connector_unavailable`, and the error is
logged. A failure of the hourly loop that queues checks is
`rank.schedule_failed`.

Each PostHog project's conversions sync is `page_conversions.synced`, with
`integration_id`, `days`, `rows`, `events` (how many count) and
`backfilled`. When PostHog refuses or fails, `outcome` is `failure` or
`error` with `error_code` `connector_auth`, `connector_quota`,
`connector_invalid` or `connector_unavailable`, and the error is logged.

A check after a rank check or a conversions sync is
`seo_regressions.checked`, with `trigger` (`rank_check` or `conversions`),
`sites`, `found`, `resolved`, `fixes_new`, `fixes_reopened` and
`fixes_confirmed`; when it fails, `seo_regressions.failed` with
`error_code` `internal` and an error log, and the sync still counts as done.
Checks after a crawl or a Search Console sync add to the fix counts of
`crawl.finished` and `search_console.synced`.
