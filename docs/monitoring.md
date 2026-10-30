# Monitoring

Outflare can report how busy, slow and healthy it is as Prometheus metrics, for your own dashboards and alerts: request rates and latency per route, the job queue, the database connection pool, Postgres itself, and the server's CPU, memory and goroutines. It is off unless you turn it on, and nothing leaves the server: you point your own Prometheus, Grafana Alloy or Grafana Agent at it.

This is separate from [product analytics](product-analytics.md) (what people do, sent to your PostHog) and the [anonymous usage report](telemetry.md). Metrics carry route patterns, job kinds, outcomes and table names only, never workspace ids, people, paths with ids or anything a customer typed.

## Turn it on

Set `OUTFLARE_METRICS_ADDR` to a second, private address, such as `:9091`. The server then serves `GET /metrics` there, on its own listener, never on the public port (it refuses to start on `HTTP_ADDR`'s port).

| Variable | Default | What it does |
| --- | --- | --- |
| `OUTFLARE_METRICS_ADDR` | empty (off) | Where to serve `/metrics`, such as `:9091`. Keep it off the public internet. |
| `OUTFLARE_METRICS_TOKEN` | empty | When set, scrapers must send `Authorization: Bearer <token>`. Use one whenever other services share the network. |
| `OUTFLARE_METRICS_PPROF` | `false` | Also serve Go's profiler at `/debug/pprof/` on the same listener, to see what uses CPU or memory. Profiles can contain request data: only on a private listener. |

If the listener can't start, the server logs it and records `metrics.listen_failed`, and carries on serving the API and running jobs.

## Dashboards and alerts with Docker Compose

`deploy/docker-compose.yml` turns the metrics on inside Docker's own network (`:9091`, never published) and has an optional `monitoring` profile with Prometheus and Grafana, already set up:

```sh
echo "GRAFANA_ADMIN_PASSWORD=$(openssl rand -base64 18)" >> deploy/.env
docker compose -f deploy/docker-compose.yml --profile monitoring up -d
```

Open <http://localhost:3000> (bound to localhost only), sign in as `admin`, and open **Outflare** in the Outflare folder. Prometheus keeps 30 days (`PROMETHEUS_RETENTION`) and evaluates the alert rules in `deploy/monitoring/alerts.yml`; see them under Alerting in Grafana or on Prometheus's Alerts page. To be notified, add a contact point in Grafana or an Alertmanager.

## Grafana Cloud or your own Prometheus

- **Scrape** the metrics address every 15 to 30 seconds with job name `outflare` (the dashboard and rules expect it), with the bearer token if you set one. On Grafana Cloud, run Grafana Alloy next to the server with a `prometheus.scrape` that `forward_to`s a `prometheus.remote_write` for your stack.
- **Dashboard:** import `deploy/monitoring/grafana/dashboards/outflare.json` (Dashboards → New → Import) and choose your Prometheus data source.
- **Alerts:** load `deploy/monitoring/alerts.yml` as they are into Prometheus, or into Grafana Cloud with `mimirtool rules load deploy/monitoring/alerts.yml`.

Every Outflare metric starts with `outflare_`, so it is easy to keep only those if your plan counts series.

## What is measured

| Metric | Labels | Meaning |
| --- | --- | --- |
| `outflare_http_requests_total` | `route`, `code` (`2xx`...`5xx`) | Requests served. `route` is the route pattern (`GET /v1/workspaces/{ws}/leads`), `unmatched` when none matched. |
| `outflare_http_request_duration_seconds` | `route` | Request time histogram. `GET /mcp` is the hosted MCP server's event stream and stays open, so leave it out of latency. |
| `outflare_http_requests_in_flight` | | Requests being served now. |
| `outflare_http_rate_limited_total` | `limit` (`api`, `register`, `site_ip`, `site_key`, `site_visitor`, `site_flags`, `site_consent`) | Requests a rate limit refused with 429. |
| `outflare_jobs_processed_total` | `kind`, `outcome` (`succeeded`, `retried`, `dead`) | Jobs this server ran. |
| `outflare_jobs_duration_seconds`, `outflare_jobs_wait_seconds` | `kind` | How long a job ran, and how long it waited after it was due before a worker took it. |
| `outflare_jobs_reaped_total` | `kind` | Running jobs put back in the queue because their worker stopped holding them (a crash, or a job longer than the lease). Such a job may run twice. |
| `outflare_jobs_active`, `outflare_jobs_workers` | | Jobs running in this process, and how many workers it has (`WORKERS` plus the send queue's). |
| `outflare_jobs_queued`, `outflare_jobs_scheduled`, `outflare_jobs_running` | `kind` | Whole-queue depth from the database: due now, waiting for a later time (retries, schedules), and claimed. |
| `outflare_jobs_oldest_due_seconds` | `kind` | How long the longest-waiting due job has waited. The best single sign that workers can't keep up. |
| `outflare_ingest_events_total` | `outcome` (`accepted`, `rejected`, `capped`) | Website events the collector received: queued, refused for their shape, or turned away by a daily limit. |
| `outflare_ad_tracking_arrivals_total` | `network` (an ad network, or `other`), `outcome` (`click_id`, `ad_ids`, `none`) | Landing pages from paid ads (a paid `utm_medium`) and how they were tracked: the network's click id, only the tracking template's ad ids, or neither. |
| `outflare_ad_tracking_unfilled_macros_total` | `network` | Tracking template values that arrived as the macro itself, because the network didn't fill them in. |
| `outflare_redirect_clicks_total` | `outcome` (`counted`, `bot`, `unknown`, `error`) | Clicks on campaign redirect links (`/l/{code}`): a person, a link scanner or preview, a code that isn't a link, or a click that couldn't be stored. |
| `outflare_redirect_landings_total` | `outcome` (`claimed`, `unmatched`) | Page views carrying a redirect click's key (`ofclk`): the visit claimed the click, or the click was already claimed (a reload) or belongs to another workspace. |
| `outflare_inbox_events_total` | `outcome` (`stored`, `rejected`, `failed`) | Website events workers took from the inbox. `rejected` is an event the workspace refused (an unapproved name over the limit, a record conflict); `failed` is a batch that will be tried again. |
| `outflare_inbox_batch_seconds` | | Time to store one batch from the inbox. |
| `outflare_inbox_pending`, `outflare_inbox_oldest_seconds`, `outflare_inbox_failed` | | Website event batches waiting for a worker, how long the oldest has waited, and batches set aside after 5 failures. |
| `outflare_db_pool_*` | | The engine's connection pool: connections in use, idle, open and the maximum, and how often and how long callers waited for a free one. |
| `outflare_pg_connections` | `state` | Connections to the database by state (`active`, `idle`, `idle in transaction`), next to `outflare_pg_max_connections`. |
| `outflare_pg_longest_transaction_seconds`, `outflare_pg_lock_waiting_connections` | | The oldest open transaction, and connections waiting on a lock. |
| `outflare_pg_commits_total`, `_rollbacks_total`, `_blocks_hit_total`, `_blocks_read_total`, `_deadlocks_total`, `_temp_bytes_total` | | The database's own counters: throughput, cache hits, deadlocks and queries spilling to disk. |
| `outflare_pg_database_bytes` | | Size of the database. |
| `outflare_pg_table_bytes`, `outflare_pg_table_dead_rows`, `outflare_pg_table_seq_scans_total` | `table` | For the 25 largest tables, and always the job queue (`jobs`): size with indexes, rows waiting for vacuum, and sequential scans (a large table scanned often is missing an index). |
| `outflare_pipeline_items` | `pipeline`, `stage`, `state` (`due`, `scheduled`, `working`, `held`) | Pipeline items now, from the database ([pipeline.md](pipeline.md)). |
| `outflare_pipeline_oldest_due_seconds` | `pipeline`, `stage` | How long the longest-waiting due item of a stage has waited. The highest is the bottleneck. |
| `outflare_pipeline_items_total`, `outflare_pipeline_codes_total` | `pipeline`, `stage`, `outcome` or `code` | What stages decided in this process, and why items were skipped, held, retried, made to wait, paused or failed. |
| `outflare_pipeline_batch_seconds`, `outflare_pipeline_batch_items`, `outflare_pipeline_wait_seconds` | `pipeline`, `stage` | Time and size of a batch, and the wait from an item being due to a worker claiming it. |
| `outflare_pipeline_limit_waits_total`, `outflare_pipeline_limit_reports_total` | `account`, `reason` or `outcome` | Calls to outside accounts held back by a rate limit or breaker, and how calls went. `account` is the kind (`integration`, `ai`), never an id. |
| `outflare_pipeline_runs`, `outflare_pipeline_breakers` | `pipeline` and `state`, or `account` and `state` | Runs running or paused, and accounts whose breaker is open or half open. |
| `outflare_ai_calls_total`, `outflare_ai_cost_cents_total` | `provider`, `task` | AI model calls made with workspaces' own keys, and what they cost on the workspaces' own provider bills in US cents, by provider (connector id) and tool (`plan`, `reply_labelling`...). Never by workspace. |
| `outflare_ai_tokens_total` | `provider`, `task`, `kind` (`input`, `output`, `cache_read`, `cache_write`) | Tokens those calls used. |
| `outflare_ai_budget_alerts_total` | `level` (`80`, `100`), `scope` (`workspace`, `tool`) | Workspaces warned that their AI spend reached 80% or all of a monthly limit. |
| `outflare_connection_limits_alerts_total` | `level` (`80`, `100`), `period` (`day`, `month`) | Workspaces warned that a paid connection used 80% of a usage limit, or reached it ([connection limits](usage-limits.md)). |
| `outflare_connection_limits_stops_total` | `connector`, `period` | Paid calls Outflare didn't make because the connection had reached a limit that stops calls. |
| `outflare_hooks_deliveries_total` | `connector`, `outcome` (`accepted`, `duplicate`, `bad_signature`, `not_found`, `invalid`, `error`) | Webhooks connectors sent to a connection's live updates address (`/v1/hooks/...`). |
| `outflare_hooks_events_total` | `connector`, `result` (`handled`, `ignored`, `retried`, `failed`) | What workers did with each event a webhook carried. |
| `outflare_hooks_handle_lag_seconds` | `connector` | Time from a webhook arriving to its event being handled. |
| `outflare_query_runs_total` | `outcome` (`success`, `invalid`, `timeout`, `unavailable`, `error`) | [Queries](query.md) run. |
| `outflare_query_duration_seconds`, `outflare_query_rows_total` | | How long queries took, and the rows they returned. |
| `outflare_smart_lists_lists`, `outflare_smart_lists_oldest_refresh_seconds`, `outflare_smart_lists_failing` | | [Smart lists](lists.md#smart-lists): how many there are, how long since the least recently refreshed one was brought up to date (normal is under 20 minutes), and how many failed their last refresh. |
| `outflare_rules_evaluations_total` | `trigger`, `outcome` | [Rules](rules.md) evaluated for a record: `fired`, or why not (`no_match`, `duplicate`, `cooldown`, `late`, `before_live`, `too_deep`, `manual`), and absence clocks `started` and `stopped`. |
| `outflare_journeys_calls_total` | `kind`, `outcome` | Calls made by journeys' webhook, Slack, `push_to_sequencer`, `push_to_crm`, `sync_audience` and `ai` steps: `done`, `skipped` (nothing to do), `retried` (a try that will be tried again) or `failed`. |
| `outflare_sequencers_leads_pushed_total` | `connector`, `outcome` | People journeys added to Smartlead, Instantly and EmailBison campaigns: `added`, `already_in`, `refused` (the sequencer left them out) or `error`. |
| `outflare_crm_contacts_pushed_total` | `connector`, `outcome` | People journeys created or updated in a CRM (HubSpot): `created`, `updated` or `error`. |
| `outflare_ads_audience_syncs_total` | `connector`, `outcome` | People journeys added to or removed from customer list audiences on ad accounts (Meta, Google Ads): `added`, `removed`, `skipped` (no ad consent, or no email or phone) or `error`. |
| `outflare_journeys_call_duration_seconds` | `kind` | Time one try of a journey call took. |
| `outflare_journeys_entries_refused_total` | `reason` | People a journey's trigger would have started but its limits turned away: `limit` (entry limit per hour or day) or `reentry` (too soon after their last run). |
| `outflare_journeys_waiting_runs` | | Journey runs on a `wait_until` step. |
| `outflare_journeys_oldest_due_seconds` | | How long past its time the most overdue run of an active journey is. |
| `outflare_automations_excluded_total` | `where` | People an exclusion left out: `journey` (refused entry), `broadcast` (skipped), `sequence` (not enrolled). |
| `outflare_rules_active`, `outflare_rules_timers_due`, `outflare_rules_oldest_due_seconds` | `trigger` (active) | Active rules by trigger kind, and absence timers that ran out and wait to be handled, with how long the oldest has waited (normal is near 0 and under two minutes). |
| `outflare_ad_conversions_total` | `connector`, `outcome` (`sent`, `skipped`, `failed`) | Conversions sent back to ad platforms ([paid-media.md](paid-media.md#sending-conversions-back)). |
| `outflare_ad_conversions_oldest_queued_seconds` | | How long the oldest conversion waiting to be sent has waited. |
| `outflare_meeting_events_total` | `status` (`booked`, `held`, `no_show`, `canceled`) | Meeting events recorded on people's timelines ([customers.md](customers.md#meetings-as-events)). |
| `outflare_meeting_events_pending` | | Meetings tied to a person whose latest status isn't recorded as an event yet. |
| `outflare_partners_clicks_total` | `outcome` (`tracked`, `inactive`, `not_found`, `error`) | Clicks on partners' short links ([partners.md](partners.md)). |
| `outflare_partners_conversions_total` | `type` (`signup`, `sale`, `refund`, `custom`), `source` (`api`, `postback`, `browser`), `outcome` (`credited`, `not_credited`, `duplicate`, `rejected`, `error`) | Partner conversions reported by workspaces' backends, postback URLs and websites' `outflare.signup()`. |
| `outflare_partners_rewards_total` | `kind` (`cash`, `fee`, `action`, `adjustment`), `event` (`created`, `approved`, `rejected`, `reversed`, `fulfilled`, `paid`) | Changes to the partner reward ledger. |
| `outflare_partners_postbacks_total` | `outcome` (`sent`, `retried`, `rejected`, `unreachable`, `invalid`) | Postbacks to partners' own trackers ([partners.md](partners.md#postbacks-to-your-partners-trackers)). |
| `outflare_partners_rewards_overdue_seconds` | | How long past its hold the oldest reward due to approve on its own has waited. |
| `outflare_partners_imports_total` | `connector` (`tolt`), `outcome` (`done`, `failed`) | Partner program imports from another platform that finished ([moving from Tolt](partners-tolt-migration.md)). |
| `outflare_partners_import_objects_total` | `kind` (`groups`, `links`, `partners`, `flows`, `promo_codes`, `customers`, `transactions`, `commissions`), `result` (`created`, `updated`, `unchanged`, `skipped`, `unreadable`: a whole kind left out, counted once) | Objects partner imports read, and what each became. |
| `outflare_flags_requests_total` | `surface` (`site`, `api`), `outcome` (`ok`, `refused`, `error`) | Requests to decide a visitor's [feature flags](feature-flags.md): from the website script or a server. |
| `outflare_flags_request_seconds` | `surface` | Time to decide them. |
| `outflare_flags_cache_total` | `result` (`hit`, `miss`) | Lookups of a workspace's flags answered from memory or read from Postgres (at most every 10 seconds per process). |
| `outflare_site_consent_requests_total` | `endpoint` (`config`, `choice`), `outcome` (`ok`, `refused`, `error`) | Requests from websites' [consent banners](consent-banner.md): the banner's settings, and visitors' choices kept as consents. |
| `outflare_site_consent_request_seconds` | `endpoint` | Time to answer them. |
| `outflare_site_consent_choices_total` | `action` (`accept_all`, `reject_all`, `custom`, `opt_out`, `gpc`, `api`), `region` (`eea`, `us`, `other`) | Visitors' consent choices kept. |
| `outflare_cookie_scan_scans_total` | `outcome` (`ok`, `failed`) | Scans of websites for the cookies and scripts their [cookie policy](consent-banner.md#the-cookie-policy) lists. |
| `outflare_cookie_scan_pages_total`, `outflare_cookie_scan_found_total` | | Pages scans read, and what they found. |
| `outflare_cookie_scan_seconds` | | Time one scan takes. |
| `outflare_cookie_scan_reports_total` | `outcome` (`ok`, `refused`, `error`) | Reports of a site's cookie and script names from visitors' browsers. |
| `outflare_cookie_scan_new_total` | `known` (`known`, `unknown`) | Things found on a site for the first time, by whether the vendor catalogue knew them. |
| `outflare_ab_results_total` | `outcome` (`ok`, `error`) | [A/B test](ab-tests.md) results counted from product events: when someone opens a running test's results or ends it. |
| `outflare_ab_results_seconds` | | Time to count them, every goal included. |
| `outflare_reviews_page_reads_total` | `platform`, `outcome` (`success`, `pending` (still being read; the next round picks it up), `blocked`, `slow_page`, `page_not_found`, `not_a_review_page`, `save_failed`, `connector_error`) | Reads of brands' [review pages](reviews.md). |
| `outflare_reviews_new_total` | `platform` | Reviews kept for the first time. |
| `outflare_reviews_pages` | `platform`, `state` (`ok`, `failing`) | Active review pages, and how many failed their last read. |
| `outflare_sync_runs_total` | `module` (`analytics`, `search_console`, `page_conversions`, `ads`, `seo`), `outcome` (`success`, `internal`, `budget_reached`, `connector_*`) | Scheduled sync runs of connections. |
| `outflare_sync_stuck` | `module`, `code` | Scheduled reads whose last 3 or more runs failed, retries included, by the last error code: connections' syncs, SEO site competitor and backlink reads (`seo_site_competitors`, `seo_site_backlinks`), news searches (`news`) and competitor looks (`competitor_glances`, always `crawl_failed`). |
| `outflare_social_account_reads_total` | `channel`, `outcome` (`success`, `blocked`, `page_not_found`, `invalid_address`, `no_follower_count`, `save_failed`, `connector_error`) | Reads of social accounts' followers ([social](social.md)). |
| `outflare_social_stuck_accounts` | `channel`, `code` | Active social accounts whose last 3 or more reads failed, retries included, by the last error code. |
| `outflare_reviews_stuck_pages` | `platform`, `code` | Active review pages whose last 3 or more reads failed, retries included, by the last error code. |
| `outflare_reviews_oldest_read_seconds` | `platform` | Time since the least recently read active review page was read. |
| `outflare_news_search_runs_total` | `purpose` (`topic`, `coverage`), `outcome` (`success`, `connector_needed`, `usage_limit_reached`, `connector_error`, `internal`) | Runs of saved [news searches](news-watch.md). |
| `outflare_news_found_total` | `kind` (`article`, `coverage`) | Articles and coverage items news searches kept for the first time. |
| `outflare_inbound_email_received_total` | `purpose` (`support`, `ai_receipts`, `none`), `outcome` (`accepted`, `duplicate`, `unknown_address`, `bad_signature`, `too_large`, `error`) | Messages the operator's mail receiver delivered to [inbound email](inbound-email.md). |
| `outflare_inbound_email_processed_total` | `purpose`, `status` (`processed`, `ignored`, `rejected`, `failed`) | What became of delivered messages. |
| `outflare_inbound_email_received_bytes_total` | | Size of the inbound messages accepted. |
| `outflare_inbound_email_raw_purged_total` | | Raw inbound messages deleted after 30 days. |
| `outflare_exchange_rates_fetched_total` | `outcome` (`success`, `error`) | Fetches of the European Central Bank's daily exchange rates, every 6 hours on servers with inbound email. |
| `outflare_exchange_rates_newest_age_seconds` | | Age of the newest exchange rate stored, set after each fetch. |
| `outflare_exchange_rates_receipts_converted_total` | | AI receipts in other currencies given their US dollar amount once a rate arrived. |
| `outflare_news_searches` | `state` (`ok`, `failing`) | Active news searches, and how many failed their last run. |
| `outflare_news_search_overdue_seconds` | | How long past its time the most overdue active news search is. |
| `outflare_news_article_reads_total` | `status` (`up`, `gone`, `blocked`, `opted_out`, `unreachable`, `failed`), `journalist` | Reads of new news articles for their byline; `journalist` is `true` when one found a new journalist. |
| `outflare_social_publishes_total` | `channel`, `outcome` (`published`, `failed`, `retried`) | Tries of the job that posts the brand's own [social posts](social.md#publishing). |
| `outflare_reddit_sends_total` | `kind`, `outcome` (`submitted`, `failed`, `unknown`) | [Reddit campaign](reddit-campaigns.md) actions sent to Engain. `unknown`: Engain didn't answer, so the action may or may not exist. |
| `outflare_reddit_checks_total` | `outcome` (`ok`, `engain_error`, `reddit_error`), `reddit_read` | Checks of sent Reddit campaign actions on Engain and on Reddit as a logged-out visitor sees them. |
| `outflare_social_publications_scheduled` | `channel`, `state` (`due`, `later`) | Approved posts not out yet: due ones wait for the publish job, later ones are scheduled ahead. |
| `outflare_social_publication_overdue_seconds` | `channel` | How long past its time the most overdue approved post is. |
| `outflare_seo_link_reads_total` | `reason` (`requested`, `dead_page`), `outcome` (`success`, `budget_reached`, `connector_auth`, `invalid_request`, `provider_error`, `internal`) | Reads of who links to a page or domain on workspaces' own SEO connections ([link building](seo.md#link-reads)). |
| `outflare_seo_link_page_checks_total` | `status` (`up`, `gone`, `redirected`, `blocked`, `opted_out`, `unreachable`, `failed`) | OutflareBot's checks of link prospects' pages. |
| `outflare_seo_archive_lookups_total` | `outcome` (`found`, `none`, `error`) | Internet Archive lookups of broken links found by page checks. |
| `outflare_seo_link_pitches_written_total` | `outcome` (`written`, `no_ai`, `ai_unavailable`, `ai_failed`) | Openers the workspace's AI wrote for link pitches. |
| `outflare_seo_link_replies_read_total` | `kind` (`agreed`, `declined`, `wants_payment`, `wants_content`, `other`, `no_ai`, `ai_unavailable`, `ai_failed`) | Replies to link pitches the workspace's AI read. |
| `outflare_seo_link_live_checks_total` | `outcome` (`live`, `not_yet`, `removed`, `still_live`, `unreachable`) | Checks of agreed and live links. |
| `outflare_seo_web_searches_total` | `outcome` (`done`, `budget_reached`, `connector_invalid`, `connector_auth`, `connector_quota`, `connector_unavailable`) | Searches on workspaces' own SERP connections (`POST /search-results`). |
| `outflare_playbooks_automation_runs_total` | `kind` (`schedule`, `event`), `outcome` (`started`, `held`, `failed`, `capped`) | Playbook automations firing: runs started, held for a person (a limit, a connection, the approver), failed (retried in 15 minutes), or left for tomorrow by the daily cap. |
| `outflare_playbooks_automation_lag_seconds` | | How long the most overdue active playbook automation has waited past its run time, at the last pass. |
| `outflare_build_info` | `version`, `role` | The running version and role (`OUTFLARE_ROLE`); the dashboard marks each change of version as a deploy. |
| `outflare_metrics_scrape_errors`, `outflare_metrics_scrape_duration_seconds` | | Database queries that failed in the last scrape, and how long it took. |
| `go_*`, `process_*` | | Go runtime and process: CPU seconds, resident memory, heap, goroutines, garbage collection, open files. |

The database numbers are read when Prometheus scrapes, at most once every 10 seconds whatever the number of scrapers, with a 5-second limit, using the partial indexes on queued and running jobs.

## Reading it

- **Requests slow, pool full.** `outflare_db_pool_acquired_conns` at `outflare_db_pool_max_conns` with `empty_acquire_wait_seconds` rising means requests and jobs queue for a connection. The pool's size comes from `DATABASE_URL` (`?pool_max_conns=20`); by default it is the larger of 4 and the number of CPUs, which is small next to 32 workers. Keep the total across servers under `outflare_pg_max_connections`.
- **Queue backing up.** `outflare_jobs_oldest_due_seconds` climbing for one kind while `outflare_jobs_active` equals `outflare_jobs_workers` means more `WORKERS` (or more servers); climbing while workers are idle means jobs are failing and retrying (`outcome="retried"`) or waiting on the pool.
- **Pipeline stage behind.** The stage with the highest `outflare_pipeline_oldest_due_seconds` is the bottleneck. If its batches are slow (`outflare_pipeline_batch_seconds`) and "Why items stop" shows rate limits or open breakers, the outside account is the limit. If batches are fast but small, the per-workspace cap is binding. Otherwise it needs more workers.
- **Website events behind.** `outflare_inbox_oldest_seconds` over a few seconds means workers aren't keeping up with the collector (or none is running); the events are safe in the inbox and are stored once they catch up. A rising `outflare_inbox_failed` means batches are being set aside: look for `site_events.store_failed` in the logs.
- **Webhooks failing.** A rise in `outflare_hooks_deliveries_total{outcome="bad_signature"}` for one connector means its signing secret changed: make a new address or paste the new secret on the connection. `not_found` means a tool still sends to an address that was replaced or turned off. Failed events are listed under Logs → Incoming webhooks with their error code, and can be run again.
- **Database pressure.** A cache hit ratio under 99%, rising `temp_bytes`, long transactions or many dead rows point at the query or table to look at.

## Alerts

`deploy/monitoring/alerts.yml` has rules for: the server not answering; over 2% of requests failing with a 5xx; p95 latency over 2 seconds; a due job waiting over 10 minutes; website events waiting over 5 minutes to be stored, or batches set aside; jobs of one kind dying; jobs put back after their lease ran out; the pool over 90% in use or callers waiting on it; Postgres over 80% of `max_connections`; a transaction open over 5 minutes; deadlocks; over 20,000 goroutines; a pipeline stage with an item waiting over 30 minutes, over 10% of a stage's items retrying or failing, items failing for good, or an outside account's breaker open for 10 minutes; the database growing over 25% in a day (once it is over 1 GB); database metrics that can't be read; one tool making over 5 AI calls a second for 15 minutes (a loop spending workspaces' money); over 20 AI spend limits reached in an hour; a paid connection reaching its daily or monthly usage limit (info: its workspace's admins are emailed); over 500 calls an hour through one connector refused at a usage limit for 30 minutes (a feature retrying into the limit); over 20 webhook deliveries with a bad signature for one connector in 15 minutes; over 5 webhook events failing in 30 minutes; webhook events waiting over 5 minutes to be handled; over half of Domain Connect lookups failing (a DNS provider's API down, or the signing key's TXT record at outflare.ai missing); Query unable to set up its database role; over a quarter of queries timing out; meaning-based search on in workspaces while the database has no pgvector; most embedding jobs failing; most questions asked of the knowledge base failing, or answers from it taking over 20 seconds at the 95th percentile (info); a smart list not refreshed for over an hour; smart lists whose last refresh failed (info); an absence rule's timer due for over 15 minutes, over 10,000 rule timers waiting, or rule jobs failing and retrying; a journey run due for over 15 minutes; over half of journey webhook, Slack, sequencer or CRM calls failing (info), or taking over 10 seconds at the 95th percentile (info); over half of the people journeys add to one sequencer's campaigns failing in 30 minutes; over half of the people journeys create or update in one CRM failing in 30 minutes; over half of the people journeys add to or remove from one ad platform's audiences failing in 30 minutes; over a quarter of conversions sent to one ad platform failing; a conversion waiting over 30 minutes to be sent; over 500 meetings waiting 30 minutes for their meeting event on people's timelines; a partner reward waiting over 30 minutes past its hold to be approved; partner conversions failing with server errors; partner link clicks not being stored; over half of partner postbacks failing for an hour (info: usually partners' trackers, or outbound traffic blocked); several partner imports from Tolt failing in an hour (info); the same kind unreadable from Tolt in several partner imports within 6 hours; over 2% of feature flag requests failing; flags taking over 250 ms to decide at the 95th percentile; over 2% of consent banner requests failing; a network's tracking template macros arriving unfilled for an hour (info); over 80% of a network's paid landings in 6 hours with no click id or ad ids (info); over 5% of clicks on campaign redirect links failing to be stored; consent banner requests taking over 500 ms at the 95th percentile; over half of cookie scans reading no page; over 5% of browsers' cookie reports failing; A/B test results taking over 10 seconds to count at the 95th percentile (info); over 5 A/B test results failing to count in 30 minutes; a review page not read for two days; over half of review page reads failing; a news search over an hour past its time; over half of news search runs failing with a provider or internal error; over half of news article reads failing to reach the page; an approved social post over 10 minutes past its time; over 5 social posts failing on one channel in an hour; over half of link reads failing for reasons other than a budget; over half of link prospect page checks failing; over half of Internet Archive lookups failing (info); review pages that keep failing after retries with an error Outflare has to fix (`slow_page`, `not_a_review_page`, `save_failed`; a site blocking the page reader, `blocked`, is the reader's to get past and doesn't alert); review page reads that couldn't be saved; social accounts that keep failing after retries with an error Outflare has to fix (`slow_page`, `no_follower_count`, `save_failed`; a site blocking the page reader, `blocked`, is the reader's to get past and doesn't alert); over half of social account reads failing; over half of link pitch openers or of replies to pitches failing to be read by the AI; over half of checks of agreed and live links unable to reach the page (info); over half of social account reads failing; over half of social account reads failing; a connection's analytics, Search Console, conversions, ads or SEO sync failing 3 runs in a row with Outflare's own error or the provider down; any sync run failing on Outflare's side; over half of web searches failing for reasons other than a budget; playbook automations failing to start runs for an hour with none started; and an active playbook automation over 15 minutes past its run time. The thresholds suit one server; inbound email deliveries refused for a bad signature (the mail receiver and the engine disagree on the inbound secret), inbound messages failing to be processed, or deliveries failing before they are stored; the newest exchange rate over 6 days old; over a quarter of support replies failing to send, a support reply waiting over 5 minutes for a worker, or replies taking over 20 seconds to send at the 95th percentile (info); over 5% of public help centre pages failing; help centre views not being counted (info); help centre pages taking over a second to build at the 95th percentile (info); adjust them in your copy.

For [Reddit campaigns](reddit-campaigns.md), alerts fire when over 5 actions fail to reach Engain in an hour, and when over half of the checks of sent actions fail over 6 hours.

Data network alerts: a withdrawal past its 30-day deadline (critical); a member's sharing over 2 days behind; most sharing jobs failing; over a quarter of lookups failing; and over 500 lookups an hour hitting a domain's daily limit (info: someone may be guessing names).

## Metrics from extensions

An extension registers its own Prometheus collectors with `Host.Metrics()`, so they are served on the same metrics address and reach the same Prometheus. Name them `outflare_<extension>_*`. The dashboards and alerts for them ship with the extension, not here.
