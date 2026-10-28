# News watch

The news watch reads the news for you. Save a search, and Outflare runs it on a schedule through your own web search connection and keeps every article it finds. A **topic** search follows a subject, such as "residential proxies", so you can see what is being written and who writes it. A **coverage** search looks for a brand, such as "Northpeak", and keeps each article about it as that brand's **coverage**.

It is the first part of digital PR in Outflare: finding a story only your brand can tell, getting it to the journalists who cover the topic, and counting the coverage it wins. Journalists as records, stories and pitching come next.

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

A new search runs straight away. **Run now** (`POST /v1/workspaces/{ws}/news-searches/{search}/run`) runs it again whatever its schedule. A workspace keeps up to 200 searches. A search's `last_error_code` says why its last run failed; it clears on the next run that works.

## Articles

Each run keeps up to 50 results. An article is kept once per workspace, however many searches find it, without its `#fragment` or tracking tags (`utm_*`, `fbclid` and the like), with the searches that found it (`GET /v1/workspaces/{ws}/news-articles`). Deleting a search deletes the articles only it found.

## Coverage

A coverage search keeps an article as the brand's coverage when its title or snippet names every word of the search, and it isn't on the brand's own website or one of its subdomains. Each item is on its outlet's company record (a prospect company when you have none), so everything you know about an outlet is in one place, and each one is recorded as a mention (`mention.found`).

New items wait for a person: **Confirm** or **Dismiss** (`PATCH /v1/workspaces/{ws}/coverage/{coverage}`). Add an article the watch missed with `POST /v1/workspaces/{ws}/coverage`. `linked` says whether the article links to the brand's site; it is empty until someone checks.

## Permissions

Reading searches, articles and coverage needs `seo.read`; saving, changing and running searches, and confirming or adding coverage, need `seo.manage`. A member limited to some brands sees and changes only their brands' searches and coverage, and the shared topics.

## Events

Saving, changing and deleting a search are `news_search.created`, `.updated` and `.deleted`. Each run is `news_search.ran`, with `results`, `kept`, `new_articles` and `new_coverage`, or `news_search.run_failed` with its error code. Each new coverage item is `mention.found`; confirming or dismissing it is `coverage.confirmed` or `coverage.dismissed`.

## Monitoring

The Grafana dashboard's **News watch** row shows runs by outcome, active searches and what they find, and the most overdue search. Alerts fire when a search is over an hour past its time, or when over half of runs fail with a provider or internal error ([monitoring](monitoring.md)).
