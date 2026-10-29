# Analytics

Outflare reads the analytics tools already on your website, so a person, a
script or an agent can ask "how is the site doing?" without caring which tool
answers. Every provider answers the same five reports in the same shape.
Outflare only reads: nothing is changed at the provider. A sync stores each
tool's daily numbers in Outflare, so history can be queried, compared across
tools and added up without calling the providers again (see
[Stored history](#stored-history)). Nothing in the rest of Outflare needs
analytics connected. SEO tools (DataForSEO, Ahrefs) are covered in
[seo.md](seo.md); their figures join the same websites by domain.

## Providers

| Provider | Connect with | Reports |
| --- | --- | --- |
| Google Analytics 4 (`google_analytics`) | Sign in with Google (read-only), or a service account key | traffic, pages, sources, events (key events count as conversions) |
| Google Search Console (`google_search_console`) | Sign in with Google (read-only), or a service account key | traffic (clicks, impressions), pages, search_queries |
| Bing Webmaster Tools (`bing_webmaster`) | API key | traffic, pages, search_queries |
| PostHog (`posthog`) | Sign in with PostHog, US or EU Cloud (read-only), or a personal API key (cloud or self-hosted) | traffic, pages, sources, events |
| Plausible (`plausible`) | Stats API key and site domain, cloud or self-hosted | traffic, pages, sources, events (goals) |
| Fathom (`fathom`) | API key | traffic, pages, sources, events |
| Matomo (`matomo`) | Address and auth token | traffic, pages, sources, events (plus goal conversions) |
| Umami (`umami`) | Cloud API key, or a self-hosted address and login | traffic, pages, sources, events |
| Mixpanel (`mixpanel`) | Service account and project id | traffic (from the page view event), pages, sources, events |
| Amplitude (`amplitude`) | API key and secret key | traffic (active users, page views), pages, events |
| Cloudflare Web Analytics (`cloudflare_web_analytics`) | Sign in with Cloudflare, or an API token and account id | traffic (page views, visits), pages, sources; with Zone Read and Analytics Read on the token, also [AI crawler requests](ai-visibility.md#are-ai-crawlers-visiting) |
| Simple Analytics (`simple_analytics`) | Domain, plus API key for private sites | traffic, pages, sources |
| Microsoft Clarity (`microsoft_clarity`) | API token | traffic and pages for the last three days only, plus rage and dead clicks |
| Vercel Web Analytics (`vercel_analytics`) | Access token, plus team id for team projects | traffic, pages, sources, events (custom events); production traffic only, and the project needs Web Analytics turned on |

Listed but not connectable, because they have no read API for traffic:
Hotjar (surveys and user lookup only) and Segment (it forwards events but
does not count visits).

## Reports

| Report | What |
| --- | --- |
| `traffic` | Totals for the range plus one point per day |
| `pages` | Top pages |
| `sources` | Top referrers and sources |
| `search_queries` | What people searched before clicking through (Search Console, Bing) |
| `events` | Events and conversions by name |

Metrics are `visitors`, `pageviews`, `sessions` (visits), `bounce_rate` and
`ctr` (fractions from 0 to 1), `avg_duration` (seconds), `clicks`,
`impressions`, `position`, `events` and `conversions`. A metric a provider
does not measure is left out of the answer, never reported as zero. `notes`
explains caveats in words, such as Search Console's two to three day delay.

```sh
# Connected analytics tools, their reports and default site
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics"

# Traffic totals from every tool, one call
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/summary?from=2026-09-27&to=2026-10-24"

# Top 50 search queries from one connection
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/$ID/reports/search_queries?limit=50"

# Which sites a connection can read, the domain each counts under, and whether it syncs
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/$ID/sites"
# Pick the default site for live reports
curl -X PUT -H "Authorization: Bearer $TOKEN" -d '{"site":"123456789"}' "$URL/v1/workspaces/$WS/analytics/$ID/site"
# Give one site its domain, or switch it off (site ids are URL-encoded)
curl -X PATCH -H "Authorization: Bearer $TOKEN" -d '{"domain":"example.com"}' "$URL/v1/workspaces/$WS/analytics/$ID/sites/123456789"
curl -X PATCH -H "Authorization: Bearer $TOKEN" -d '{"sync":false}' "$URL/v1/workspaces/$WS/analytics/$ID/sites/sc-domain%3Aold.com"
```

Reads need the `analytics.read` scope (owners, admins, operators and viewers
have it). Changing sites needs `integrations.manage`. Dates are
inclusive `YYYY-MM-DD`; the default range is the last 28 days. When a
connection can read several sites and has no default, reports read the first
site it syncs; name another with `site`.

Answers are kept for 15 minutes (an hour for Mixpanel and Amplitude, six
hours for Clarity, which allows ten calls a day) so dashboards and agents do
not spend the provider's rate limits. `fresh=true` asks again.

Agents use the MCP tools `list_analytics`, `get_analytics_summary`,
`get_analytics_report`, `list_analytics_sites`, `set_analytics_site` and
`update_analytics_site` for
live answers, and `get_analytics_overview`, `query_analytics_history`,
`compare_analytics_tools` and `sync_analytics` for stored history. To
connect a tool, an agent finds it with `list_connectors` (category
`analytics`) and calls `request_connection`: the person pastes the key or
signs in with Google on the link, never in the chat.

## PostHog sign-in

PostHog reads Outflare's sign-in details from a document the server hosts at
`$PUBLIC_URL/v1/integrations/oauth/clients/posthog.json` (an OAuth
Client ID Metadata Document). That URL is the client id, so there is no app
to register with PostHog and no secret to set. PostHog has to fetch the
document, so the sign-in works only when `PUBLIC_URL` is a public https
address; on `localhost`, choose the API key instead. The sign-in asks
for `user:read`, `project:read` and `query:read`, the person picks which
projects to allow, and Outflare finds whether the account is on US or EU
Cloud by itself. PostHog shows an "unverified application" notice until the
app is verified, which is optional (see PostHog's OAuth docs).

## Stored history

Every six hours the engine copies each analytics connection's daily numbers
into Postgres (`analytics_daily`), one row per site, report, day and key, in
the same metrics as the live reports. One sign-in often sees many sites (every
GA4 property or PostHog project the login can open), so every site syncs
unless it is switched off, up to 20 per connection; past 20, sites sync once
switched on. Switching a site off deletes what was stored for it. Clarity syncs once a day and traffic
only, because its API allows ten calls a day and three days of history.

A sync that fails (the tool down, a refused key, Outflare's own error) is
tried again soon rather than after its whole interval: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works.
This includes the Search Console and conversions parts of the sync. Each
sync shows why its last run failed, and **Sync now** runs it straight away.

- **First sync:** 90 days of traffic, and 7 days of pages, sources, search
  queries and events (the top 50 per day). It starts within a few minutes of
  connecting.
- **Later syncs:** the last three days again, because providers keep
  revising recent days (Search Console runs two to three days behind).
  Breakdowns build up a day at a time from then on.
- **Limits:** a sync makes a few calls per site per report and day it
  fetches (about 30 on the first sync, about 15 after), so it stays well
  inside every provider's rate limits. A
  connection whose key stops working is marked in Connectors and skipped
  until it is fixed. Set `"sync_paused": true` in a connection's config to
  stop syncing it.
- **PostHog counts conversions by landing page.** Each PostHog project
  also stores, day by day, the visits that started on each page and how
  many went on to a conversion event, overall and from search engines (one
  query per sync: 90 days first, then the last three). Choose the events
  that count with `PATCH .../analytics/$ID/sites/$SITE` and
  `"conversion_events": ["signed_up"]`; changing them reads the 90 days
  again. The SEO module shows it by page (see
  [search intelligence](search-intelligence.md#conversions-by-landing-page)).
- **Websites line up by domain.** Each site gets a domain (`example.com`,
  without `www.`), taken from the site's address. Google Analytics
  properties and Vercel projects do not show one, so set it on the Analytics
  page or with `PATCH .../analytics/$ID/sites/$SITE` and `"domain"`. Stored
  history moves with it.
- **Sites belong to brands.** A site reports for the brand whose domains
  include its domain. Right after an analytics tool is connected, the app
  asks what to import: every site the sign-in sees, with a tick for the ones
  to store and a brand for each. Brands are guessed from the site's address
  or name ("zenshield-4fe59" is Zenshield) and test copies (staging, dev)
  start unticked. Picking a brand gives the site the brand's website as its
  domain, so nobody types one; "Change" under it sets a subdomain such as
  `docs.example.com`. The same list is under "N sites in {tool}" on the
  Analytics page. Through the API, `PATCH .../analytics/$ID/sites/$SITE` sets
  the domain and `PUT .../analytics/$ID/site-brand` with `{"site", "brand"}`
  (a handle, `none` for shared, or `auto` to follow the domain again) picks a
  brand outright.
- **Every site gets a decision.** Each row says why its brand was suggested
  ("Matches trailcast.example", "Name matches"), and Save waits until every
  ticked site has a brand, is shared by all brands, or is set to "Don't
  import". A site no brand fits can start one: "New brand from this site"
  makes a brand named after the site, with its domain as the website.
- **Pick a brand's sites without signing in again.** With one brand picked
  in the sidebar and no analytics counting for it, Analytics offers "Pick
  from {connection}" for each analytics connection the brand uses, and lists
  only that brand's sites and the ones no brand has yet.
- **People kept to some brands** (and their API tokens) see and place only
  their brands' sites and the ones no brand has yet, into their own brands:
  never another brand's site, a shared one, or another brand's domain.
- **Gaps show on Home.** Until every stored site has a brand, Analytics shows
  "Finish setting up analytics" and `GET /status` (and Home's needs-attention
  list) reports `analytics_site_unmapped` with the sites. A brand that no
  analytics tool reads is `brand_without_analytics`.

Search Console connections also store every page and query day by day, how
Google indexed each page and the sitemaps it read, for the SEO module's
Keywords page; see [docs/search-intelligence.md](search-intelligence.md).

Reading history never calls a provider, so it is fast and free:

```sh
# Every website, each tool side by side, search clicks added up across Google and Bing
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/overview?from=2026-07-01&to=2026-10-23"

# Each tool's daily traffic for one website, to compare them
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/compare?host=example.com"

# Top pages over three months, added up across tools
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/history?report=pages&group=key&host=example.com&from=2026-07-24&limit=50"

# Sync status, and sync now (one connection waits and returns rows stored; all is queued)
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/syncs"
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/$ID/sync"
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/sync"
```

`history` groups by `day` (one row per day per tool), `tool` (totals per
tool), `key` (each page, source, query or event across tools) or `tool_key`.
Counts are added up; click-through and position are weighted by
impressions. Visitors added up over several days count a returning visitor
once per day, so they run higher than a tool's own unique-visitor total for
the range. Syncing needs `integrations.manage`; reading needs
`analytics.read`. Deleting a connection deletes its stored history.

## For agents

Two endpoints are shaped for an agent (or a script) that needs an answer
rather than raw numbers. Both read stored data only, so they are free.

- **`GET /analytics/changes`** answers "what changed?" for each website:
  - `headline`: plain sentences that name their tool and dates, ready to
    repeat ("Visitors 1,180, up 68.6% on the 7 days before (700) (Plausible,
    2026-10-17 to 2026-10-23).").
  - `tools`: each tool's metrics over its own latest `days` stored days
    (default 7) against the same number of days before. Each tool keeps its
    own dates, so Search Console running two or three days behind is not
    read as a drop.
  - `search`: clicks and impressions added up across Google and Bing.
  - `movers`: the pages, sources and search queries that rose or fell most.
    Pages and sources come from one tool, because adding two tools' counts
    of the same visits would double them.
  - `unusual_days`: days at least half again above, or half below, the
    median of the 28 days before, and well outside the usual spread.
  - `seo`: the latest SEO snapshot against the one before, with keywords
    that moved up or down, new and lost.
  - `gaps`: what is missing for a full picture (no search console, no
    conversions, a sync that is failing or late, no SEO tool) with the
    connector ids to request or the fix.
  - `units`, once per answer, says what each metric counts.
- **`GET /analytics/catalog`** answers "what can I ask?": per website, the
  tools, which metrics each has stored per report, the days stored, the last
  sync, the SEO tools with their snapshots and budget left, and the same
  gaps. `ask` maps common questions to the endpoint or MCP tool that answers
  them.

```sh
# This week against last week, every website
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/changes"

# Month on month for one website, ten movers per list
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/changes?host=example.com&days=28&limit=10"

curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/analytics/catalog"
```

In MCP these are `get_analytics_changes` and `get_analytics_catalog`, and
the catalog is also the resource `outflare://analytics/catalog`. Three MCP
prompts run a whole review: `weekly_website_review`,
`explain_traffic_change` and `seo_check`. The server's instructions tell an
agent to start with `get_analytics_changes`, to use the stored-data tools
before the live ones, to give the tool and dates for every number, and to
offer the fix for each gap. Analytics and SEO tools answer compact JSON: no
indentation, no empty values, decimals cut to two places (four below 1).

## Google Analytics and Search Console

Both use the server's Google OAuth client (`GOOGLE_OAUTH_CLIENT_ID` and
`GOOGLE_OAUTH_CLIENT_SECRET`, the same one used for Google sign-in and Gmail
mailboxes). `CONNECTOR_GOOGLE_ANALYTICS_CLIENT_ID` and friends override it
per connector.

No Google client on the server? Connect either one with a **service account
key** instead: in Google Cloud, IAM & Admin, Service accounts, create one,
add a JSON key, and paste the whole file. Then give the service account's
email read access: a Viewer in Google Analytics (Admin, Property access
management) or a Restricted user in Search Console (Settings, Users and
permissions). Outflare signs in as the service account and never stores a
token, only the key, encrypted.

One sign-in connects both. Each Google connector asks for the Analytics and
the Search Console scope together, and when the person grants both, Outflare
connects the other tool too (unless the workspace already has it, or the
account has no properties there). The person can untick either scope on
Google's consent screen; only what they grant is connected.

In Google Cloud Console:

1. Enable the **Google Analytics Data API**, the **Google Analytics Admin
   API** and the **Google Search Console API** on the client's project.
2. Add `$PUBLIC_URL/v1/integrations/oauth/callback` to the client's
   authorised redirect URIs.
3. Add the scopes `https://www.googleapis.com/auth/analytics.readonly` and
   `https://www.googleapis.com/auth/webmasters.readonly` to the consent
   screen.

Both scopes are "sensitive" to Google. While the app is in testing, only
test users you list can connect (and their sign-in lasts seven days). To let
anyone connect, submit the app for Google's verification, which needs a
privacy policy, a homepage on your domain and a short video of the consent
flow, and usually takes a few days to a few weeks. Sensitive scopes do not
need the paid security assessment that restricted scopes (such as full
Gmail) need.
