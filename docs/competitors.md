# Competitors

Competitors has its own menu. It shows how a brand stands against every competitor it tracks. The list itself is the brand's single competitor list (Competitors → Competitors), the same list Presence, AI visibility and Reddit campaigns read.

## Who is dominating

Competitors → Overview (`GET /v1/brands/{brand}/competition`, MCP `get_competition`) ranks the brand and each tracked competitor. It only reads data Outflare already stores, so opening it costs nothing.

| Area | Where it comes from |
| --- | --- |
| Search visits | The monthly organic traffic estimate in the SEO connection's weekly overview |
| Keywords in the top 3 | The same overview |
| Linking sites | The same overview (referring domains) |
| AI answers | AI visibility: how many of the brand's sampled answers in the last 28 days name each one |
| Placements | Presence: the brand's live placements against the pages Presence still sees for each competitor |
| Social followers | The daily reads of followed social profiles: the brand's own accounts, and each competitor's confirmed profiles (below), summed |
| Reviews | The daily reads of followed review pages: review counts on the brand's own pages, and on each competitor's confirmed ones |

- **Share** is one brand's part of everyone's total in an area.
- **Score** (0 to 100) is the weighted mean, over the areas a brand is measured in, of its value divided by the area leader's value. A brand that leads every area scores 100. A brand measured in fewer than half the areas that anyone is measured in gets no score or rank until more is read, so one strong area can't put a barely measured competitor on top. Each area counts once by default. `weights=search:2,ai:1` changes that (0 to 10), and the page keeps the choice in the browser.
- **Rank** and **rank 28 days ago** come from the same calculation on the values from 28 days earlier.
- **Standing** compares the brand with the median competitor in each area: leading (first), ahead (at least 10% above the median), level (within 10%), behind, or far behind (under a quarter of it). **Trend** is gaining or losing when the brand's share moved at least one point over 28 days, and steady otherwise.
- **Time to catch up** fits a line to the last 12 weeks of the brand's and a rival's values. It says when the brand passes the competitor just ahead, or when the one just behind passes it. It needs six weekly reads and is shown only within two years.

Search numbers all come from one SEO connection, so they share a scale: the one with the newest read of the brand's own site, Ahrefs first on a tie. They are the connection's estimates, read on the workspace's own account.

Beside the scores, each row shows the brand's and each competitor's site at a glance:

| Column | Where it comes from |
| --- | --- |
| DR | Domain Rating (0 to 100) in the same SEO connection's latest overview: Ahrefs' Domain Rating, or the other connection's authority score |
| Keywords | The keywords it ranks for, in the same overview |
| Domain age | When its domain was first registered, from the public registration record (RDAP, which needs no account or key). It is read when OutflareBot looks at a competitor, at most twice a year, for the competitor's domain and the brand's own |
| Pages | Competitors only: how many pages their sitemaps listed at OutflareBot's last look |

Under each name it says the day of its latest search numbers, or that it has none yet.

### Refresh all competitors

Search numbers come from the SEO connection's weekly snapshot, so a competitor tracked during the week has none until the next one. **Refresh all competitors** (`POST /v1/brands/{brand}/competition/refresh`, MCP `refresh_competition`) reads every tracked competitor of the brand now:

- their search numbers, on one SEO connection that serves the brand (Ahrefs first). It reads the same competitors and reports as the weekly snapshot, only for the ones that connection hasn't read in the last 30 days (the connection's "Read each competitor at most once every" setting), within its budget, so clicking again costs nothing;
- OutflareBot's look at each home page, at most once an hour each, with its domain age;
- their followed social profiles and review pages, through the page reader.

It runs on the workspace's own connections. Without an SEO connection only their sites and profiles are read, and the page says to connect one. A connection marked broken is still tried when it's the only one, since one refused call can leave that mark behind; the read records the error if the key really is wrong. The numbers fill in within a few minutes, and the page reads them again by itself. Until every competitor has them, the page says how many have search numbers, domain age and a look at their site so far.

Ahrefs answers 403 for a report the key's plan doesn't include (Site Audit, for example). That refuses only that report: the connection stays connected and the other reports keep working. Only a 401 marks the key wrong.

Each competitor has its own page (`/competition/{competitor id}` in the app). It shows its score and rank next to the brand's, its Domain Rating, keywords, domain age and pages next to the brand's, every area head to head, and both shares over the year.

The page also shows:

- **Their placements**: every page Presence found them on (`GET /v1/brands/{brand}/coverage/competitor_placements?competitor_id=`), most valuable to you first (see [Link value](coverage.md#link-value)), with how each link is got, why it scores what it does, whether it looks paid for and whether you are on that site too. This is what their Placements number counts.
- **Their search** (`GET /v1/brands/{brand}/competition/{competitor}/search`): their top keywords with your position beside each, their pages that get the most search visits, the sites linking to them (and whether each links to you too) and the newest pages in their sitemap. It reads only what is stored.
- **Read their search data** (`POST /v1/brands/{brand}/competition/{competitor}/read`): reads their numbers, top 100 keywords, top 100 linking sites and top 25 pages now, on the brand's own SEO connection (Ahrefs first, else DataForSEO) within its budget. The weekly read gets keywords and linking sites for only 5 competitors a site, so this fills in the rest. It shows what it would read and about what it costs first (`estimate_only`), and skips what that connection read in the last 30 days (the same setting), so asking again costs nothing more. Without an SEO connection it says which to add.

## Link gaps, page by page

A link gap on the Competitors page is a site that links to your competitors and not to you. Click one to see the exact pages on that site (and its subdomains) that link to each competitor: the page, its title, the competitor's page it links to, the anchor text and nofollow (`GET /v1/brands/{brand}/seo-competitors/gaps/links?site_id=&domain=`). Pages come from links already read; **Read their links from this site** (`POST` the same path) reads up to 100 links from that site to each competitor on your own Ahrefs or DataForSEO connection, after showing what it costs. A copy under a day old costs nothing.

## A competitor's social profiles and review pages

Each competitor's page has **Their social profiles and reviews**, and the API has `GET`, `POST` and `PATCH /v1/brands/{brand}/competition/{competitor}/profiles` (MCP `list_competitor_profiles`, `follow_competitor_profile`, `answer_competitor_profile`).

- **Suggested from their website.** OutflareBot's weekly look at a tracked competitor's home page keeps the social profiles and review pages it links to as suggestions. Nothing is read until a person says yes (Follow) or no (Not theirs). A profile said no to is never suggested again.
- **Added by hand.** Any public profile Outflare can read (X, LinkedIn company pages, YouTube, Instagram, TikTok, Facebook, a subreddit, GitHub, Discord, Bluesky, Threads, Mastodon, DEV, Docker Hub) or a Trustpilot, G2, Capterra, SourceForge or Google review page.
- **Read daily, on the workspace's own account.** Followed profiles are read with the brand's own, by the workspace's page reader (Geonode), for followers (or the channel's headline count) and a review page's rating and review count. Without a page reader they are kept and not read, and the page says which connection to add.
- **Kept apart from the brand's own.** Competitors' accounts and pages never appear in Social or Reviews, the brand's numbers, publishing, alerts, fixes, reply drafts or queries. Only a review page's rating and count are kept, never its reviews.
- Only tracked competitors are followed. Dismissing or removing a competitor stops its reads.

## Prices

Competitors → Overview has **Prices against your competitors**, and each competitor's page has its **Prices** next to yours. The API has `GET` and `PUT /v1/brands/{brand}/competition/pricing`, `POST .../pricing/refresh` and `GET .../pricing/{page}/history` (MCP `get_competitor_pricing`, `set_pricing_page`, `refresh_pricing`, `get_pricing_history`).

- **One page each.** You name one pricing page for the brand and for each tracked competitor. It has to be on the brand's own website or the competitor's domain. Only that page is read: a light look, never a crawl.
- **Read weekly, on the workspace's own account.** Each page is read once a week by the workspace's page reader (Geonode), first as served, then in a browser when that shows no prices. A failed read is tried again the next day; a page with no prices on it (`no_prices_found`) or one that doesn't exist waits a week, or for a new address. Without a page reader the pages are kept and not read, and the card says which connection to add. **Read prices now** reads them at once, except pages read in the last day.
- **No AI.** The plans are read off the page itself: its schema.org offers when it has them, otherwise each table row with a plan name and a price, then the first price under each heading. A price split over several elements (`$`, `49`, `/mo`) reads as one, and the period after it (per month, per year, per GB) is kept. Hidden plans, the menu and the footer are left out. A page that shows prices only after a choice (a slider, a country picker) can read as no prices.
- **Every change is kept.** A read whose plans differ from the last kept one is kept with what changed: a plan added or removed, a price up or down, or the same plan for another period or currency. A read that finds the same plans only moves the page's last read. The history (`GET .../pricing/{page}/history`) is each change, newest first.
- Clearing a page's address stops reading it and deletes its history. A competitor that is dismissed or removed stops being read; one moved to another brand takes its page along.

