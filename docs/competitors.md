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
- **Score** (0 to 100) is the weighted mean, over the areas a brand is measured in, of its value divided by the area leader's value. A brand that leads every area scores 100. Each area counts once by default. `weights=search:2,ai:1` changes that (0 to 10), and the page keeps the choice in the browser.
- **Rank** and **rank 28 days ago** come from the same calculation on the values from 28 days earlier.
- **Standing** compares the brand with the median competitor in each area: leading (first), ahead (at least 10% above the median), level (within 10%), behind, or far behind (under a quarter of it). **Trend** is gaining or losing when the brand's share moved at least one point over 28 days, and steady otherwise.
- **Time to catch up** fits a line to the last 12 weeks of the brand's and a rival's values. It says when the brand passes the competitor just ahead, or when the one just behind passes it. It needs six weekly reads and is shown only within two years.

Search numbers all come from one SEO connection, so they share a scale: the one with the newest read of the brand's own site, Ahrefs first on a tie. They are the connection's estimates, read on the workspace's own account.

Each competitor has its own page (`/competition/{competitor id}` in the app). It shows its score and rank next to the brand's, every area head to head, and both shares over the year.

## A competitor's social profiles and review pages

Each competitor's page has **Their social profiles and reviews**, and the API has `GET`, `POST` and `PATCH /v1/brands/{brand}/competition/{competitor}/profiles` (MCP `list_competitor_profiles`, `follow_competitor_profile`, `answer_competitor_profile`).

- **Suggested from their website.** OutflareBot's weekly look at a tracked competitor's home page keeps the social profiles and review pages it links to as suggestions. Nothing is read until a person says yes (Follow) or no (Not theirs). A profile said no to is never suggested again.
- **Added by hand.** Any public profile Outflare can read (X, LinkedIn company pages, YouTube, Instagram, TikTok, Facebook, a subreddit, GitHub, Discord, Bluesky, Threads, Mastodon, DEV, Docker Hub) or a Trustpilot, G2, Capterra or Google review page.
- **Read daily, on the workspace's own account.** Followed profiles are read with the brand's own, by the workspace's page reader (Geonode), for followers (or the channel's headline count) and a review page's rating and review count. Without a page reader they are kept and not read, and the page says which connection to add.
- **Kept apart from the brand's own.** Competitors' accounts and pages never appear in Social or Reviews, the brand's numbers, publishing, alerts, fixes, reply drafts or queries. Only a review page's rating and count are kept, never its reviews.
- Only tracked competitors are followed. Dismissing or removing a competitor stops its reads.
