# Query

Query runs read-only SQL over one brand's data, so a question such as "win rate by source this quarter" or "people who visited pricing and have no deal" gets an exact answer in one call, instead of being paged together from lists. People and agents use the same tool, and it sees exactly what the caller may see through the API.

```sh
curl -X POST "$OUTFLARE_URL/v1/brands/$BRAND/query" \
  -H "Authorization: Bearer $OUTFLARE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"sql": "select stage, count(*), sum(amount_cents) from deals group by stage order by 2 desc"}'
```

```json
{
  "columns": [{"name": "stage", "type": "text"}, {"name": "count", "type": "int8"}, {"name": "sum", "type": "numeric"}],
  "rows": [["qualified", 12, 4800000], ["won", 5, 2100000]],
  "row_count": 2, "truncated": false, "duration_ms": 14
}
```

`$BRAND` is the brand's id. Agents use the MCP tools `describe_brand_data`, `list_query_tables` and `run_query`, which take the brand's handle or id.

A query belongs to one brand (`plans/brands/README.md`): different brands are different businesses, so their data is never queried together. The workspace routes `GET /v1/brands/{brand}/query/tables` and `POST /v1/brands/{brand}/query` are deprecated and stay until the brands contract step: they query the brand `?brand=` picks (a handle or id), else the workspace's first brand (a member or token kept to some brands gets its own), and `?brand=all` queries every brand you can see, as before.

## Tables

`GET /v1/brands/{brand}/query/tables` (MCP `list_query_tables`) lists the tables with their columns, the read scope each needs, and whether you hold it.

| Table | Scope | What it holds |
| --- | --- | --- |
| `people`, `companies` | `customers.read` | Customer records of each type, without merged or archived ones, with their main email or domain |
| `records` | `customers.read` | Every record of every type, including merged (`merged_into`) and archived ones |
| `record_identities`, `record_relationships` | `customers.read` | Emails, phones, ids and domains; links between records |
| `customer_events`, `consents`, `subscriptions` | `customers.read` | Product events, the consent log, payment subscriptions (`monthly_minor` is MRR in minor units) |
| `rules`, `rule_firings` | `customers.read` | The brand's [rules](rules.md), and each time one fired for a record |
| `products`, `lifecycle_states`, `lifecycle_moves`, `lifecycle_overall` | `customers.read` | The brand's [products](product-lifecycle.md), where each company and person stands with each, every move between stages, and each company's overall status |
| `transactions` | `customers.read` | [Payments, refunds, chargebacks and credits](transactions.md), in their currency and the brand's |
| `smart_list_changes` | `customers.read` | Who joined and left each [smart or static list](lists.md#history), and when |
| `metrics`, `metric_values` | `customers.read` | The brand's [live metrics](metrics.md), and each company's and person's value, overall and per product |
| `bulk_edits` | `customers.read` | [Bulk edits](bulk-edits.md) owners ran, what they changed and how many records |
| `journeys`, `journey_runs` | `customers.read` | The brand's journeys (`steps` counts the live version's steps), and each person's way through one |
| `brands` | `workspace.read` | The brand itself |
| `leads` | `leads.read` | The brand's leads for outreach |
| `sequences`, `enrollments`, `messages` | `sequences.read` | Sequences, who is in them, and the emails they sent (without bodies) |
| `replies` | `inbox.read` | Mail received, with its AI label, without bodies |
| `deals`, `meetings`, `spend` | `outcomes.read` | Outcomes and money spent |
| `emails`, `email_events`, `broadcasts` | `emails.read` | Lifecycle email, without bodies or addresses |
| `visits` | `analytics.read` | Website visits from the Outflare site snippet, with where each came from (`country`, `region`, `city`, and the network: `asn` and `isp`) and the device (`device`, `device_brand`, `device_model`, `os`, `os_version`, `browser`, `browser_version`, `language`) |
| `social_posts` | `social.read` | Posts on the workspace's own social accounts |
| `reviews`, `review_page_days` | `social.read` | [Reviews](reviews.md) of the brands on Trustpilot, G2, Capterra, SourceForge and Google, and each review page's numbers day by day |
| `backlinks`, `link_prospects` | `seo.read` | [Link building](seo.md): links from other sites to the brands' websites, live or lost, with each linking domain's strength, traffic and spam score; and the pages that could link, with how each was found (`method`), its score and where its pitch stands (`stage`), such as `select method, count(*) filter (where live_at is not null) from link_prospects group by 1` |
| `coverage_donor_profiles`, `coverage_link_orders` | `coverage.read` | [Paid link orders](link-orders.md): the brand's donor catalog (domain, PBN flag, niches and stored metrics, without notes) and each order's status, donor price (minor units and currency), live-check result and Coverage placement, without notes or `lost_reason`. Amounts in USD and EUR stay in their own currency, such as `select currency, status, count(*), sum(donor_amount_minor) from coverage_link_orders group by 1, 2` |
| `ad_accounts`, `ad_entities`, `ad_metrics_daily`, `ad_conversion_actions_daily` | `analytics.read` | [Paid ads](paid-media.md) from the brand's connected ad accounts: each account, its campaigns, ad groups and ads (`level`), and their daily spend, impressions, clicks and the network's conversions, overall and per conversion action. Money from the networks is in micros (millionths) of `currency`, so `select day, sum(spend_micros) / 1e6 from ad_metrics_daily where level = 'campaign' group by 1`. Sum one `level` only |
| `ad_keywords`, `ad_keyword_metrics_daily`, `ad_search_terms_daily`, `ad_clicks` | `analytics.read` | Search ads: keywords with quality score and bid estimates, their daily numbers, what people searched, and single clicks with their click id |
| `ad_acquisitions`, `ad_revenue` | `customers.read` (acquisitions), `analytics.read` | The ad click that brought each person in, with when they signed up and first paid, and every payment and refund credited to it: join spend to customers and revenue by campaign |
| `ad_changes`, `ad_conversion_sends`, `ad_audiences`, `ad_brand_economics` | `analytics.read` | Pauses and budget changes Outflare made, conversions sent back to the networks (without match keys), lists synced as audiences, and the brand's ad targets |
| `ad_match_quality` | `analytics.read` | What an ad network says about how well the conversions sent to it match its accounts: a score out of 10 per pixel, event and day, the share of events carrying each identifier, and what to fix |
| `ad_holdouts` | `analytics.read` | Holdout tests for paid ads: the countries a brand kept its ads out of and when. The results (expected sign-ups and lift) come from the API |
| `attribution_conversions`, `attribution_credits`, `attribution_models` | `analytics.read` | Conversions, and each one's credit shared over the touches before it per attribution model (`weight` adds up to 1) |
| `tracking_campaigns`, `tracking_links`, `tracking_link_clicks`, `tracking_destination_days` | `analytics.read` | Tracking campaigns and their redirect links, and each click on them with device, place and bot flag (no address kept) |
| `tracker_traffic_sources`, `tracker_offers`, `tracker_offer_days`, `tracker_landers`, `tracker_lander_clicks`, `tracker_flows`, `tracker_conversions`, `tracker_network_costs` | `analytics.read` | The click tracker: traffic sources (without postback addresses or tokens), offers, landers, flows, postback conversions with payouts, and what each network campaign spent per day |
| `search_daily`, `search_segments`, `search_sitemaps`, `url_inspections`, `search_vitals`, `search_ga4_outcomes`, `search_algorithm_updates`, `indexability_snapshots` | `seo.read` | Google Search Console clicks, impressions and position by page and query (and by country and device), sitemaps, URL inspections, Core Web Vitals, Google Analytics outcomes by landing page, Google updates and indexing totals by day |
| `seo_sites`, `crawl_runs`, `crawl_pages`, `crawl_issues`, `crawl_links`, `crawl_ai_bots`, `seo_regressions`, `page_targets` | `seo.read` | The brand's sites (without verification tokens), every crawl and the pages, issues and links it found, which AI crawlers robots.txt lets in, drops Outflare detected, and the keyword each page targets |
| `tracked_keywords`, `rank_observations`, `keyword_serps`, `seo_snapshots`, `backlink_events`, `link_pitches` | `seo.read`; `link_pitches`: `sequences.read` | Rank tracking (each check's position and top results), full results pages, reports from SEO data connections, backlink changes and link-building pitches |
| `seo_competitors`, `competitor_pages`, `competitor_events` | `seo.read` | Competitors, the pages in their sitemaps with when each appeared, and what they did |
| `ai_cohorts`, `ai_prompts`, `ai_runs`, `ai_samples`, `ai_mentions`, `ai_citations`, `ai_claims`, `ai_named_brands`, `ai_brand_facts`, `ai_fact_checks`, `ai_benchmarks` | `seo.read` | AI visibility: the prompts asked, each AI answer (with the model and whether it searched the web), the brands it named and with what stance, the pages it cited, its claims, and whether it got the brand's facts right |
| `ai_bot_hits`, `ai_bot_reports`, `ai_page_checks` | `seo.read` | AI crawler requests to the brand's sites per day, and checks of how an AI crawler sees a page |
| `analytics_daily`, `page_vitals`, `heatmap_clicks`, `heatmap_scrolls`, `site_visitors`, `experiments`, `experiment_pages` | `analytics.read`; heatmaps and visitors: `customers.read`; experiments: `seo.read` | Numbers from connected web analytics, real visitors' page speed, click and scroll heatmaps, each browser seen on the site with its company once known, and website experiments with their pages and results |
| `session_recordings` | `customers.read` | Recorded visits: when, pages, device and size, without the recording itself or deleted ones |
| `attribute_changes`, `record_merges`, `field_definitions`, `option_sets`, `record_types` | `customers.read` | Every record's full field history (old and new value, who, from where and when), merges, and the record catalog: fields, option lists and the brand's own record types |
| `smart_lists`, `smart_list_members`, `static_list_members`, `ideal_customer_profiles` | `customers.read`; profiles: `profiles.read` | Lists with their rules and who is in each now, and the brand's ideal customer profiles |
| `score_definitions`, `score_history`, `signals`, `signal_types` | `customers.read` | Scores and each record's value and band per day; signals (buying intent, churn risk) with their owner and outcome |
| `sales_leads`, `sales_messages`, `sales_events`, `sales_members`, `sales_quotas`, `sales_templates`, `sales_alert_settings`, `commission_plans`, `commission_statements` | `customers.read` | Sales: leads with their owners, state and amount, messages and every event on them (without content), the team, quotas and commissions |
| `crm_activities`, `crm_calendar_events`, `crm_mail_messages`, `crm_mail_sends` | `customers.read` | Tasks and calls on records (without note text), and meetings and emails with customers from the team's calendars and mailboxes (without content) |
| `revenue_days`, `revenue_customers`, `revenue_forecast_history`, `product_aliases` | `customers.read` | MRR per day with new, expansion, contraction and churned MRR, each customer's MRR, each week's quarterly forecast, and the names products go by |
| `support_conversations`, `support_messages`, `support_assistant_runs`, `conversation_sentiments` | `inbox.read`; sentiments: `customers.read` | Support: conversations with response and resolution times, SLA breaches and ratings, messages (without bodies), what the AI agent did, and each conversation's sentiment |
| `support_sla_policies`, `help_centre_days`, `help_article_days` | `inbox.read` (targets), `workspace.read` | Support targets, and help centre views, searches that found nothing and article votes per day |
| `partner_programs`, `partner_memberships`, `partner_groups`, `partner_clicks`, `partner_referrals`, `partner_conversions`, `partner_rewards`, `partner_payouts`, `partner_promo_codes` | `partners.read` | Partner and affiliate programs, partners, clicks, referrals, credited sales, rewards and payouts, without postback secrets, payout tokens or payout details |
| `spend_recurring` | `outcomes.read` | Recurring costs the brand entered |
| `lead_lists` | `leads.read` | Lead lists and how they fill |
| `agent_runs`, `agent_run_updates`, `brand_findings` | `agents.read`; findings: `workspace.read` | Agent runs with their goal, plan, status and report, what happened in each, and what agents learned about the brand |
| `brand_profile_facts`, `brand_personas`, `brand_profile_changes` | `workspace.read` | The brand profile: what agents know about the company as facts a person confirms, the personas, and every change to either |
| `missions`, `mission_updates`, `mission_actions`, `memories` | `missions.read`; memories: `workspace.read` | Missions with their goal, limits, status and report, their updates, each API call an agent asked to make (without request bodies or results), and what agents were told to remember |
| `actions` | `actions.read` | The action log: every agent action held for approval or run, how it was decided and the status it returned (without request bodies or results) |
| `ab_tests`, `ab_test_checks`, `insights`, `alerts` | `analytics.read`; alerts: `workspace.read` | A/B tests with their goals, decision, result and what was learned; saved insights; alerts |
| `journey_versions`, `journey_run_steps`, `lifecycle_stages` | `emails.read` (journeys), `customers.read` | Every version of every journey, each step people went through with its outcome, and the brand's lifecycle stages |
| `social_accounts`, `social_account_days`, `social_publications`, `social_post_snapshots` | `social.read` | The brand's and competitors' social accounts with followers by day, posts drafted and published from Outflare, and each post's numbers over time |
| `content_items`, `content_publishes` | `content.read` | The brand's articles and pages, and each publish to its CMS |
| `brand_assets`, `brand_generations`, `brand_videos`, `brand_video_steps`, `brand_video_versions`, `creatives`, `creative_skills`, `creative_skill_versions`, `creative_skill_drafts`, `creative_lessons`, `creative_history` | `content.read` | The creative studio: the brand's files, requests to its picture, video and voice providers, talking videos with their stages and versions, creatives and their skills, lessons, and the history of each creative |
| `email_templates`, `email_topics`, `email_broadcast_recipients`, `suppressions` | `emails.read`, `leads.read` | Email templates and topics, who each broadcast reached (without addresses), and addresses that must not be emailed (`leads.read`) |

jsonb columns such as `attributes` are read with `->>`: `select attributes->>'plan' as plan, count(*) from people group by 1`.

### A table per record type

Each of the brand's own record types is a table too, named by its key: a `subscription` type is `select plan, seats from subscription`, and the default `deal` type is `deal` (beside `deals`, the outcomes table). A type's table has `id`, `brand_id`, `name`, one column per active field (numbers and ratings as `numeric`, checkboxes as `boolean`, currencies, locations and multi-value fields as `jsonb`, the rest as `text`; a value of another kind reads as null), then `attributes`, `origin`, `created_at` and `updated_at`, without merged or archived records. It needs `customers.read`, and `GET .../query/tables` lists it with its columns. A type whose key another table already has (such as `people`) isn't added, and `person` and `account` are `people` and `companies`.

## Data catalog

The data catalog says what a brand's data holds before anyone writes SQL. `GET /v1/brands/{brand}/data/catalog` (MCP `describe_brand_data`, in the app Data → Catalog) lists every table above, grouped by area (customers, revenue, ads, analytics, outreach, email, seo, coverage, social, workspace), with what a row is, where its rows come from, the scope it needs and whether you hold it, its time column, example questions with their SQL, and, read live from the data, how many rows it holds and the first and last date (how fresh it is).

`GET /v1/brands/{brand}/data/catalog/{table}` (MCP `describe_brand_data` with `table`) explains every column: what it means, its unit (amounts ending `_minor` or `_cents` are minor units of the currency column named; times are UTC), what an id joins to, whether it holds personal data, the share of rows that fill it, and the most common values of columns such as `status`, `source` or `channel` (read from up to 100,000 rows).

The numbers are read with your own access, through the same role, brand and scopes as a query, so the catalog never shows more than a query could; a table you can't read is described without numbers. They are kept for up to five minutes; `?fresh=true` reads them again. Counting every table of a large brand can take a few seconds: `?numbers=false` answers at once with the descriptions and no numbers (unless a kept copy exists), which is how the app shows the list before the numbers arrive.

Every table that holds a brand's data is meant to be in the catalog (AGENTS.md, "Data: agents can see all of it"): a new view comes with its description in `engine/internal/api/query_catalog.go`, and a test fails on a column without one. Tables not opened to SQL yet are listed in `engine/internal/db/querytables/pending.txt`, and the few that never are (secrets, sign-in material, machinery) in `querytables/private.txt`, each with its reason.

## What a query can see

- **This workspace only.** Every table shows only the workspace's own rows.
- **One brand.** Every customer table (`records`, `people`, `companies`, the type tables, `record_identities`, `record_relationships`, `customer_events`, `consents`, `subscriptions`) and `brands` hold only rows whose own `brand_id` is the query's brand; a row with no brand is not the brand's and is left out too. No SQL text changes that: a `brand_id` filter, a subquery, a CTE or a join only ever sees the brand's rows, and the tables underneath can't be read at all. Another brand's token or a member kept to other brands gets `404` for the brand's query routes.
- **Outreach and activation.** `leads`, `sequences`, `enrollments`, `messages`, `replies`, `meetings` and `deals` (outreach, brands step 5), and the rule, journey and email tables (step 4), hold only rows whose own `brand_id` is the query's brand.
- **Areas not yet moved to brands.** The website, social and spend tables keep the rule they had for a caller kept to some brands: the brand's rows and the ones no brand owns. Each area becomes strict as it moves (`plans/brands/README.md`, "Order").
- **Your scopes.** Each table needs its read scope. A query naming a table you can't read is refused with `forbidden`, naming the scope.

## What a query can do

- One `SELECT` (or `WITH ... SELECT`). Anything else, or a second statement, fails with `query_invalid` and the database's own message.
- Nothing is ever changed: the query runs in a read-only transaction that is always rolled back, so an agent kept to missions may run it outside one.
- Up to `limit` rows come back (default 100, at most 1,000, and at most 4 MB); `truncated` says there were more.
- A query running over 15 seconds is stopped with `query_timeout`. Count and group rather than list.

## How it is kept safe

Query runs as a database role of its own, one per workspace, that can read only the views in the `query` schema (migration `0199`) and no table. Each view filters by the workspace, brand and scopes of the request, which the server writes for the query's database connection just before it runs and removes after (the brand since migration `0342`). The role can neither read nor change that, and the views are security barriers, so the query's own conditions never see a row before the view's filter has dropped it. The type tables are temporary views the query's own connection makes just before the query, over `query.records`, so they hold no more than it does; the query's transaction is read-only, so it can't make or change one, and they go when the connection closes. Every query opens a fresh connection that is closed afterwards, so nothing a query sets lasts. Because each workspace has its own role, the database never shows one workspace's running query to another. Functions that change settings, take locks, signal other sessions or run SQL from a string are refused too. That gives a clear message, but the boundary is the role and the read-only transaction.

**Self-hosting:** Query needs the database user in `DATABASE_URL` to be able to create roles (`CREATEROLE`, or a superuser, as with the Docker Compose and most managed Postgres). The role's password is derived from `OUTFLARE_SECRET_KEY`, and the role logs in with the same host and options as `DATABASE_URL`, so `pg_hba.conf` must allow password logins for it. Without that, Query answers `query_unavailable` and the log says why. Each server runs at most 3 queries at once (`OUTFLARE_QUERY_CONNECTIONS`, 0 turns Query off), each on its own connection, so allow for that many more connections per API server.

## Queries in the app

Queries sit in the **Data** menu, with **Endpoints** (published queries) and **Customer data** (the People list). A query belongs to one brand, so pick a brand in the switcher first. The Queries page lists your saved queries (yours, and those shared with the brand) as a table; each opens on a page of its own (`/query/<id>`, a new one at `/query/new`) with explicit **Save** and **Cancel**, a **Not saved** badge while it has changes, and a menu to make it a list, download it, see its versions, share it, publish it or archive it.

### Describe it in English

Type what you want to see, such as "customers who spent over $500 in total: their email, user id, sign-up date and lifetime spend, biggest spenders first", and press **Build it**. The workspace's own AI model (its pick for **Query questions** under AI settings; there is no Outflare key, and without one the page says which connection to add) answers with a builder, or with SQL when a builder can't say it (a grouped report, for example), or says what data is missing. On an open query, a description changes the query you have ("also show their country"). The model sees the brand's table and column names, its person and company fields, its identity kinds, its payment kinds and its most-used event names, never a customer's data. The engine checks every answer before you see it: it compiles the builder, then runs the SQL once on one row as you, with your scopes. An answer it refuses goes back to the model with the reason, up to three times.

| | |
| --- | --- |
| `POST /v1/brands/{brand}/query/ask` | Describe a query (`question`, up to 2,000 characters; optional `sql` or `builder` to change). Answers `202` with the question, `status: queued`. Costs one AI call on the workspace's own key, within its AI budget |
| `GET /v1/brands/{brand}/query/ask/{question}` | The answer once `status` is `ready`: `kind` (`builder`, `sql`, or empty when the data can't answer it, with `explanation` saying why), `name`, `builder`, `sql` and `explanation`. Only the person who asked reads it |

Each description is `query_question.asked` and `query_question.answered` in the activity log (with the question's id, the kind and the number of tries, never its text), and a failure is `query_question.failed` with its error code.

### The builder

The builder makes a query without SQL, one row per row of a table: **Start from** a table (People, Companies, Deals and the rest), add **Columns** (a column of the table, a person or company field, an identity such as a user id, or a total from a related table, such as the sum of a person's payments), each with its own label, **Filters** on any column (equals, contains, more than, in the last N days, is set and so on, matching all or any), an **Order** and **How many rows**. A total of an amount in cents can be shown as money (divided by 100). On a wide screen the builder sits beside the results, which follow it as you change it: dates read as dates, amounts in cents as money in the brand's currency, empty values as a dash, and you can sort, search and pick the columns shown. A column of cents (`_minor`, `_cents`) shows as money by itself. The **SQL** tab shows the SQL it writes, to copy; **Edit the SQL by hand** turns the builder off for that query, since the builder can't read SQL changed by hand.

A query that starts from People or Companies also returns `record_id`, which links each row to the person or company and lets the query become a list: **Make it a list** makes a dynamic list of everyone it returns (`kind: query`, see Lists), kept up to date.

`POST /v1/brands/{brand}/query/build` with `{"builder": {...}}` answers the SQL a builder writes, or `400 query_builder_invalid` saying what to fix. Totals from related tables are subqueries, so they never multiply rows; values are quoted, never pasted into the SQL. A saved query keeps its `builder` (and the `question` it was described with) with each version; saving a `builder` writes its SQL, and saving `sql` without one saves SQL only.

### SQL

The SQL tab highlights Postgres SQL and completes table and column names; Cmd/Ctrl+Enter runs the query. Beside it are the tables with their columns (from `GET .../query/tables`, searchable; a click inserts a name at the cursor) and the brand's catalog: each record type with its fields, which are its table's columns. Results come back as a table, with the row count, the time it took and a notice when there were more rows than shown (the page asks for up to 1,000), or as a chart: bars, lines or areas over one column along the bottom and up to eight number columns. Anyone who can run queries can download the results as CSV.

### Saved queries

Saving needs `customers.manage`; reading and running them `customers.read`, the owner's rule in `plans/brands/README.md`: anyone with `customers.read` runs queries over the brands they can see, limited by their scopes, and saving and sharing queries needs `customers.manage`.

- A saved query belongs to one brand and is **private** to the person who saved it (for an API token, the person who made the token) until they **share** it with the brand (`visibility: brand`). Then everyone who can query the brand sees and runs it, and anyone with `customers.manage` can change it. Only the person who saved it shares or unshares it. Another person's private query is `404`, as is another brand's.
- **Versions.** Every change to the SQL, the builder, the description or the chart is a new version, with an optional note; renaming it or changing its description isn't. Every version is kept: open one, run it, or restore it, which makes a new version from the old one (`restored_from` says which), so the history is never rewritten.
- **Running** a saved query runs exactly what the ad hoc query runs, through the same role, limits and scopes: its SQL is checked against what you may read when it runs, not when it was saved, and the brand comes from the route, so a saved query never reads another brand whatever its SQL says.
- **Archiving** takes a query out of the list; it can't be changed or run, and its versions stay. You archive your own and shared ones; a caller with `workspace.manage` (owners and admins) archives anyone's.

| | |
| --- | --- |
| `GET /v1/brands/{brand}/queries` | Saved queries you can see (`q` searches name and description, `visibility=private` only yours, `brand` only shared ones), newest first, at most 500 |
| `POST /v1/brands/{brand}/queries` | Save one: `name`, and `sql` or `builder`; optional `question`, `description`, `chart` (`{"type": "bar"\|"line"\|"area", "x": "plan", "y": ["n"]}`), `visibility` and `note` |
| `GET`, `PATCH /v1/brands/{brand}/queries/{saved_query}` | Read one with its current SQL and chart; change it (new `sql` or `chart` is a new version) |
| `POST .../queries/{saved_query}/archive` | Archive it |
| `GET .../queries/{saved_query}/versions`, `.../versions/{version}` | Its versions, newest first (the newest 200), and one version |
| `POST .../queries/{saved_query}/versions/{version}/restore` | Make a new version from an old one |
| `POST .../queries/{saved_query}/run` | Run the current version, or `version`; `limit` as for Query. It only reads, so an agent kept to missions may run it outside one |

Agents use the MCP tools `list_saved_queries`, `get_saved_query`, `run_saved_query` and `save_query`.

### Published endpoints

Owners and admins can publish a saved query as an endpoint (from the query's menu; **Data → Endpoints** lists them, each with how to call it and its latest calls) that another system calls with a brand API token holding only `query.endpoints`, with parameters bound as `$1`, `$2`, …, a row cap, a rate limit per token and every call logged. See [Published query endpoints](query-endpoints.md).

### CSV downloads

`POST /v1/brands/{brand}/query/csv` (with `sql`) and `POST .../queries/{saved_query}/csv` (optionally `version`) run the query and answer a CSV file (`Content-Disposition: attachment`): a header row of column names, then the rows, jsonb as JSON, times as RFC 3339 and null as an empty cell. A download holds up to 10,000 rows and 16 MB (`limit` asks for fewer), more than the 1,000 a JSON answer carries, since it is for a spreadsheet rather than a screen or an agent's context; `Outflare-Query-Rows` says how many it holds and `Outflare-Query-Truncated` whether there were more. A text cell that starts like a formula (`=`, `+`, `-`, `@`) gets a `'` in front, so a spreadsheet doesn't run it. A download needs `customers.read` and the read scopes of the tables it reads. It is personal data leaving the app in bulk, so every download is a `query.exported` entry in the activity log (with its row and column counts, never the SQL or the rows), and an agent kept to missions downloads only inside one.

## Events

Each query is an `api.called` event (`runBrandQuery`, or `runQuery` on the workspace route), and a successful one also records `query.ran` with the tables it read (type tables are counted in `type_tables`, not named), `brand_route`, the row count, whether it was truncated and how long it took. A saved query's run adds its `saved_query_id`, `version` and `current_version`. The SQL itself is never sent. Saved queries record `query.saved`, `query.version_created`, `query.updated` (name or description), `query.shared` (with `visibility`), `query.restored`, `query.archived` and `query.exported` in the activity log, each a product event of the same name with ids, versions and counts only, never the SQL or the name. A failure to set up the role is `query.setup_failed`, and a database that refuses the type tables (temporary views need the `TEMP` privilege, which Postgres gives every role by default) is `query.type_tables_failed`: the query still runs without them. Metrics: `outflare_query_runs_total{outcome}`, `outflare_query_duration_seconds`, `outflare_query_rows_total` and `outflare_query_type_tables_failed_total` (alert `OutflareQueryTypeTablesFailing`), plus `outflare_query_saved_runs_total{outcome}`, `outflare_query_exports_total{outcome}` and `outflare_query_export_rows_total` (alert `OutflareQueryExportsFailing`), on the Grafana dashboard's Query row. Saved queries live in `saved_queries` and `saved_query_versions` (migration `0345`).
