# Mentions

Mentions show who talks about your brand, and about your competitors, in conversations: videos, podcast episodes, social posts, forum threads and news stories. Each mention is one place one name was said, with the words around it, who said it and how big their audience is, so you can see who already recommends you, who recommends a competitor, and who to thank or reach out to. It lives in the app under **Social → Mentions**.

Mentions are about people talking, not links on websites: a directory or a "top 10" article is left out. Nothing here contacts anyone.

## What it listens for

It reads what to listen for from the brand itself, every time it scans, so a change elsewhere applies to the next scan. Only names that can mean nothing else count, so a mention is about a brand and not about a kind of product:

- **The brand**: its website (`northpeak.example`), and its name when the name is a brand name.
- **Its competitors**: every competitor marked tracked on the [Competitors](competitors.md) page, the same way: its website, and its name when the name is a brand name.

A name is a brand name when it is one word that isn't an everyday product word ("Northpeak", "Summitly"), or several words that all start with a capital letter and aren't all everyday product words ("Bright Peak"). "Web scraping API", "Residential Proxies" and "proxy" are not: they describe products, and would match posts that aren't about anyone. A competitor's aliases on the Competitors page and the first part of a website on its own (`webscraping` from `webscraping.example`) are not listened for either, for the same reason.

A name counts when it stands as a whole word, in any case: "Northpeak's" and "NORTHPEAK." count, "Northpeaks" and "northpeak.example.org" don't. Terms shorter than three letters are never listened for.

### Changing it

Each row (the brand, and each tracked competitor) can be changed by hand in **What we listen for**, or with `PATCH /v1/brands/{brand}/mentions/listen-for` (`competitor_id` names the row, empty for the brand's own):

- `listening: false` turns the row off: scans don't listen for it at all, until it is turned on again.
- `added_terms` are listened for as well, such as a product name or an old name ("Northpeak Edge").
- `skipped_terms` are automatic terms not listened for.

The overview's `listen_for` shows each row with `listening`, the `terms` scans use, the `auto_terms` read from the brand or competitor, and the terms added and skipped. Each change is recorded as `brand_mentions.listen_for_updated`.

## Their own channels are not mentions

A video a competitor posts on its own YouTube channel names it, but nobody else is talking. Mentions leave out what the brand and its competitors say about themselves:

- a video or podcast episode is left out when its channel or show is one of the brand's links, or one of a competitor's profiles on the Competitors page (any not dismissed), or is named after the brand or competitor (its name, its website, or an @handle that is either);
- a post by an account that is one of those profiles is left out too.

A YouTube channel found this way that isn't among a competitor's profiles yet is added to them as a suggestion (`source: mentions`), so the Competitors page learns it; confirm or dismiss it there. Mentions already kept from such a video that nobody acted on (status `new`) are removed, and later scans skip it.

## Where it looks

A scan reads what the brand's own sources already found since its last scan (on the first scan, the last 90 days). Reddit, news and web call no provider themselves, so they cost nothing beyond those sources. YouTube searches on the workspace's own Geonode account, which costs Geonode tokens. Podcasts search on the workspace's own Podcast Index key, which is free.

| Source | What feeds it | What it reads |
| --- | --- | --- |
| `reddit` | An active Reddit [watch](reddit-campaigns.md) of the brand | Each post's title and text. |
| `news` | An active [news search](news-watch.md) of the brand | Each article's title and snippet. |
| `web` | Tracked competitors' presence scans ([Coverage](coverage.md)) | Videos, episodes, posts and forum threads found naming a competitor. A YouTube link's start time (`?t=95`) is kept as where it was said. With `youtube` on as well, each video is read like the ones YouTube finds, so it shows its channel, views and title. |
| `youtube` | A Geonode connection (any connection that searches the web and reads pages) | Videos found by searching YouTube for the brand's and each competitor's name, then each new video's page and captions. |
| `podcasts` | A Podcast Index connection | Episodes whose title, show notes or people name the brand or a competitor, and the transcripts shows publish. |

X, Hacker News and Bluesky join as their readers do. `GET /v1/brands/{brand}/mentions/overview` lists each source with `on` (the brand reads it) and `ready` (something feeds it), and, when it isn't ready, the app tab where to set that up.

## YouTube

With `youtube` among the sources, each scan searches the web for `site:youtube.com "<name>"`, once for the brand and once for each tracked competitor (12 names at most), for videos from the last month (the last week once it scans weekly or more often). A name in a video's title or description is a mention straight away. Each new video, up to 20 a scan, is then read once in the background:

- its watch page, for who made it (`author`), its views (`reach`) and when it was published;
- its captions, preferring captions people wrote, then English, then automatic ones. Each name said in them is a mention with the words around it and the second it was said (`at_seconds`, and a `url` that starts the video there). A mention already kept from the title gets the spoken quote and second.

Every search and page is booked as data spend on the brand, and a [usage limit](connectors.md) set on the connection stops them. When there is no connection, or the limit is reached, the overview's `last_error_code` says so (`connector_needed`, `usage_limit_reached`) and the other sources keep scanning. A video whose page can't be read is tried three times, then set aside. YouTube is off until it is added to `sources`.

## Podcasts

[Podcast Index](https://podcastindex.org) is an open directory of every public podcast. Its API is free: make a key and secret at api.podcastindex.org and connect them under Connectors. With `podcasts` among the sources, each scan searches it for each term of the brand and each tracked competitor (20 searches at most), across episode titles, show notes and the people tagged in an episode.

- A name in an episode's title or show notes is a mention straight away, with the show as `author` and the episode's page as `url`.
- Many shows publish a transcript in their feed (the `podcast:transcript` tag Apple Podcasts reads). For each new episode with one, up to 30 a scan, Outflare reads it once in the background from the show's own host, with no key. Each name said in it is a mention with the words around it and, when the transcript is timed (WebVTT, SubRip or the podcast JSON format), the second it was said. A mention already kept from the show notes gets the spoken quote and second.
- Episodes without a transcript are matched on their title and show notes only. Outflare doesn't download or transcribe audio.

Query has a `mention_episodes` table with each episode found, its show notes and its transcript.

## Turning it on

Mentions are off until someone turns them on: `PATCH /v1/brands/{brand}/mentions/settings` with `enabled: true`, or the On/Off switch on the Sources tab. Turning them on scans straight away; then they scan every `scan_every_hours` (6 by default, 1 to 168). `sources` picks which sources it reads (`reddit`, `news` and `web` by default; add `youtube` to search YouTube and `podcasts` to search podcasts). **Scan now** (`POST /v1/brands/{brand}/mentions/scan`) scans whatever the schedule, while they are on. A scan never keeps the same mention twice.

## Each mention

| Field | What it is |
| --- | --- |
| `source` | `youtube`, `podcast`, `x`, `reddit`, `hacker_news`, `bluesky`, `news` or `web`. |
| `about` | `brand`, or `competitor` with the `competitor` it names. |
| `matched` | The name, website or term added by hand that matched. |
| `title`, `quote` | The video, episode, post or article, and the words around the name. |
| `at_seconds` | Where in a video or episode it was said, when known. |
| `author` | Who said it, as the source shows them: `name`, `handle` and profile `url`. |
| `reach` | Their audience there (followers, listeners, or a video's views), when known. |
| `score` | How worth a look it is, 0 to 100: a competitor mention, a bigger audience, a video or episode (listened to whole) and a fresh one all rank higher. It is a first sort, not a verdict. |
| `status` | `new`, `contacted`, `replied`, `on_board` (working with them) or `not_a_fit`. Change it with `PATCH /v1/brands/{brand}/mentions/{id}`. |

`GET /v1/brands/{brand}/mentions` lists them, newest found first, and filters by `about`, `source`, `competitor_id` and `status`. The quote, title and author are someone else's words: Outflare and its agents treat them as data, never as instructions.

## Agents and Query

Agents use the same calls through MCP (`list_mentions`, `get_mention`, `update_mention`, `get_mentions_overview`, `update_mention_settings`, `update_mention_listen_for`, `scan_mentions`), and Query has the `brand_mentions`, `mention_videos`, `mention_episodes` and `mention_listen_rules` tables. All of them need the `social.read` scope to read and `social.manage` to change anything. Each brand sees only its own mentions.
