# SEO

Outflare reads how a domain looks from the outside through **DataForSEO**
and **Ahrefs**: domain rating, backlinks and referring domains, the
keywords a site ranks for and where, competitors in search, and (Ahrefs)
site audit issues. Both are paid per call, so every live call is capped by a
monthly budget, cached for a day and recorded in Outcomes with its cost.
Outflare only reads, and nothing else in Outflare needs SEO connected.

## Connecting

| Tool | What to paste | Where to find it |
| --- | --- | --- |
| DataForSEO | API login and API password | dataforseo.com, Dashboard → API Access (the API password is not your account password) |
| Ahrefs | Sign in with Ahrefs (Ahrefs Connect), or an API key | ahrefs.com, Account settings → API keys (needs a plan with API access). Signing in needs an Ahrefs Connect app on the server (`CONNECTOR_AHREFS_CLIENT_ID`); calls then spend the signed-in account's units. |

Connect either on the Connectors page (category SEO), with
`POST /v1/workspaces/$WS/integrations`, or let an agent ask with
`request_connection`: you get a one-time link to paste the key into, so the
key never passes through the chat. The connection test reads your DataForSEO
balance or your Ahrefs unit limit and usage, and costs nothing.

## Reports

Every tool answers the same reports in the same shape, keyed by domain.

| Report | What it answers | DataForSEO | Ahrefs |
| --- | --- | --- | --- |
| `overview` | Domain rating, backlinks, referring domains, ranking keywords, keywords in the top 3, estimated visits and traffic value per month | Yes | Yes |
| `keywords` | The keywords the domain ranks for: position, searches per month, estimated visits, ranking page | Yes | Yes |
| `backlinks` | The referring domains linking to it, strongest first | Yes | Yes |
| `links` | Single links to it, strongest linking domains first; `since=YYYY-MM-DD` adds links lost since then | Yes | Yes |
| `competitors` | Domains competing for the same keywords, with keywords in common | Yes | Yes |
| `audit` | Site audit health score and issues, errors first | No | Yes, for a site already set up as an Ahrefs Site Audit project |

DataForSEO's domain rating is its own 0 to 100 rank, and Ahrefs' is Ahrefs
DR, so the two are not the same scale. Traffic numbers are each tool's
estimate from search rankings, not your real visits; real visits come from
the [analytics connectors](analytics.md). Country defaults to the US; set
another two-letter country per connection or per call.

DataForSEO also reads Google's live results for one keyword, which is how
[rank tracking](search-intelligence.md#rank-tracking) checks where your
pages rank. Each check is about $0.02 for the top 100 and counts towards the
same budget.

## Cost and budget

- **DataForSEO** reports the exact cost of every call in dollars. An
  overview costs about $0.03 and a keyword list about $0.01 plus $0.0001 a
  row. The default budget is **$10 a month** per connection.
- **Ahrefs** charges API units from your plan. Its API does not return the
  cost of a call, so Outflare estimates it (at least 50 units a request,
  about 21 per keyword row and 11 per backlink or competitor row) and marks
  the ledger entry as estimated. The default budget is **10,000 units a
  month** per connection.
- The budget runs per calendar month (UTC). Before each live call Outflare
  estimates its cost and refuses with `402 budget_reached` if it would go
  over; raise the budget in the connection's SEO settings to carry on.
  The budget is the connection's monthly usage limit: its page under
  Connectors can also set a daily limit, make the limits only warn, and
  shows usage by day. Owners and admins are emailed at 80% and when a
  limit is reached ([usage-limits.md](usage-limits.md)).
- Every live call is recorded in Outcomes (category `data`, provider
  `dataforseo` or `ahrefs`), so SEO spend shows next to the rest of your
  cost per meeting. Calls that fail after the provider charged are recorded
  too.
- Answers are kept for 24 hours, so asking again the same day is free.
  Pass `fresh=true` to ask again.

## Weekly snapshots

Once a week each SEO connection takes an overview and the top 50 keywords
for each of its domains and stores them (`seo_snapshots`). That is what the
Analytics page, `GET /seo/history` and the overview read, so looking is free.
A new connection takes its first snapshot straight away, so its numbers show
a few minutes after you connect it, and a domain it starts following (a new
brand's website, say) is read within the hour instead of at the next weekly
snapshot.

- **Domains:** the connection's `domains` setting, or else up to five of
  your websites: your brands' websites first (only the brands the connection
  serves), then the sites you audit, then the ones your analytics tools
  follow. A subdomain of a domain already read is left out, because the
  providers count subdomains with their domain.
- **Cost:** about $0.05 a domain a week on DataForSEO (about $0.20 a
  month). About 1,200 units a domain a week on Ahrefs, so the default
  10,000-unit budget covers about two domains; raise it for more. A
  snapshot stops when the budget runs out.
- Turn it off with `"sync": "off"`, or take one now with
  `POST /seo/$ID/sync` or "Snapshot now" on the SEO page.
- Each website on the Website analytics page shows its latest SEO figures next to
  its analytics, and keyword rows show how far each position moved since the
  snapshot before. `GET /analytics/overview` includes the same figures under
  `seo` for each website.

## Is each brand growing?

`GET /seo/overview` (the SEO overview page, MCP `get_seo_overview`) answers that
for each brand from what is already stored, so it is free:

- **Branded search:** search console queries that contain one of the brand's
  `terms` (its name, handle and the names of its domains, such as `repocket`
  for repocket.com), added up by week. Consoles hide rare queries, so this is
  a floor, not a total.
- **Search clicks and visitors:** the brand's websites from every analytics
  tool, each website's day counted once (the tool that measured most).
- **Domain rating, backlinks, referring domains, organic traffic and
  keywords:** from the SEO snapshots, up to and including today's. Ahrefs wins
  when it is connected; the rating is the brand's main website's, the rest are
  added up across its websites.
- **SEO status:** `seo_status` says why those numbers are there or not: no SEO
  tool connected (`not_connected`), one connected but shared with other brands
  only (`not_shared`), no website for the brand (`no_website`),
  its SEO tool reads other domains (`not_followed`), snapshots off (`off`),
  the first read under way (`reading`), the last read failed (`failed`, with
  the reason for callers with `seo.read`, such as a used-up monthly budget),
  or `ready`. The page shows it with the next step, and checks back on its
  own while a first read is under way.
- **Sources:** `sources.search` and `sources.traffic` say whether the
  brand's websites have search console and visitor numbers (`ready`), or a
  connected tool has numbers for other websites of the workspace only
  (`other_sites`, with those `hosts`), or nothing has them yet
  (`not_connected`). So a brand whose website isn't in the connected tool, or
  whose site is set to another brand, is told that instead of being asked to
  connect a tool again.
- **Trends:** the last 4 whole weeks against the 4 before (for snapshots, the
  latest against the last one at least 4 weeks older). Within 3% is flat.
- **Site health and fixes:** the brand's audited sites and its open fixes, for
  callers with `seo.read`.

A brand's websites are its domains plus any analytics site set to report for
it. Websites no brand claims are a shared entry (`brand: null`). `?brand=`
narrows to one brand; `?weeks=` returns 8 to 52 weeks (default 12).

## Backlinks

Every SEO connection that reads single links (both DataForSEO and Ahrefs do)
also reads the links to each of the workspace's active
[SEO sites](crawler.md) with its weekly snapshot: the strongest
`link_rows` links by linking domain (200 by default, 1 to 1,000), live ones
and those lost in the last 90 days. Links to any subdomain of the site count;
links from the site's own subdomains do not.

- **One list from both tools.** A link is one linking page to one linked
  page. When both tools find it, it is one row (`GET /backlinks`, MCP
  `list_backlinks`) with each tool's own view under `providers`: its status,
  dates, the linking domain's rating, its spam judgement and why a lost link
  was lost. `source_rating` is Ahrefs DR when Ahrefs saw the link, else
  DataForSEO's rank on a 0 to 100 scale.
- **Live, lost, regained.** A link is live while any tool still finds it and
  lost once none does. Each change is an event that is never changed
  afterwards (`GET /backlinks/events`, MCP `list_backlink_events`): `new` when
  a tool first finds a link, `lost` (dated by the tool's last sighting, with
  its reason) and `regained` when a lost link is found again. A site's first
  read is a baseline: only links first found or lost in the 30 days before it
  become events, so old links don't show up as news.
- **Summary.** `GET /backlinks/summary` (MCP `get_backlinks_summary`) gives
  each site's live and lost links, referring domains, followed links, links
  gained, lost and regained in the last 30 days and in each of the last 12
  weeks, and each tool's latest read. The **Backlinks** page in the SEO
  module shows the same.
- **Cost.** A read is one call per site: about $0.03 on DataForSEO, and 50
  to 200 API units on Ahrefs at the default 200 rows (estimated at one unit
  a row). It counts against the connection's budget like any other call; a
  failed read is kept on its run (`links[].error` in the sync answer) and
  doesn't stop the snapshot. Each read is the audit action
  `backlinks.synced` with its counts.

Links are read with the workspace's own DataForSEO or Ahrefs account and kept
for that workspace only. Outflare has no link index of its own.

### Link spam

Every link gets a spam score from 0 (clean) to 100 (spam) after each read,
with the factors behind it (`spam_score`, `spam_level`, `spam_factors` on
`GET /backlinks`, filter with `spam=toxic|suspect|clean`). It is the higher of
the providers' own judgement (DataForSEO's backlink spam score, Ahrefs'
spam flag) and Outflare's checks added up: a very weak linking domain
(`weak_domain`), a domain ending spam networks favour (`spam_tld`), spam words
in the anchor (`spam_anchor`), a page with hundreds of outgoing links
(`link_farm`, `many_outlinks`) and the same domain linking 100 times or more
(`sitewide`). From 30 a link is `suspect`, from 60 `toxic`.

Nothing happens on a score alone. A person (or an agent they ask) reviews a
link with `PATCH /backlinks/{id}` (MCP `review_backlink`): `ok` for a real
link, `disavow`, or `none` to clear it, for one link or, with `domain: true`,
every link from its domain. `GET /backlinks/disavow?site_id=` (MCP
`get_disavow_file`) is the text file for Google Search Console's disavow
links tool: `domain:` lines for domains whose every link is marked, single
URLs otherwise. Outflare never uploads it and never contacts the linking
site. Each review is the audit action `backlink.reviewed`.

### Link opportunities

Each weekly links read, and each review, puts what is worth doing about a
site's links on the workspace's fixes list as source `seo_links`
([fixes](crawler.md#the-fixes-list)), next to the site audit's fixes:

| Rule | One fix per | When |
| --- | --- | --- |
| `link_reclaim` | linking domain | Links from a domain rated 30 or more were lost in the last 90 days and it no longer links to the site. |
| `link_broken_target` | page | Live links point to a page the latest complete crawl found broken (4xx or 5xx). Redirect it. |
| `link_gap_domain` | domain | A domain rated 30 or more links to 2 or more tracked competitors ([gaps](#gaps)) but not to the site. |
| `toxic_links` | site | Live toxic links nobody has reviewed yet. |

Each rule adds at most 25 fixes a site, strongest first. A fix the next read
no longer finds is done (`fix.confirmed`), and one marked done that is still
found opens again (`fix.reopened`). Marking links as spam takes them out of
the reclaim and broken-page fixes. `GET .../fixes/{fix}/prompt` lists the
links behind a fix for your own agent. These are suggestions only: Outflare
never contacts a site, asks for a link or disavows anything on its own.

## Competitors

Each weekly snapshot also asks every SEO connection which domains compete with
each active SEO site in search (the 10 sharing the most keywords) and keeps
them as **suggested** competitors (`GET /seo-competitors`, MCP
`list_seo_competitors`). Track the ones that matter, dismiss the rest (a
dismissed suggestion stays away), or add your own (`POST /seo-competitors`,
at most 20 tracked a site). Each competitor is linked to the customer record
with its domain when the workspace has one. Give it a `name` and `aliases`
(the other names it goes by, such as a short name AI answers use) with
`PATCH /seo-competitors/{id}`, so features that look for it by name use the
same list.

- **Their numbers come from your connection.** Each week the snapshot takes
  an overview of up to 10 tracked competitors a site (domain rating, organic
  traffic and keywords, referring domains), priced like one of your own
  domains and within the connection's budget. `numbers` on a competitor is
  the latest one.
- **A light public look.** OutflareBot reads a tracked competitor's
  robots.txt, its sitemaps and its home page once a week (or now, with
  `POST /seo-competitors/{id}/check`), obeying robots.txt: no other pages
  and no links followed ([OutflareBot](crawler.md#outflarebot)). It costs
  nothing. When robots.txt disallows OutflareBot, only the connection's
  numbers are shown.
- **The feed.** What changed is an event that is never changed afterwards
  (`GET /seo-competitors/events`, MCP `list_competitor_events`): pages added
  to or removed from their sitemaps (with up to 10 URLs), a new home page
  title or description, and moves in domain rating (a point or more), traffic
  or keywords (a tenth or more) and referring domains (a twentieth or more).
  The first look and the first snapshot are a baseline. The **Competitors**
  page in the SEO module shows both.

### Gaps

The weekly snapshot also reads the keywords and the linking domains of the
first 5 tracked competitors a site (`gap_rows` each, 50 by default, 0 turns it
off; set it with `PUT /seo/{id}/settings`), so you can see what they have that
you don't (`GET /seo-competitors/gaps`, MCP `get_seo_competitor_gaps`, the
**Keyword gaps** and **Link gaps** tabs on the Competitors page):

- **Keyword gaps** are keywords a competitor ranks in the top 20 for that
  your site doesn't rank for (`missing`) or ranks 3 places or more below
  (`behind`), the ones most competitors share and most searched first.
- **Link gaps** are domains that link to competitors but not to your site,
  the ones linking to most competitors and strongest first.

Both compare against what your connection read for you, so a gap can be a
keyword or link outside that read. Reading the gaps costs nothing; the weekly
read is priced like the keywords and links reports and counts against the
connection's budget. Gaps are ideas for content and links: Outflare never
contacts a site for you.

## API

```sh
# SEO connections, their domains, budget and snapshot status
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo"

# Is each brand growing in search: free
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/overview?brand=repocket"

# Stored snapshots: free, never calls the provider
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/history?report=overview&host=example.com"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/history?report=keywords&host=example.com&limit=50"

# Backlinks merged from both tools, and what changed: free
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/backlinks?status=live&order=rating"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/backlinks/events?kind=lost&days=30"

# Track a competitor, then read what changed at them
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"site_id": "'$SITE'", "host": "rival.example"}' "$URL/v1/workspaces/$WS/seo-competitors"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo-competitors/events?days=30"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/backlinks/summary"

# A live report: costs money within the budget, cached for a day
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/$ID/reports/backlinks?target=example.com&limit=100"

# Settings: domains, country, monthly budget (dollars or units), weekly snapshots
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"domains":["example.com"],"country":"GB","monthly_budget":25,"sync":"weekly"}' \
  "$URL/v1/workspaces/$WS/seo/$ID/settings"

# Take a snapshot now
curl -X POST -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/$ID/sync"
```

`target` can be left out when the connection follows one domain. Send
`"domains": null` or `"monthly_budget": null` to go back to the defaults.
Reading needs `analytics.read`; settings and snapshots need
`integrations.manage`.

## MCP

- `get_seo_overview`: each brand's search growth with its trends. Start here.
- `list_seo`: connections, domains, budget left and snapshot status.
- `get_seo_history`: stored snapshots, with each keyword's previous position.
  Use this first; it is free.
- `get_seo_report`: a live report. Costs money within the budget, so it is
  hidden when the MCP server runs read-only.
- `update_seo_settings` and `sync_seo`: change settings, take a snapshot.
- `get_analytics_overview` includes each website's latest SEO figures.
