# News watch

The news watch reads the news for you. Save a search, and Outflare runs it on a schedule through your own web search connection and keeps every article it finds. A **topic** search follows a subject, such as "residential proxies", so you can see what is being written and who writes it. A **coverage** search looks for a brand, such as "Northpeak", and keeps each article about it as that brand's **coverage**.

It is the first part of digital PR in Outflare: finding a story only your brand can tell, getting it to the journalists who cover the topic, and counting the coverage it wins. It also finds the journalists who write the articles and makes media lists of them. [Stories](stories.md) turn what you find into a release with sourced facts and approved quotes; pitching comes next.

## What it needs

A web search connection: [Geonode](connectors.md). Each run is one search on your own Geonode account, booked in the spend ledger and kept within the usage limits you set on the connection (Connectors → Geonode → Usage). With no web search connection, searches wait and say `connector_needed`; with the connection at its limit, they say `usage_limit_reached` and try again at their next time.

## Searches

| Field | What it does |
| --- | --- |
| `query` | What is searched, as you would type it. For a coverage search, use the brand's name. |
| `purpose` | `topic` (default) or `coverage`. A coverage search needs a brand. |
| `brand_id` | The brand it is for. Leave it empty for a topic every brand shares. |
| `time_range` | How far back each run looks: `day`, `week` (default) or `month`. |
| `check_every_hours` | How often it runs, 6 to 168 hours; 24 by default. |
| `locale` | A language code (`en`) or tag (`en-GB`), or empty for the provider's default. |
| `status` | `active`, or `paused` to keep it and stop running it. |

A new search runs straight away. **Run now** (`POST /v1/workspaces/{ws}/news-searches/{search}/run`) runs it again whatever its schedule. A workspace keeps up to 200 searches. A search's `last_error_code` says why its last run failed; it clears on the next run that works. A failed run is tried again soon, before the search's next time: after 15 minutes, then 1 and 2 hours, then every 4 hours. A search with no web search connection, or one whose key was refused, waits for its schedule, since only you can fix that.

## Articles

Each run keeps up to 50 results. An article is kept once per workspace, however many searches find it, without its `#fragment` or tracking tags (`utm_*`, `fbclid` and the like), with the searches that found it (`GET /v1/workspaces/{ws}/news-articles`). Deleting a search deletes the articles only it found.

## Coverage

A coverage search keeps an article as the brand's coverage when its title or snippet names every word of the search, and it isn't on the brand's own website or one of its subdomains. Each item is on its outlet's company record (a prospect company when you have none), so everything you know about an outlet is in one place, and each one is recorded as a mention (`mention.found`).

New items wait for a person: **Confirm** or **Dismiss** (`PATCH /v1/workspaces/{ws}/coverage/{coverage}`). Add an article the watch missed with `POST /v1/workspaces/{ws}/coverage`. `linked` says whether the article links to the brand's site; it is empty until someone checks.

## Journalists

OutflareBot reads each new article once, up to 20 from each run, for its byline and the date it was published: from the page's author tags or its structured data (JSON-LD). It obeys robots.txt and sites that opted out of crawling, and costs nothing. The byline shows on the article as the page gives it.

When the byline is one person's name, the free email finder looks for their address at the outlet's own domain. When it is fairly sure (a confidence of 40 or more), the author becomes a **journalist**: a person record on the outlet's company record, like any contact, with the articles they wrote. Outflare never buys journalist data; a paid database can be connected later on your own account.

The **Journalists** list (`GET /v1/workspaces/{ws}/journalists`, MCP `list_journalists`) shows each one with how many articles they wrote, their latest, their outlets and the searches their articles came up in, most articles first. Narrow it to one search to see who covers that topic. `GET /v1/workspaces/{ws}/news-articles?journalist_id=` lists what one journalist wrote.

### Media lists

A media list is a [smart list](lists.md#smart-lists) with a news search rule: "wrote an article this search found", ever or in the last so many days. It keeps itself up to date as the news watch finds new articles, and can be used like any other list. **Make a media list** on the Journalists tab creates one for a search; or create it through the API with the rule `{"news_search": "<search id>", "op": "exists", "within_days": 90}`.

## Permissions

Reading searches, articles and coverage needs `seo.read`; saving, changing and running searches, and confirming or adding coverage, need `seo.manage`. A member limited to some brands sees and changes only their brands' searches and coverage, and the shared topics.

## Events

Saving, changing and deleting a search are `news_search.created`, `.updated` and `.deleted`. Each run is `news_search.ran`, with `results`, `kept`, `new_articles` and `new_coverage`, or `news_search.run_failed` with its error code. Each new coverage item is `mention.found`; confirming or dismissing it is `coverage.confirmed` or `coverage.dismissed`. Each article read is `news_article.read`, with its `status` and whether it found a byline and a journalist, and each new journalist is `journalist.found`.

## Monitoring

The Grafana dashboard's **News watch** row shows runs by outcome, active searches and what they find, the most overdue search, and article reads. Alerts fire when a search is over an hour past its time, when over half of runs fail with a provider or internal error, or when over half of article reads can't reach the page ([monitoring](monitoring.md)).
