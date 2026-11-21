# SEO

Outflare reads how a domain looks from the outside through **DataForSEO**,
**Ahrefs** and **SE Ranking**: domain rating, backlinks and referring domains, the
keywords a site ranks for and where, competitors in search, and (Ahrefs)
site audit issues. All three are paid per call, so every live call is capped by a
monthly budget, cached for a day and recorded in Outcomes with its cost.
Outflare only reads, and nothing else in Outflare needs SEO connected.

## Connecting

| Tool | What to paste | Where to find it |
| --- | --- | --- |
| DataForSEO | API login and API password | dataforseo.com, Dashboard → API Access (the API password is not your account password) |
| Ahrefs | Sign in with Ahrefs (Ahrefs Connect), or an API key | ahrefs.com, Account settings → API keys (needs a plan with API access). Signing in needs an Ahrefs Connect app on the server (`CONNECTOR_AHREFS_CLIENT_ID`); calls then spend the signed-in account's units. |
| SE Ranking | Data API key | seranking.com, API dashboard → Data API key (needs a Data API plan) |

Connect either on the Connectors page (category SEO), with
`POST /v1/workspaces/$WS/integrations`, or let an agent ask with
`request_connection`: you get a one-time link to paste the key into, so the
key never passes through the chat. The connection test reads your DataForSEO
balance, your Ahrefs unit limit and usage, or your SE Ranking credits left,
and costs nothing.

## Reports

Every tool answers the same reports in the same shape, keyed by domain.

| Report | What it answers | DataForSEO | Ahrefs | SE Ranking |
| --- | --- | --- | --- | --- |
| `overview` | Domain rating, backlinks, referring domains, ranking keywords, keywords in the top 3, estimated visits and traffic value per month | Yes | Yes | Yes, without domain rating, links or the top 3 (it counts positions 1 to 5 together) |
| `keywords` | The keywords the domain ranks for: position, searches per month, estimated visits, ranking page | Yes | Yes | Yes |
| `backlinks` | The referring domains linking to it, strongest first | Yes | Yes | No |
| `links` | Single links to it, strongest linking domains first; `since=YYYY-MM-DD` adds links lost since then | Yes | Yes | No |
| `competitors` | Domains competing for the same keywords, with keywords in common | Yes | Yes | Yes |
| `pages` | The domain's pages that get the most search traffic: estimated visits a month, ranking keywords and (Ahrefs) the top search | Yes | Yes | No |
| `audit` | Site audit health score and issues, errors first | No | Yes, for a site already set up as an Ahrefs Site Audit project | No |

DataForSEO's domain rating is its own 0 to 100 rank, and Ahrefs' is Ahrefs
DR, so the two are not the same scale. Traffic numbers are each tool's
estimate from search rankings, not your real visits; real visits come from
the [analytics connectors](analytics.md). Country defaults to the US; set
another two-letter country per connection or per call.

DataForSEO also reads Google's live results for one keyword, which is how
[rank tracking](search-intelligence.md#rank-tracking) checks where your
pages rank. Each check is about $0.02 for the top 100 and counts towards the
same budget. A Geonode connection can check ranks too, through Geonode's
Google Search Light API: one credit per page of 10 results, about $0.005 for
the top 100, within a monthly budget you set on the Geonode connection.

## Cost and budget

- **DataForSEO** reports the exact cost of every call in dollars. An
  overview costs about $0.03 and a keyword list about $0.01 plus $0.0001 a
  row. The default budget is **$10 a month** per connection.
- **Ahrefs** charges API units from your plan. Its API does not return the
  cost of a call, so Outflare estimates it (at least 50 units a request,
  about 21 per keyword row and 11 per backlink or competitor row) and marks
  the ledger entry as estimated. The default budget is **10,000 units a
  month** per connection.
- **SE Ranking** charges 100 API credits from your Data API plan for every
  request, whatever the number of rows. Its API does not return the cost of
  a call, so Outflare records 100 units a call and marks the entry as
  estimated. The default budget is **10,000 credits a month** per
  connection, counted as API units. The keywords your SE Ranking projects
  track come into rank tracking each day with the same key, at no Data API
  cost; see [Keywords tracked in SE Ranking](search-intelligence.md#keywords-tracked-in-se-ranking).
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
- Listing Link orders candidates, and reading a saved link gap, do not spend
  the connection budget. A live `backlinks` or `links` report, or
  `POST /seo-competitors/gaps/links`, does. See [link-orders.md](link-orders.md).

## Weekly snapshots

Once a week each SEO connection takes an overview and the top 50 keywords
for each of its domains and stores them (`seo_snapshots`). That is what the
Analytics page, `GET /seo/history` and the overview read, so looking is free.
A new connection takes its first snapshot straight away, so its numbers show
a few minutes after you connect it, and a domain it starts following (a new
brand's website, say) is read within the hour instead of at the next weekly
snapshot.

A domain whose snapshot fails (the provider down, a spent budget, a refused
key) is read again soon instead of at next week's snapshot, so the weekly
series has no gaps: after 15 minutes, then 1 and 2 hours, then every 4 hours until one works. Only the domains that failed are read
again, so the domains that worked cost nothing more.

- **Domains:** the connection's `domains` setting, or else up to five of
  your websites: your brands' websites first (only the brands the connection
  serves), then the sites you audit, then the ones your analytics tools
  follow. A subdomain of a domain already read is left out, because the
  providers count subdomains with their domain.
- **Cost:** about $0.05 a domain a week on DataForSEO (about $0.20 a
  month). About 1,200 units a domain a week on Ahrefs, so the default
  10,000-unit budget covers about two domains; raise it for more. On SE
  Ranking 200 credits a domain a week (one overview and one keyword list),
  so the default budget covers about twelve domains. A
  snapshot stops when the budget runs out.
- Turn it off with `"sync": "off"`, or take one now with
  `POST /seo/$ID/sync` or "Snapshot now" on the SEO page.
- Each website on the Website analytics page shows its latest SEO figures next to
  its analytics, and keyword rows show how far each position moved since the
  snapshot before. `GET /analytics/overview` includes the same figures under
  `seo` for each website.

## The SEO dashboard

The SEO overview opens on a dashboard (`GET /v1/brands/{brand}/seo/board`):
five tiles, each set against the period it is usually compared with, with a
green or red change.

| Tile | Dates (brand's time zone) | Compared with |
| --- | --- | --- |
| Today | today | the same weekday a week before |
| Yesterday | yesterday | the same weekday a week before |
| Month to date | the 1st to today | the same days of last month |
| This month (forecast) | the whole month | the whole of last month |
| Last month | last month | the month before |

Each tile shows first-touch Organic Search revenue (USD), payments and
sign-ups from stored PostHog conversions, and Search Console clicks; "More
numbers" adds organic sessions, impressions, CTR and position. The forecast
scales the month so far to the whole month by the days each number has:
conversions through yesterday, clicks through the last Search Console day.
Search Console runs two or three days behind, so today and yesterday usually
show conversions but no clicks; a number no stored day has shows as a dash,
never as 0.

Picking a tile lists the pages behind it (Search Console clicks by page from
the page, country and device segments, joined to organic conversions by
landing page; at most 200, most revenue first), sortable and downloadable as
CSV, and a map by country. The map shades countries by revenue, payments,
clicks, clicks that brought no payment, impressions, revenue per click,
organic sessions, average time on site or sign-ups; hovering a country
shows all of them, and the table under it sorts by any. Clicks and
impressions are Search Console's; sessions, time on site, sign-ups,
payments and revenue come from PostHog (first-touch Organic Search, by the
visitor's first country, payments counted once and USD only, stored in
`organic_countries` and readable as `query.seo_organic_countries`). Very
small countries (Singapore, Hong Kong, Malta) have no shape on the map and
are listed under it and in the table. Like the rest of the overview it reads
only what is stored, so it costs nothing.

## Target keywords and their rankings

SEO overview, Targets (`GET /v1/brands/{brand}/seo/targets`) lists every page
of the brand's SEO sites that has a target keyword (set on Keywords, Targets),
with how it ranks:

- **Position** in Search Console for the exact keyword on that page:
  impression-weighted over the 28 days to the newest stored day, its change
  from the 28 days before and from the week before, and a 12-week line.
  Clicks and impressions are for the same keyword and page.
- **Tracked rank** from the rank tracker, for the keywords it checks, by
  country and device, with the change from the check before and whether the
  ranking URL is the target page. "Track every target keyword" on Keywords,
  Rankings adds the missing ones.
- **Competing pages**: other pages of the site with at least 10 impressions
  for the keyword in the same 28 days.
- **On the page**: whether the keyword is in the title, H1 and description in
  the latest crawl.

Each target gets a band from the newest tracked rank, else Search Console's
position: top 3, top 10, 11–20 (striking distance), 21+ or no data. The tiles
at the top count each band, improved and declined targets, competing ones,
ones missing the keyword in the title or H1, and untracked ones, and filter
the table. Opening a row shows the secondary keywords with their own
positions, every tracked rank, and the competing pages. It reads only what is
stored, so it costs nothing.

### The page passport and a keyword map

Each target can also say what the page is for and what it holds:

- **Intent**: informational, commercial, transactional or navigational.
- **Page type**: homepage, product, pricing, use-case, comparison, blog,
  location, tool, docs or other.
- **Summary**: Markdown, up to 20,000 characters, of what the page holds:
  its headings, plans and prices, features, calls to action and the
  questions it answers.

Edit them when you open a row on SEO overview, Targets, or set them with the
API (`PUT` or `PATCH` on a target) and the MCP tools `set_page_target` and
`update_page_target`. A whole keyword map imports from a CSV file (Import
CSV on the Targets view) or through `POST
/v1/brands/{brand}/seo-sites/{site}/targets/import` and the MCP tool
`import_page_targets`, 1000 pages per request. The CSV has a header row with
`url`, `primary_keyword`, `secondary_keywords` (split with `|`), `intent`,
`page_type` and `summary`. A page that already has a target keeps whatever a
row leaves empty; a new page needs a keyword. Wrong rows are skipped and
listed. A site lists up to 10,000 targets.

Each target then gets a status, the first that applies:

| Status | When |
| --- | --- |
| Cannibalized | Another page of the site gets more impressions for the keyword in Search Console, or the rank tracker finds another page. The page that ranks instead is shown. |
| Wrong intent | The page type rarely answers the intent: informational on a product or pricing page, transactional on a blog or docs page, navigational on a blog or comparison page. |
| Missing content | The keyword is not in the title or H1, or the summary doesn't mention a secondary keyword. |
| No data | No impressions, tracked rank or crawl yet. |
| On target | None of the above. |

A row also lists suggested fixes: where to put the keyword, which pages to
link from (with the keyword as the anchor), when to retarget, what the summary
leaves out, and when a page in striking distance is worth links. These are
plain rules from stored data; they cost nothing.

### Money left on the table

Under the dashboard's pages and map, Money left on the table
(`GET /v1/brands/{brand}/seo/money`) puts search gaps in dollars, over the 28
days to the newest Search Console day stored:

- **Positions 4–20**: query and page pairs with at least 100 impressions in
  positions 4 to 20, with the clicks they would get at position 3 by the
  expected CTR curve, less the clicks they get now.
- **Low CTR**: pairs with at least 1,000 impressions whose CTR is under 70%
  of the expected CTR for their position.
- **Losing pages**: pages with at least 10 fewer clicks than in the 28 days
  before, with the modeled loss and, beside it, the page's own first-touch
  revenue change (never added in).

Every dollar is extra clicks times **revenue per organic click**: the
brand's first-touch organic revenue over its Search Console clicks in the
same 28 days. You can type your own rate. Queries that name the brand are left
out unless you tick Brand searches. Each row shows the link orders of the last
90 days (not lost) that already point at its page. How it's counted shows the
formula, the rate and the expected CTR curve (1: 28%, 2: 15%, 3: 10%, 4: 7%,
5: 5%, falling to under 1% past 10). These are estimates to rank work by, not
revenue earned or lost.

### Declining pages

SEO overview, Declining (`GET /v1/brands/{brand}/seo/declining`) lists the
pages whose Search Console clicks fell over the last 7 or 28 days to the
newest stored day against the same length before. Periods are whole, so a
partial week is never set against a full one.

A page is declining when it lost at least 30% of its clicks with at least 50
in the period before, or 100 clicks or more whatever the share. The period
before that sorts it:

- **New**: it was not declining the period before.
- **Still declining**: it was.
- **Recovering**: it was declining and now gained clicks; it is not counted
  in the loss.

Each page shows its clicks, impressions, position and CTR against before and
what moved most (impressions, position or CTR); the countries, device and
query that lost most; its first-touch sign-ups and revenue; a modeled loss
(clicks lost times revenue per organic click over the last 28 days); a flag
when the latest crawl found it erroring, redirecting or noindex; and how many
other pages of the site now show for its biggest losing query. Site audits'
regression fixes keep watching week against the four weeks before; this view
is for looking into a drop now.

### Cannibalization across a brand

SEO overview, Cannibalization (`GET /v1/brands/{brand}/seo/cannibalisation`)
lists every search two or more of the brand's pages compete for, over all its
SEO sites, with the same rule as Keywords, Cannibalisation: over the 28 days
to the newest Search Console day, at least 100 impressions, and two or more
pages each with a tenth of them (at least 10) at position 30 or better.
Searches naming the brand are left out.

Against the 28 days before, each search is **new**, **ongoing** or has
**gained a page**; searches that competed before and no longer do are listed
as **resolved**. Each one shows:

- the page to keep: the one you chose (Keep this one, or
  `PUT /v1/brands/{brand}/seo-sites/{site}/cannibalisation/primary`), else
  the one that targets the search, else the one with the most clicks;
- whether its pages share a language: searches across languages (such as
  `/` and `/de/`) are in their own list, as they usually need hreflang or
  targeting rather than consolidation;
- the split clicks (what the other pages got) and an estimated gain (the
  clicks if the page to keep got every impression at its own CTR, less the
  clicks now);
- its fix on the fixes list, when there is one.

### Country pages

SEO overview, Country pages (`GET /v1/brands/{brand}/seo/country-pages`)
shows how pages made for a country, city or network rank in that country.
Pick a template (a path such as `/proxies/`; templates with three or more
pages are offered).

Each page's **target country** is the country its target keyword is tracked
in (the one it shows in most, when tracked in several), else the country
Search Console shows it in most. Its clicks, impressions, CTR and position
are Search Console's for that country only, over the 28 days to the newest
stored day against the 28 before, beside the tracked rank there; a band uses
the tracked rank, else that position. A page is **not tracked** when it has
no target keyword, or its keyword is not tracked in its target country.

Templates (the path without its last part) show the pages' median position
now and before, so a template or translation change that lifts or sinks a
whole group shows as one move.

## Technical SEO at a glance

The **Technical** view of SEO overview (`GET /v1/brands/{brand}/seo/technical`, MCP `get_seo_technical`) is the quick read of a brand's technical health; Site audits keeps every page and fix. It reads only what is stored:

- **Issues by rule** from the latest finished crawl of each of the brand's sites, against the crawl before. Each rule counts **unique pages**, not occurrences, and **templates** with the language part of the path taken out, so one template error across ten languages is one template. **New** pages have the issue now and did not before; **fixed** pages had it and no longer do. A page only one of the two crawls reached is neither, so a crawl that stopped at its page limit does not look like a wave of fixes.
- **Clicks at stake**: the Search Console clicks of the pages an issue touches over the last 28 days stored. Rules are sorted by them, then errors before warnings, then pages. Notices are hidden until you ask for them.
- **Per site**: the latest crawl and the one it is compared with, pages fetched, indexed and not indexed from the indexing snapshots (with the change since the snapshot before), and whether robots.txt changed between the two crawls.
- **Core Web Vitals** from real visits recorded by the Outflare tag: the 75th percentile LCP, INP and CLS per page and device over 28 days, for pages with at least 20 visits, worst first. Poor means past Google's lines: LCP 4 s, INP 500 ms, CLS 0.25.
- **Sitemaps** as Search Console reports them: submitted, indexed, errors and warnings.

## Links at a glance

The **Links** view of SEO overview (`GET /v1/brands/{brand}/seo/links`, MCP `get_seo_links`) answers whether a brand's link footprint is growing and whether its bought links still hold; Backlinks and Link orders keep every record. It reads only what is stored, so it costs no API units:

- **Referring domains** a week over 13 weeks from the weekly SEO snapshots (Ahrefs when it has any for a domain in the window, else the other connection; the last snapshot of each week), for the brand's domains and the 8 tracked competitors with the most, with the change against one and four weeks before.
- **Velocity**: new, lost and regained links a week over 12 weeks, and the linking domains behind the new and lost ones, from backlink events. The connections read the strongest links per domain, so this is a trend, not a full count.
- **Lost links** in the last 30 days, bought ones first: a lost link is bought when a link order was published on the same page.
- **Bought links that need a look**: published, paid, verified or lost link orders whose last live check did not find the link, found it pointing at another page or with a different anchor than ordered, or has not run for 3 days. Each opens its order in Link orders.
- **Anchors** of live links: branded (the brand's name or domain), URL, generic (click here, website), exact (a target keyword of one of the brand's pages), partial (most words of one), other, or no text.

## Is each brand growing?

`GET /seo/overview` (the SEO overview page, MCP `get_seo_overview`) answers that
for each brand from what is already stored, so it is free:

- **Non-branded search:** search clicks minus branded clicks, with CTR and
  the impression-weighted average position of non-branded queries. A week
  with search totals but no query rows leaves branded and non-branded
  unknown: missing query history is not read as zero.
- **Branded search:** search console queries that contain one of the brand's
  `terms` (its name, handle and the names of its domains, such as `summitly`
  for summitly.example), added up by week. Consoles hide rare queries, so this is
  a floor, not a total.
- **Search conversions:** conversions on visits that landed from a search
  engine, from stored product analytics (PostHog page conversions).
- **Keywords in the top 3 and top 10:** top 3 from the same SEO snapshots as
  the other snapshot tiles; top 10 is the share of active tracked keywords
  that were in the top 10 that week.
- **AI mention and recommend rates:** the share of stored AI answers that
  week that name or recommend the brand.
- **Share of voice:** the brand's share of estimated search visits, and of
  keywords in the top 3, among itself and its tracked competitors, from
  stored snapshots of the same connector. Opening the page does not refresh
  them.
- **Search clicks and visitors:** the brand's websites from every analytics
  tool, each website's day counted once (the tool that measured most).
  Visitors are all channels, not organic only.
- **Domain rating, backlinks, referring domains, organic traffic and
  keywords:** from the SEO snapshots, up to and including today's. The
  series uses one connector (Ahrefs when it has a snapshot in the period)
  so the arrows compare like with like; the rating is the brand's main
  website's, the rest are added up across its websites.
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
- **Trends:** the last 4 whole weeks against the 4 before (for snapshots and share of voice, the
  latest against the last one at least 4 weeks older). Within 3% is flat. For average position, a drop is up.
- **Period:** `period` sets the whole period picked against as many weeks
  just before it (`previous_from` to `previous_to`, listed in
  `previous_weeks`): totals for counts, the mean for rates and positions, and
  for snapshots and share of voice the latest against the last one before the
  period.
- **Site health and fixes:** the brand's audited sites and its open fixes, for
  callers with `seo.read`.

A brand's websites are its domains plus any analytics site set to report for
it. Websites no brand claims are a shared entry (`brand: null`). `?brand=`
narrows to one brand; `?weeks=` returns 4 to 104 weeks back from the last
week that is over (default 12), and `?from=` and `?to=` pick the whole weeks
those days fall in instead (at most 104).

The page shows each number on its own row: its value for the period, the
change against the period before, and a chart with values on the y-axis and
each week's number on hover. Counts per week also name the week they rose and
the week they fell most, and draw the weeks before as a dashed line. Pick the
weeks at the top (4 weeks to 2 years, or custom dates); the choice stays in
the page's address, so a shared link shows the same weeks.

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
  become events, so old links don't show up as news. Each is also a webhook
  event (`backlink.new`, `backlink.lost`, `backlink.regained`), for a
  playbook that asks for a lost link back.
- **What each link carries.** Like Ahrefs' backlinks report: the linking
  domain's DR (`source_rating`) and the page's UR (`url_rating`), how many
  keywords the page ranks for, how many domains it links to and links it
  has to other sites, its title, author, language, platform, page type and
  topics (Ahrefs' page types such as `/Article/Listicle`), the link's type
  (`text`, `image`, `redirect`, `canonical`, ...), whether it sits in the
  page's main content, and `hops`: where the linked URL redirects to. Ahrefs
  gives all of these; DataForSEO gives the page's rank, link type, language,
  platform and redirects.
- **Traffic is a choice.** Set `link_traffic: true` on an Ahrefs
  connection (`PUT /seo/{id}/settings`, or the checkbox in its settings on
  Rankings & backlinks) to also read each linking domain's and page's
  monthly organic traffic and the page's referring domains. Ahrefs charges
  about 25 more API units a link for these, so it is off by default: at 200
  links that is 5,200 more units a site a week. DataForSEO has no traffic
  for links.
- **Filters cost nothing.** `GET /backlinks` filters and sorts what is
  stored: `follow` (`dofollow`, `nofollow`, `ugc`, `sponsored`), `link_type`
  (or `content` for links in the main content), `dr_min`/`dr_max`,
  `ur_min`/`ur_max`, `domain_traffic_min`/`max`, `page_traffic_min`/`max`,
  `page_type`, `source_domain`, and `q` for words or a phrase, narrowed with
  `q_in` (`source_url`, `title`, `anchor`, `target_url`). `group=domain`
  shows one link per linking domain (the best one: live, followed, in the
  content, from the strongest page) with `group_links` saying how many it
  stands for; `group=page` does the same per linking page. History works
  like Ahrefs': `since=YYYY-MM-DD` adds the links lost since then to the live
  ones, and `change=new` or `change=lost` lists only the links first seen or
  lost since then.
- **Summary.** `GET /backlinks/summary` (MCP `get_backlinks_summary`) gives
  each site's live and lost links, referring domains, followed links, links
  gained, lost and regained in the last 30 days and in each of the last 12
  weeks, each tool's latest read, and the live links by page type and link
  type (for filters). The **Backlinks** page in the SEO module shows the
  same, with the links table: one link per domain by default, All, New or
  Lost over a history range, every filter above, and the full referring and
  target URLs to click.
- **Cost.** A read is one call per site: about $0.03 on DataForSEO, and 50
  to 200 API units on Ahrefs at the default 200 rows (estimated at one unit
  a row). It counts against the connection's budget like any other call; a
  failed read is kept on its run (`links[].error` in the sync answer),
  doesn't stop the snapshot, and is tried again on its own soon (after 15
  minutes, then 1 and 2 hours, then every 4 hours), as is a site's failed
  read of its competitors, until one works or next week's snapshot comes. Each read is the audit action
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

Every day each SEO connection is asked which domains compete with each active
SEO site in search (the 50 sharing the most keywords, one read a site a day
within the connection's budget), and the domains ranking in the top results
beside the site for at least two of its tracked keywords in the last week are
added at no cost (source `rankings`). Platforms that rank for nearly
everything without competing with anyone (GitHub, YouTube, Reddit,
Wikipedia, Medium, app and review sites and the like) are never suggested
from either, and a platform suggested before this is withdrawn. Unless
someone added it by hand, a platform is never read on the connection's
budget: no weekly numbers, keyword or link gaps, or dead-page reads. They are
kept as **suggested** competitors (`GET /seo-competitors`, MCP
`list_seo_competitors`). Track the ones that matter, dismiss the rest (a
dismissed suggestion stays away), or add your own (`POST /seo-competitors`,
as many as you like). Each competitor is linked to the customer record
with its domain when the workspace has one. Give it a `name` and `aliases`
(the other names it goes by, such as a short name AI answers use) with
`PATCH /seo-competitors/{id}`, so features that look for it by name use the
same list. It is the brand's one competitor list: Presence scans it, AI
visibility counts it, a competitor with a website named on an AI visibility
cohort is tracked on it (source `ai_visibility`), and Reddit actions write
the competitors they name as it names them.

- **Their numbers come from your connection.** Each week the snapshot takes
  an overview of every tracked competitor (domain rating, organic traffic
  and keywords, referring domains), priced like one of your own domains and
  within the connection's budget. The ones never read, then the ones read
  longest ago, go first, so a run the budget stops is picked up by the next.
  A competitor the provider can't read is skipped, not the rest. `numbers`
  on a competitor is the latest one.
- **Each competitor is read at most once a month.** A competitor the
  connection read in the last `competitor_read_days` (30 by default, 1 to
  365; set it with `PUT /seo/{id}/settings` or **Read each competitor at most
  once every** in the SEO tool's settings) is skipped, however the read is
  asked for: the weekly snapshot, **Refresh all competitors**, or one
  competitor's **Read their search data**. So hundreds of tracked
  competitors cost one read each a month, not one a week, and pressing
  refresh again costs nothing.
- **A light public look.** OutflareBot reads a tracked competitor's
  robots.txt, its sitemaps and its home page once a week (or now, with
  `POST /seo-competitors/{id}/check`), obeying robots.txt: no other pages
  and no links followed ([OutflareBot](crawler.md#outflarebot)). It costs
  nothing. When robots.txt disallows OutflareBot, only the connection's
  numbers are shown. A look that failed for another reason (the site didn't
  answer) is tried again after 15 minutes, then 1 and 2 hours, then every 4
  hours until one works, not a week later.
- **The feed.** What changed is an event that is never changed afterwards
  (`GET /seo-competitors/events`, MCP `list_competitor_events`): pages added
  to or removed from their sitemaps (with up to 10 URLs), a new home page
  title or description, and moves in domain rating (a point or more), traffic
  or keywords (a tenth or more) and referring domains (a twentieth or more).
  The first look and the first snapshot are a baseline. The **Competitors**
  page in the SEO module shows both. Each page gone from their sitemaps is
  also a webhook event (`competitor.page_removed`, at most 50 a look), since
  sites still linking to it may link to yours instead.

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

**Link orders** (the Link orders page in the SEO module, [link-orders.md](link-orders.md))
uses the already-saved link gap as its Candidates list
(`GET /v1/brands/{brand}/link-orders/candidates`). That read is free: it does
not call Ahrefs or DataForSEO, and it does not `POST /seo-competitors/gaps/links`
(that route is a paid referring-domain pull and is never started from Link
orders). A connection that cannot produce a backlinks snapshot cannot fill this
list. **SE Ranking** has no `backlinks` or `links` report, so it does not feed
referring domains, the link gap, or Link orders candidates.

Click a keyword gap to see **who ranks for it**: the top 20 results, competitor
or not, marked as you, tracked, suggested or not a competitor, with Yes and No
for suggestions and Track for the rest, including a site you said no to
before, so one click makes it a competitor (`GET /seo-competitors/results`, MCP
`get_seo_keyword_results`). Once a day, Outflare reads the results pages of up
to 25 of a site's keyword gaps (the ones most competitors rank for first, each
at most once a week) on the workspace's own SERP connection (DataForSEO or
Geonode), within its budget, and keeps them. A keyword not read yet can be read
from its page. A tracked keyword's latest rank check (top 10) shows the same way.

### More competitors from the keyword gaps

The domains that rank for at least two of the keyword gaps read in the last 30
days are suggested as competitors (source `keyword_gaps`, up to 25 a day), so a
competitor that ranks beside the ones you track but that no connection named
shows up to say Yes or No to. It never suggests your own site, a platform anyone
uses (GitHub, Reddit, YouTube and the like) or a domain already on the list,
dismissed ones included. **Find more competitors** on the Competitors page
(`POST /v1/brands/{brand}/competition/discover`, MCP `discover_competitors`)
reads the next batch now. Without a SERP connection it says to connect one.

## Link prospects

Every way of earning links (competitors' links, broken links, links to win
back, competitors' dead pages, resource pages, promoting a piece of content,
mentions without a link, partners, pages that still link to a domain that
has expired) adds to one list of **link prospects**:
pages that could link to one of your sites (`POST /link-prospects`, up to 500
at a time, MCP `add_link_prospects`). Playbooks add to it too. It costs
nothing: numbers come from what is stored.

- **One company per website.** Each page's website is a company record (a
  prospect company when you have none), so its people, replies and deals
  stay in one place.
- **A score from 0 to 100**, best first: half the website's strength (domain
  rating), a quarter its search traffic, a quarter the method (winning back
  a lost link scores highest), less 20 for a suspect spam score.
  `score_factors` says why. Numbers you don't pass come from your backlinks
  and the SEO connections' snapshots.
- **Kept out, with why.** A page is added as `excluded` when its website
  already links to the site (`links_to_us`, except when winning a link back
  or asking a site that names you), is a tracked competitor, is on the
  suppression list (`competitor`, `customer`, `partner`, or `opted_out` for
  any other reason) or has toxic links (`spam`).
- **Stages.** `prospect`, then `pitched`, `replied`, `agreed` and `live`, or
  `declined`; `removed` when a live link goes away (`PATCH
  /link-prospects/{id}`, MCP `update_link_prospect`). Each move is a webhook
  event (`link_prospect.*`).
- **One pitch per website.** While a prospect on a website is pitched,
  replied or agreed, or for the cool-down after one was declined (60 days by
  default, `pitch_cooldown_days` in the [settings](#link-reads)), moving another
  page there into outreach answers `409 site_in_outreach`. A website that
  opts out is never pitched, even when it was added earlier, and an excluded
  prospect goes back to `prospect` before it can be pitched.

The **Link building** page under Outreach shows the list, the pitches
waiting for approval, results by method, competitors' dead pages (add their
linking sites as prospects in one click), a domain's most linked pages and
the settings. Outflare never offers or agrees to pay for a link.

### Checking prospects

OutflareBot checks a prospect's page (`POST /link-prospects/{id}/check`, or
up to 100 at a time with `POST /link-prospects/check`; MCP
`check_link_prospects`), at most once an hour, obeying robots.txt and crawl
opt-outs. It reads the page and at most two of the site's contact, about or
write-for-us pages, and each outbound link once. The prospect's `check` says:

- **Up or not**: up, gone (404 or 410), redirected to another site, blocked
  by robots.txt, opted out or unreachable.
- **Links to us**: whether it links to the site already, with the links.
- **Broken links**: which of its first 50 outbound links are gone or don't
  answer, with the Internet Archive's closest snapshot of up to 10, so you
  know what the dead page said.
- **The AI's read**, with the workspace's own AI key (the tool "Link page
  reading" on the AI usage page, with its own limit): what kind of page it is,
  its topic, its author, how well it fits the site (`fit`, 0 to 100, which
  moves the score up to 15 points either way) and which of the site's pages
  to offer. Without AI, the page sharing the most words is suggested.
- **Contacts**: addresses at the website's own domain on the page and its
  contact pages, and the author's likely address from the free email finder.
  Each becomes a person record on the website's company.

A page to pitch that already links, or is gone, is excluded (`links_to_us`,
`page_gone`). An empty page to offer takes the suggestion.

**Winning back lost links.** `POST /link-prospects/reclaim` (MCP
`reclaim_link_prospects`) adds the links you lost in the last 90 days as
prospects, with the page they linked to as the page to offer. It's free;
check them next to see which pages are still up and who to ask.

### Pitching

A pitch goes out through one of your own Outflare sequences (`POST
/link-prospects/{id}/pitches` with `sequence_id`, MCP `start_link_pitch`), to
one of the prospect's contacts (`record_id`, by default the surest with an
email address). The sequence's first step uses `{{custom.opener}}`. The
**starter sequence** has one ready (`POST /link-building/sequences`, MCP
`create_link_sequence`, or "Create the starter sequence" on the page): a
draft whose first email is the opener, with one follow-up five days later
in the same thread. Edit it like any sequence. The workspace's AI (the tool
"Link pitches" on the AI usage page, with its own limit) writes the opener for
each page, from what the page check found: its topic, its author, a broken
link it has, and your page to offer. It never offers payment, and the page
is read as data, never as instructions. `GET /link-building/sequences` lists
your sequences with `uses_opener`.

- **Approval, on by default.** The pitch waits as a draft under **Pitches**
  (`GET /link-pitches?status=draft`) for a person to read the opener, edit
  it and approve it (`POST /link-pitches/{id}/approve`) or reject it. Turn
  approval off per sequence (`PATCH /link-building/sequences/{id}` with
  `approve_pitches: false`, or in the settings) and pitches through it go
  out as soon as their opener is written, at most `auto_send_per_day` in
  24 hours (10 unless you change it, up to 200). Pitches past the limit
  wait as drafts (reason `daily_cap`) and go out on a later day, oldest
  first, unless you approve them sooner. Even with approval off, a pitch
  waits for a person when the AI writing its opener is unsure about it
  (reason `unsure:` and why: the page doesn't fit, it couldn't tell what
  the page is about, or the site looks spammy or sells links) or the site's
  spam score is 30 or more (`spammy_site`). `GET /link-building/sequences`
  shows each sequence's `auto_send_per_day` and `sent_today`. A pitch without AI waits for a
  person to write the opener. Pitches waiting show as a count on Link building
  in the sidebar and on the Pitches tab, in the workspace stats
  (`link_pitches_waiting`) and as a `link_pitches_waiting` item in the
  workspace status, so agents see them too.
- **Sending.** Approving makes the contact a lead from their person record,
  puts the opener on it (`custom.opener`, plus `custom.link_page`) and
  enrolls it in the sequence, whose own checks apply to every send: the
  email policy, suppressions, sending windows and mailbox limits. The
  prospect moves to `pitched`. One pitch per website still holds: approving
  answers `409 site_in_outreach` while another page there is in outreach or
  cooling down.
- **Replies.** Any reply moves the prospect to `replied`, then the AI reads
  it: agreed, declined, asks for payment, wants content first, or other.
  When it is sure, agreed moves the prospect to `agreed` and declined to
  `declined`. The reply gets an inbox tag (`link: agreed`, `link: wants
  payment`, and so on), and asks for payment or content are flagged for a
  person. Without AI, move the prospect yourself.
- **Live.** Every hour, agreed links due a look are checked (each at most
  daily, live ones weekly). A link found on the page moves the prospect to
  `live`. A live link gone from a page that is still up goes back on the
  list as a prospect to win back: `removed_at` is set, and `win_back`,
  `removed_at` and `live_since` go in its evidence. Nothing is sent until you
  or a playbook pitches it again, and its opener says the page used to link
  to you. A live link whose page is gone moves to `removed`. Either way,
  `link_prospect.removed` is recorded (with `win_back` when it went back on
  the list). An agreed link still not there after 14 days is announced once
  (`link_prospect.overdue`), a cue to nudge the site.
- **Results.** `GET /link-prospects/results` (MCP `get_link_results`)
  counts the prospects found in the last 90 days by method: pitched,
  replied, agreed, live, declined, removed, and live links per pitch. The
  Outcomes report grouped by sequence has `live_links` and
  `cost_per_live_link_cents`.

Pitches started by events, and an autopilot that pitches on its own, are
part of Outflare Cloud.

### Link reads

Who links to a page or domain that isn't yours: a competitor's page that
went dead, a competitor's most linked guide, a site you want links like.
Outflare reads it from your own Ahrefs or DataForSEO connection (`POST
/link-reads`, MCP `read_links`): one page when the target is a URL with a
path, else the whole domain. Each read costs money within the connection's
budget (about $0.02 plus $0.00003 a link on DataForSEO, about one API unit a
link on Ahrefs), so ask with `estimate_only` first to see the cost. A copy
under a day old is free. Without an SEO connection, it answers `409
connector_needed`.

Every read is kept, so these reports cost nothing:

- **Dead pages** (`GET /link-reads/dead-pages`, MCP `list_dead_pages`):
  pages gone from tracked competitors' sitemaps, with who links to them, the
  most linking domains first. Those sites now have a broken link and may
  link to your page instead.
- **Top pages** (`GET /link-reads/top-pages?target=rival.example`, MCP
  `list_top_linked_pages`): a domain's pages ranked by the domains linking
  to them, from its latest read. The counts cover the links that read kept.
  Only when you ask (**Read search traffic** on the page, or the `pages`
  report: `GET /seo/{id}/reports/pages?target=rival.example`, MCP
  `get_seo_report`), one paid read of your SEO connection adds each page's
  estimated visits a month, keywords and top search, so you can pick the
  page worth pitching. Nothing reads it on its own.

**The weekly read.** The weekly SEO snapshot also reads who links to pages
that went from tracked competitors' sitemaps in the last 8 days (at most 20
a site, each page once). A page whose read fails is read again soon, on its
own, rather than at next week's snapshot: after 15 minutes, then 1 and 2
hours, then every 4 hours, while it is still among the last 8 days' pages
and until a read of it works. It spends at most a share of the connection's
monthly budget a month on this: 10% by default. Change it, or turn it off,
in the brand's link building settings
(`PATCH /v1/brands/{brand}/link-building/settings`, MCP
`update_link_building_settings`), which also hold the pitch cool-down
(`pitch_cooldown_days`, 60 by default). Each brand has its own settings and
its own share of the budget. Your own lost links need no paid
read: your backlinks already name the page that dropped the link.

### Searching the web

`POST /v1/brands/{brand}/search-results` (MCP `search_web`) reads one
Google results page for any query, as you'd type it (operators such as
`intitle:` and `-site:` work), from your own DataForSEO or Geonode
connection: the one rank tracking uses, or `integration_id`. It costs
money within the connection's budget (about $0.02 a search on DataForSEO,
$0.005 on Geonode), so ask with `estimate_only` first. You get the organic
results, best first; only the cost is kept, in Outcomes. The search is
the brand's: its cost, and the approval when an agent's search waits for
a person, show in that brand on the Agents page and nowhere else.
Without a SERP connection, it answers `409 connector_needed`.

### Link building playbooks

One [playbook](../playbooks/README.md) per way of earning links, each
from find to pitch. Starting one starts an agent run; every paid search or
read shows its cost first, and every pitch waits for approval unless its
sequence skips it.

| Playbook | Finds | Needs |
| --- | --- | --- |
| `link-competitor-gaps` | Pages linking to your competitors but not to you | Ahrefs or DataForSEO |
| `link-broken-links` | Pages on your topic with a dead link your page can replace | DataForSEO or Geonode |
| `link-reclaim-lost-links` | Links you lost: redirects for links to your dead pages, and a note to sites that dropped one | Nothing paid |
| `link-competitor-dead-pages` | Sites linking to a competitor's deleted page | Ahrefs or DataForSEO |
| `link-resource-pages` | Resource lists on your topic | DataForSEO or Geonode |
| `link-promote-an-asset` | Sites linking to the pages that rank for your new page's topic | Both |
| `link-unlinked-mentions` | Pages that name your brand without a link | DataForSEO or Geonode |
| `link-partners-and-customers` | Partner and customer sites that don't link to you | Nothing paid |

Each needs your site in SEO and a mailbox to send from. Your AI model
reads pages and writes openers; without one, you write them.

#### Running them by themselves

Each link playbook can run on a schedule or start when something happens
([playbook automations](playbooks.md#running-one-by-itself)). You approve
it once, with its spend limits, and pitches still wait for your approval.
Each one suggests what suits it:

| Playbook | Suggested |
| --- | --- |
| `link-competitor-gaps` | Monthly, or on `competitor_link.new`, gathered daily |
| `link-broken-links` | Monthly, or on `outbound_link.broken`, gathered daily |
| `link-reclaim-lost-links` | Monthly, or on `backlink.lost` and `fix.found`, gathered daily |
| `link-competitor-dead-pages` | On `competitor.page_removed`, gathered daily |
| `link-resource-pages` | Monthly |
| `link-promote-an-asset` | On `asset.published`, a run per page |
| `link-unlinked-mentions` | Weekly, or on `mention.found`, gathered daily |
| `link-partners-and-customers` | Monthly, or on `partner.created`, gathered daily |

The events that start them:

- **`competitor_link.new`**: a domain is new among a tracked competitor's
  linking domains since its previous weekly read, and doesn't link to you.
  It compares reads the SEO snapshot already makes (the competitors' top
  domains, `gap_rows`), so it costs nothing more. A domain can be new to a
  competitor's top list rather than newly linking. `competitors` says how
  many of your tracked competitors it links to now.
- **`outbound_link.broken`**: a link prospect's page has a broken link it
  didn't have at its previous check (every broken link on its first
  check), with what the Internet Archive kept of it. Pages of broken-link
  prospects nobody has pitched are checked again every **Check
  broken-link pages again every** days (link building settings,
  `recheck_days`, 30 by default, 0 never). Checks are free, apart from your
  AI reading the page.
- **`asset.published`**: a page of yours worth promoting is marked
  published, on the Link building API (`POST .../link-building/assets`,
  MCP `publish_link_asset`) by a person or an agent, such as the one that
  publishes your content. Its site, and so its brand, is the SEO site it is
  on.
- **`mention.found`**: the news watch found an article naming your brand
  ([news watch](news-watch.md)).

## API

```sh
# SEO connections, their domains, budget and snapshot status
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/seo"

# Is each brand growing in search: free
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/seo/overview?brand=summitly"

# Stored snapshots: free, never calls the provider
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/history?report=overview&host=example.com"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/workspaces/$WS/seo/history?report=keywords&host=example.com&limit=50"

# Backlinks merged from both tools, and what changed: free
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/backlinks?status=live&order=rating"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/backlinks/events?kind=lost&days=30"

# Track a competitor, then read what changed at them
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"site_id": "'$SITE'", "host": "rival.example"}' "$URL/v1/brands/$BRAND/seo-competitors"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/seo-competitors/events?days=30"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/backlinks/summary"

# Who links to a competitor's page: estimate, then read; then the free reports
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"target": "https://rival.example/guide", "estimate_only": true}' "$URL/v1/brands/$BRAND/link-reads"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/link-reads/dead-pages?site_id=$SITE"

# Link prospects: add pages, list the best, pitch one
curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"site_id": "'$SITE'", "prospects": [{"page_url": "https://blog.example.org/best-tools", "method": "broken_link"}]}' \
  "$URL/v1/brands/$BRAND/link-prospects"
curl -H "Authorization: Bearer $TOKEN" "$URL/v1/brands/$BRAND/link-prospects?min_score=40"
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"stage": "pitched"}' "$URL/v1/brands/$BRAND/link-prospects/$PROSPECT"

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
