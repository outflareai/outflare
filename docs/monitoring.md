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
| `outflare_http_requests_total` | `route`, `code` (`2xx`...`5xx`) | Requests served. `route` is the route pattern (`GET /v1/brands/{brand}/leads`), `unmatched` when none matched. |
| `outflare_http_request_duration_seconds` | `route` | Request time histogram. `GET /mcp` is the hosted MCP server's event stream and stays open, so leave it out of latency. |
| `outflare_http_requests_in_flight` | | Requests being served now. |
| `outflare_http_rate_limited_total` | `limit` (`api`, `session`, `register`, `site_ip`, `site_key`, `site_visitor`, `site_flags`, `site_consent`) | Requests a rate limit refused with 429. |
| `outflare_jobs_processed_total` | `kind`, `outcome` (`succeeded`, `retried`, `dead`, `interrupted`, `released`) | Jobs this server ran. `interrupted` stopped for a shutdown and went back in the queue at once; `released` was still running when the shutdown grace ended and was handed back (its handler ignores cancellation). |
| `outflare_jobs_duration_seconds`, `outflare_jobs_wait_seconds` | `kind` | How long a job ran, and how long it waited after it was due before a worker took it. |
| `outflare_jobs_reaped_total` | `kind` | Running jobs put back in the queue because their worker stopped holding them (a crash, or a job longer than the lease). Such a job may run twice. |
| `outflare_jobs_active`, `outflare_jobs_workers` | | Jobs running in this process, and how many workers it has (`WORKERS` plus the send queue's). |
| `outflare_jobs_queued`, `outflare_jobs_scheduled`, `outflare_jobs_running` | `kind` | Whole-queue depth from the database: due now, waiting for a later time (retries, schedules), and claimed. |
| `outflare_jobs_oldest_due_seconds` | `kind` | How long the longest-waiting due job has waited. The best single sign that workers can't keep up. |
| `outflare_ingest_events_total` | `outcome` (`accepted`, `rejected`, `capped`) | Website events the collector received: queued, refused for their shape, or turned away by a daily limit. |
| `outflare_ad_tracking_arrivals_total` | `network` (an ad network, or `other`), `outcome` (`click_id`, `ad_ids`, `none`) | Landing pages from paid ads (a paid `utm_medium`) and how they were tracked: the network's click id, only the tracking template's ad ids, or neither. |
| `outflare_ad_tracking_unfilled_macros_total` | `network` | Tracking template values that arrived as the macro itself, because the network didn't fill them in. |
| `outflare_redirect_clicks_total` | `outcome` (`counted`, `bot`, `unknown`, `error`, `dropped`) | Clicks on campaign redirect links (`/l/{code}`): a person, a link scanner or preview, a code that isn't a link, a link that couldn't be looked up, or a click dropped because too many waited to be written. Clicks are written in batches a moment later. |
| `outflare_redirect_click_writes_total` | `outcome` (`written`, `refused_quota`, `link_gone`, `failed`, `dropped_full`) | What happened to redirect clicks when their batch was written: written, over the workspace's click quota, their link gone, the batch failed and waits for the next flush, or dropped with too many waiting. |
| `outflare_redirect_click_buffer` | | Redirect clicks waiting in a process to be written. |
| `outflare_tracker_postbacks_total` | `outcome` (`accepted`, `duplicate`, `updated`, `unknown_source`, `wrong_domain`, `ip_refused`, `invalid`, `unknown_click`, `expired_click`, `rate_limited`, `error`) | Tracker conversion postbacks from brands' networks, by what happened. |
| `outflare_tracker_source_postbacks_total` | `outcome` (`queued`, `filtered`, `suspect`, `awaiting_lookup`, `sent`, `retried`, `rejected`, `unreachable`, `invalid`) | Tracker conversions sent back to traffic sources' postback URLs, by what happened. |
| `outflare_tracker_fraud_flags_total` | `subject` (`click`, `conversion`), `reason` (`datacenter_network`, `repeat_click`, `fast_conversion`, `filter_list`, `honeypot`, `ip_reputation`) | Tracker clicks and conversions a fraud signal flagged. |
| `outflare_tracker_ip_lookups_total` | `outcome` (`scored`, `flagged`, `capped`, `off`, `retried`, `failed_auth`, `failed_quota`, `failed_unavailable`, `failed_invalid`) | Tracker click addresses looked up on brands' own IP reputation connections, by what happened. |
| `outflare_tracker_ip_lookup_seconds` | | Time one IP lookup took at the brand's provider. |
| `outflare_tracker_ip_lookups_due`, `outflare_tracker_ip_lookup_oldest_seconds` | | Click addresses waiting for an IP lookup, and how long the oldest has waited. |
| `outflare_tracker_weight_suggestions_seconds` | `outcome` (`success`, `error`) | Time to work out a link's or flow's suggested weights. |
| `outflare_report_drilldown_seconds` | `outcome` (`success`, `error`) | Time to answer one drill-down report. |
| `outflare_tracker_lander_clicks_total` | `outcome` (`routed`, `fallback`, `unknown_click`, `wrong_domain`, `error`) | Clicks on landers' calls to action (`/click`), by where they went. |
| `outflare_tracker_conversion_rows_total` | `via` (`pixel`, `api`, `csv`), `outcome` | Tracker conversions from pixels and uploads, by how they came and what happened. |
| `outflare_redirect_routes_total` | `outcome` (`path`, `destination`, `fallback`, `shared_domain`) | Clicks on redirect links with destinations, by where they went: a rule-based path's destination, one of the default destinations, the link's own page because every destination was capped or stopped, or the link's own page because the click came on the shared domain, where tracker features are off. A cap reached is normal, so this has a panel and no alert. |
| `outflare_redirect_click_flush_seconds` | | Time to write one batch of up to 1,000 redirect clicks. |
| `outflare_redirect_landings_total` | `outcome` (`claimed`, `pending`, `direct`, `unmatched`) | Page views carrying a redirect click's key (`ofclk`): the visit claimed the click, its click wasn't written yet and the landing waits for it, a direct-mode visit recorded its own click, or nothing took it (a reload, another workspace's key, or a waiting landing no click took within an hour). |
| `outflare_inbox_events_total` | `outcome` (`stored`, `rejected`, `failed`) | Website events workers took from the inbox. `rejected` is an event the workspace refused (an unapproved name over the limit, a record conflict); `failed` is a batch that will be tried again. |
| `outflare_inbox_batch_seconds` | | Time to store one batch from the inbox. |
| `outflare_inbox_pending`, `outflare_inbox_oldest_seconds`, `outflare_inbox_failed` | | Website event batches waiting for a worker, how long the oldest has waited, and batches set aside after 5 failures. |
| `outflare_db_pool_*` | | The engine's connection pool: connections in use, idle, open and the maximum, and how often and how long callers waited for a free one. |
| `outflare_pg_connections` | `state` | Connections to the database by state (`active`, `idle`, `idle in transaction`), next to `outflare_pg_max_connections`. |
| `outflare_pg_longest_transaction_seconds`, `outflare_pg_lock_waiting_connections` | | The oldest open transaction, and connections waiting on a lock. |
| `outflare_pg_commits_total`, `_rollbacks_total`, `_blocks_hit_total`, `_blocks_read_total`, `_deadlocks_total`, `_temp_bytes_total` | | The database's own counters: throughput, cache hits, deadlocks and queries spilling to disk. |
| `outflare_db_tx_retries_total` | `operation`, `outcome` (`retried`, `gave_up`) | A request's write that lost a deadlock (or couldn't serialize) and ran again; `gave_up` when its third try lost too and the request answered 500. Used by saving transactions and customer events. |
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
| `outflare_quota_checks_total` | `resource` (`brands`, `mailboxes`, `tracking_domains`, `records`, `emails_sent`), `outcome` (`allowed`, `refused`, `error`) | Checks an extension's quota answered before adding something or sending ([quotas](quotas.md)). |
| `outflare_connection_limits_alerts_total` | `level` (`80`, `100`), `period` (`day`, `month`) | Workspaces warned that a paid connection used 80% of a usage limit, or reached it ([connection limits](usage-limits.md)). |
| `outflare_connection_limits_stops_total` | `connector`, `period` | Paid calls Outflare didn't make because the connection had reached a limit that stops calls. |
| `outflare_hooks_deliveries_total` | `connector`, `outcome` (`accepted`, `duplicate`, `bad_signature`, `not_found`, `invalid`, `error`) | Webhooks connectors sent to a connection's live updates address (`/v1/hooks/...`). |
| `outflare_hooks_events_total` | `connector`, `result` (`handled`, `ignored`, `retried`, `failed`) | What workers did with each event a webhook carried. |
| `outflare_hooks_handle_lag_seconds` | `connector` | Time from a webhook arriving to its event being handled. |
| `outflare_query_runs_total` | `outcome` (`success`, `invalid`, `timeout`, `unavailable`, `error`) | [Queries](query.md) run. |
| `outflare_query_duration_seconds`, `outflare_query_rows_total` | | How long queries took, and the rows they returned. |
| `outflare_query_saved_runs_total`, `outflare_query_exports_total` | `outcome` (as above) | [Saved queries](query.md#query-studio) run, and query results downloaded as CSV. |
| `outflare_query_export_rows_total` | | Rows written to CSV downloads. |
| `outflare_smart_lists_lists`, `outflare_smart_lists_oldest_refresh_seconds`, `outflare_smart_lists_failing` | | [Smart lists](lists.md#smart-lists): how many there are, how long since the least recently refreshed one was brought up to date (normal is under 20 minutes), and how many failed their last refresh. |
| `outflare_rules_evaluations_total` | `trigger`, `outcome` | [Rules](rules.md) evaluated for a record: `fired`, or why not (`no_match`, `duplicate`, `cooldown`, `late`, `before_live`, `too_deep`, `manual`), and absence clocks `started` and `stopped`. |
| `outflare_lifecycle_evaluations_total` | `level`, `outcome` | [Lifecycle stages](product-lifecycle.md) checked for a record at a level (`company`, `person`, `any`): `moved`, or why not (`no_move`, `duplicate`, `late`, `stale`, `before_live`, `not_approved`, `off`). |
| `outflare_record_metrics_updates_total` | `outcome` | Events added to a live [metric](metrics.md): `updated`, or why not (`backfilled`, `too_old`, `no_value`). |
| `outflare_record_metrics_backfill_seconds`, `outflare_record_metrics_sweep_seconds` | `outcome` (backfill) | How long a metric's fill from stored events, and one window sweep, took. |
| `outflare_lifecycle_sweep_seconds` | | How long one lifecycle inactivity sweep took. |
| `outflare_emails_guarded_total` | `trigger`, `outcome` | Marketing emails from journeys and broadcasts that a workspace's guardrails stopped (`frequency_cap`) or held until morning (`quiet_hours`). |
| `outflare_journeys_calls_total` | `kind`, `outcome` | Calls made by journeys' webhook, Slack, `push_to_sequencer`, `push_to_crm`, `sync_audience` and `ai` steps: `done`, `skipped` (nothing to do), `retried` (a try that will be tried again) or `failed`. |
| `outflare_sequencers_leads_pushed_total` | `connector`, `outcome` | People journeys added to Smartlead, Instantly and EmailBison campaigns: `added`, `already_in`, `refused` (the sequencer left them out) or `error`. |
| `outflare_mailboxes_sign_ins_moved_total` | `kind` (`admin`, `sign_in`), `outcome` (`moved`, `linked`, `skipped`, `failed`) | Mailbox sign-ins from before they were Gmail and Outlook connections, moved into connections at start-up. |
| `outflare_crm_contacts_pushed_total` | `connector`, `outcome` | People journeys created or updated in a CRM (HubSpot): `created`, `updated` or `error`. |
| `outflare_ads_audience_syncs_total` | `connector`, `outcome` | People journeys added to or removed from customer list audiences on ad accounts (Meta, Google Ads): `added`, `removed`, `skipped` (no ad consent, or no email or phone) or `error`. |
| `outflare_journeys_call_duration_seconds` | `kind` | Time one try of a journey call took. |
| `outflare_journeys_entries_refused_total` | `reason` | People a journey's trigger would have started but its limits turned away: `limit` (entry limit per hour or day) or `reentry` (too soon after their last run). |
| `outflare_journeys_waiting_runs` | | Journey runs on a `wait_until` step. |
| `outflare_journeys_oldest_due_seconds` | | How long past its time the most overdue run of an active journey is. |
| `outflare_automations_excluded_total` | `where` | People an exclusion left out: `journey` (refused entry), `broadcast` (skipped), `sequence` (not enrolled). |
| `outflare_rules_active`, `outflare_rules_timers_due`, `outflare_rules_oldest_due_seconds` | `trigger` (active) | Active rules by trigger kind, and absence timers that ran out and wait to be handled, with how long the oldest has waited (normal is near 0 and under two minutes). |
| `outflare_lifecycle_due`, `outflare_lifecycle_oldest_due_seconds` | | Lifecycle inactivity checks (at risk, churned for no usage) that fell due and wait for the sweep, with how long the oldest has waited (normal is near 0 and under two minutes). |
| `outflare_record_metrics_due`, `outflare_record_metrics_oldest_due_seconds`, `outflare_record_metrics_backfilling` | | Metric values whose window moved and wait for the sweep, how long the oldest waited (normal: near 0, minutes at most), and metrics being refilled now. |
| `outflare_ad_conversions_total` | `connector`, `outcome` (`sent`, `skipped`, `failed`) | Conversions sent back to ad platforms ([paid-media.md](paid-media.md#sending-conversions-back)). |
| `outflare_ad_conversions_oldest_queued_seconds` | | How long the oldest conversion waiting to be sent has waited. |
| `outflare_ad_conversion_adjustments_total` | `connector`, `outcome` (`sent`, `skipped`, `failed`) | Refunds sent to ad platforms as changes to conversions already sent ([paid-media.md](paid-media.md#refunds)). |
| `outflare_ads_feed_sends_total` | `transport` (`data_manager`, `ads_api`), `outcome` (`sent`, `failed`) | Conversions sent to Google through the conversion feed, by the path they took ([paid-media.md](paid-media.md#sending-conversions-back)). |
| `outflare_ads_feed_batches_total` | `transport` (`data_manager`, `ads_api`, `payers_list`), `outcome` (`sent`, `failed`, `refused`) | Requests the feed made; `refused` means the Data Manager API will not take the destination and the account fell back to `ads_api`. |
| `outflare_ads_payers_list_members` | | People on every account's Outflare paying customers list, added together, as of the last refresh. |
| `outflare_meeting_events_total` | `status` (`booked`, `held`, `no_show`, `canceled`) | Meeting events recorded on people's timelines ([customers.md](customers.md#meetings-as-events)). |
| `outflare_meeting_events_pending` | | Meetings tied to a person whose latest status isn't recorded as an event yet. |
| `outflare_event_inbox_received_total` | | Product events sent to `POST /v1/brands/{brand}/events` and saved in the event inbox ([customers.md](customers.md#product-events)). |
| `outflare_event_inbox_processed_total` | `outcome` (`accepted`, `duplicate`, `rejected`, `held`) | Product events the `events.process` job finished. |
| `outflare_event_inbox_rejected_total` | `code` | Product events rejected, by the code the Events page shows (`missing_id`, `invalid_timestamp`, `property_type`, `other_brand`, `internal`, ...). |
| `outflare_event_inbox_process_seconds` | | Time to process one product event. |
| `outflare_event_inbox_pending` | | Product events waiting to be processed. |
| `outflare_event_inbox_oldest_pending_seconds` | | How long the oldest waiting product event has waited. |
| `outflare_event_inbox_purged_total` | | Event inbox rows deleted at the end of their retention (30 days accepted or duplicate, 90 rejected or held). |
| `outflare_event_inbox_purge_overdue_seconds` | | How far past its retention the oldest row still waiting for the sweep is. |
| `outflare_customer_events_name_checks_total` | `method` (`ai`, `spelling`), `outcome` (`ok`, `asked`) | New event names the `event_names.check` job compared with the names the brand already has. `asked` puts a "Is this a typo?" question on the brand's Events page. Its failures show as `event_names.check` in the job panels and alerts. |
| `outflare_privacy_exports_built_total` | `outcome` (`ready`, `too_large`, `record_gone`, `internal`, `error`) | Privacy exports the `privacy_export.build` job finished ([customers.md](customers.md#privacy-export)); `error` is an attempt that will be retried. |
| `outflare_privacy_exports_build_seconds` | | Time to build one privacy export. |
| `outflare_privacy_exports_size_bytes` | | Uncompressed size of the privacy exports built (the limit is 100 MB). |
| `outflare_privacy_exports_expired_total` | | Privacy exports whose file was deleted 7 days after it was ready. |
| `outflare_partners_clicks_total` | `outcome` (`tracked`, `inactive`, `not_found`, `error`) | Clicks on partners' short links ([partners.md](partners.md)). |
| `outflare_partners_conversions_total` | `type` (`signup`, `sale`, `refund`, `custom`), `source` (`api`, `postback`, `browser`), `outcome` (`credited`, `not_credited`, `duplicate`, `rejected`, `error`) | Partner conversions reported by workspaces' backends, postback URLs and websites' `outflare.signup()`. |
| `outflare_partners_rewards_total` | `kind` (`cash`, `fee`, `action`, `adjustment`), `event` (`created`, `approved`, `rejected`, `reversed`, `fulfilled`, `paid`) | Changes to the partner reward ledger. |
| `outflare_partners_postbacks_total` | `outcome` (`sent`, `retried`, `rejected`, `unreachable`, `invalid`) | Postbacks to partners' own trackers ([partners.md](partners.md#postbacks-to-your-partners-trackers)). |
| `outflare_partners_rewards_overdue_seconds` | | How long past its hold the oldest reward due to approve on its own has waited. |
| `outflare_partners_imports_total` | `connector` (`tolt`), `outcome` (`done`, `failed`) | Partner program imports from another platform that finished ([moving from Tolt](partners-tolt-migration.md)). |
| `outflare_partners_import_objects_total` | `kind` (`groups`, `links`, `partners`, `flows`, `promo_codes`, `customers`, `transactions`, `commissions`, `clicks`), `result` (`created`, `updated`, `unchanged`, `skipped`, `unreadable`: a whole kind left out, counted once) | Objects partner imports read, and what each became. |
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
| `outflare_reviews_page_reads_total` | `platform`, `outcome` (`success`, `pending` (still being read; the next round picks it up), `blocked`, `slow_page`, `page_failed`, `page_not_found`, `not_a_review_page`, `save_failed`, `connector_error`) | Reads of brands' [review pages](reviews.md). |
| `outflare_reviews_new_total` | `platform` | Reviews kept for the first time. |
| `outflare_reviews_pages` | `platform`, `state` (`ok`, `failing`, `no_reader`) | Active review pages, how many failed their last read, and how many are in a workspace with no page reader connected. |
| `outflare_sync_runs_total` | `module` (`analytics`, `search_console`, `page_conversions`, `organic_conversions`, `ads`, `seo`), `outcome` (`success`, `internal`, `budget_reached`, `connector_*`) | Scheduled sync runs of connections. |
| `outflare_ads_item_days_synced_total` | `level` (`ad_group`, `ad`, `keyword`, `search_term`) | Days of numbers stored for ad sets, ad groups and ads (Meta and Google), and Google keywords and search terms. |
| `outflare_ads_sync_runs_total` | `window` (`first`, `hourly`, `daily`, `weekly`, `monthly`), `outcome` | Runs of the hourly ad sync per connection, by the window each read (an account's first 90 days; 2 days every hour; 7 once a day; 35 once a week; 95 once a month) and outcome (`success` or an error code) ([paid-media.md](paid-media.md#what-is-stored)). |
| `outflare_ads_clicks_harvested_total` | `connector`, `outcome` (`stored`, or an error code) | Paid clicks read from Google's `click_view` into `ad_clicks`, one per click stored, or one per refused day ([paid-media.md](paid-media.md#what-is-stored)). |
| `outflare_ads_change_events_total`, `outflare_ads_change_event_reads_total` | `connector`; `connector`, `outcome` | Changes made on ad platforms (Google `change_event`) stored in `ad_change_events`, and the reads of each connection's history every 15 minutes with their outcome ([paid-media.md](paid-media.md#googles-own-changes)). |
| `outflare_ads_alerts_total` | `kind` | Paid ads alerts raised as new findings, by kind (`alert.spend_anomaly`, `alert.pacing`, `alert.flatline`, `alert.zero_conversions`, `alert.disapproved`, `alert.auto_apply_on`, `alert.sends_failing`, `alert.tracking_broken`, `overspend_pause`) ([paid-media.md](paid-media.md#alerts)). |
| `outflare_ads_alert_runs_total` | `outcome` (`success`, `error`) | Runs of the paid ads alert job, once per workspace after each hourly sync. |
| `outflare_ads_findings_total` | `kind`, `autonomy` (`auto`, `approve`, `alert`, `shadow`), `outcome` (`created`, `applied`, `recorded`) | Findings the paid ads rules wrote: new rows for a person, savings applied on their own, and what a rule would have done in shadow mode ([paid-media.md](paid-media.md#findings-and-decisions)). |
| `outflare_ads_verdicts_total` | `kind`, `verdict` (`worked`, `did_not_work`, `not_sure`) | Verdicts on findings whose change was made, in cost per new paying customer. |
| `outflare_ads_findings_open` | | Findings waiting for a person across every workspace, as of the last run of the rules. |
| `outflare_sync_stuck` | `module`, `code` | Scheduled reads whose last 3 or more runs failed, retries included, by the last error code: connections' syncs, SEO site competitor and backlink reads (`seo_site_competitors`, `seo_site_backlinks`), news searches (`news`) and competitor looks (`competitor_glances`, always `crawl_failed`). |
| `outflare_social_account_reads_total` | `channel`, `outcome` (`success`, `blocked`, `page_not_found`, `invalid_address`, `no_follower_count`, `save_failed`, `connector_error`) | Reads of social accounts' followers ([social](social.md)). |
| `outflare_social_stuck_accounts` | `channel`, `code` | Active social accounts whose last 3 or more reads failed, retries included, by the last error code. |
| `outflare_reviews_stuck_pages` | `platform`, `code` | Active review pages whose last 3 or more reads failed, retries included, by the last error code. |
| `outflare_reviews_oldest_read_seconds` | `platform` | Time since a read of the least recently tried active review page ended, read or failed. Pages without a connected page reader are left out. |
| `outflare_pricing_page_reads_total` | `outcome` (`success`, `blocked`, `slow_page`, `page_failed`, `page_not_found`, `no_prices_found`, `save_failed`, `connector_error`) | Weekly reads of the brand's and its competitors' [pricing pages](competitors.md#prices). |
| `outflare_pricing_oldest_read_seconds` | | Time since a read of the least recently tried pricing page ended, read or failed. Pages without a connected page reader are left out. |
| `outflare_news_search_runs_total` | `purpose` (`topic`, `coverage`), `outcome` (`success`, `connector_needed`, `usage_limit_reached`, `connector_error`, `internal`) | Runs of saved [news searches](news-watch.md). |
| `outflare_news_found_total` | `kind` (`article`, `coverage`) | Articles and coverage items news searches kept for the first time. |
| `outflare_inbound_email_received_total` | `purpose` (`support`, `ai_receipts`, `none`), `outcome` (`accepted`, `duplicate`, `unknown_address`, `bad_signature`, `too_large`, `error`) | Messages the operator's mail receiver delivered to [inbound email](inbound-email.md). |
| `outflare_inbound_email_processed_total` | `purpose`, `status` (`processed`, `ignored`, `rejected`, `failed`) | What became of delivered messages. |
| `outflare_inbound_email_received_bytes_total` | | Size of the inbound messages accepted. |
| `outflare_inbound_email_raw_purged_total` | | Raw inbound messages deleted after 30 days. |
| `outflare_exchange_rates_fetched_total` | `outcome` (`success`, `error`) | Fetches of the European Central Bank's daily exchange rates, every 6 hours on servers with inbound email. |
| `outflare_exchange_rates_newest_age_seconds` | | Age of the newest exchange rate stored, set after each fetch. |
| `outflare_geoip_downloads_total` | `file` (`City`, `ASN`), `outcome` (`success`, `not_published`, `error`) | Downloads of DB-IP's monthly City Lite and ASN Lite files, checked every 6 hours by each server that answers visitors ([website tracking](website-tracking.md#where-visitors-are)). |
| `outflare_geoip_file_age_seconds` | `file` | Age of the DB-IP file a server uses, from the day it was built; absent while the server has none. |
| `outflare_exchange_rates_receipts_converted_total` | | AI receipts in other currencies given their US dollar amount once a rate arrived. |
| `outflare_desktop_links_total` | `step` (`asked`, `approved`, `linked`) | Computers linking to Outflare for desktop. |
| `outflare_desktop_reports_total` | `outcome` (`ok`, `invalid`, `error`) | Reports from Outflare for desktop. |
| `outflare_desktop_readings_total` | `provider`, `source` (`official`, `extended`) | Usage windows the apps reported. |
| `outflare_desktop_read_failures_total` | `provider`, `code` | Tools the apps couldn't read, by the app's error code. |
| `outflare_desktop_devices` | `state` (`reporting`, `silent`) | Linked computers that reported in the last hour, and silent ones. |
| `outflare_desktop_readings_purged_total` | | Usage readings deleted after 90 days. |
| `outflare_news_searches` | `state` (`ok`, `failing`) | Active news searches, and how many failed their last run. |
| `outflare_news_search_overdue_seconds` | | How long past its time the most overdue active news search is. |
| `outflare_news_article_reads_total` | `status` (`up`, `gone`, `blocked`, `opted_out`, `unreachable`, `failed`), `journalist` | Reads of new news articles for their byline; `journalist` is `true` when one found a new journalist. |
| `outflare_news_story_ai_runs_total` | `kind` (`ideas`, `draft`, `pitch`, `reply`), `outcome` (`success`, `connector_needed`, `ai_model_needed`, `ai_budget_reached`, `connector_auth`, `ai_failed`, `check_failed`) | Runs of the workspace's own AI on [stories](stories.md): suggesting ideas, drafting releases, writing pitch openers and reading journalists' replies. |
| `outflare_stories_quote_approval_emails_total` | `outcome` (`sent`, `blocked`, `failed`, `bounced`) | Emails asking a PR expert to [approve a quote](stories.md#asking-the-expert), as their outreach sequence finished them. |
| `outflare_stories_quote_approval_pages_total` | `action` (`view`, `decide`), `outcome` (`ok`, `gone`, `invalid`, `limited`, `error`) | Requests to the public quote approval page. `gone` is a used, expired or replaced link. |
| `outflare_stories_quote_approval_decisions_total` | `decision` (`approved`, `changes_suggested`, `declined`) | Experts' decisions through their approval links. |
| `outflare_social_publishes_total` | `channel`, `outcome` (`published`, `failed`, `retried`) | Tries of the job that posts the brand's own [social posts](social.md#publishing). |
| `outflare_reddit_sends_total` | `kind`, `outcome` (`submitted`, `failed`, `unknown`) | [Reddit campaign](reddit-campaigns.md) actions sent to Engain. `unknown`: Engain didn't answer, so the action may or may not exist. |
| `outflare_reddit_checks_total` | `outcome` (`ok`, `engain_error`, `reddit_error`), `reddit_read` | Checks of sent Reddit campaign actions on Engain and on Reddit as a logged-out visitor sees them. |
| `outflare_social_publications_scheduled` | `channel`, `state` (`due`, `later`) | Approved posts not out yet: due ones wait for the publish job, later ones are scheduled ahead. |
| `outflare_social_publication_overdue_seconds` | `channel` | How long past its time the most overdue approved post is. |
| `outflare_seo_link_reads_total` | `reason` (`requested`, `dead_page`), `outcome` (`success`, `budget_reached`, `connector_auth`, `invalid_request`, `provider_error`, `internal`) | Reads of who links to a page or domain on workspaces' own SEO connections ([link building](seo.md#link-reads)). |
| `outflare_seo_link_page_checks_total` | `status` (`up`, `gone`, `redirected`, `blocked`, `opted_out`, `unreachable`, `failed`) | OutflareBot's checks of link prospects' pages. |
| `outflare_seo_archive_lookups_total` | `outcome` (`found`, `none`, `error`) | Internet Archive lookups of broken links found by page checks. |
| `outflare_seo_link_pitches_written_total` | `outcome` (`written`, `no_ai`, `ai_unavailable`, `ai_failed`) | Openers the workspace's AI wrote for link pitches. |
| `outflare_seo_link_pitches_auto_total` | `outcome` (`sent`, `unsure`, `spammy_site`, `daily_cap`, `site_in_outreach`, `opted_out`, `not_pitchable`, `sequence_archived`) | Link pitches through sequences that skip approval: sent, or why they wait. |
| `outflare_seo_link_replies_read_total` | `kind` (`agreed`, `declined`, `wants_payment`, `wants_content`, `other`, `no_ai`, `ai_unavailable`, `ai_failed`) | Replies to link pitches the workspace's AI read. |
| `outflare_seo_link_live_checks_total` | `outcome` (`live`, `not_yet`, `removed`, `still_live`, `unreachable`) | Checks of agreed and live links. |
| `outflare_seo_web_searches_total` | `outcome` (`done`, `budget_reached`, `connector_invalid`, `connector_auth`, `connector_quota`, `connector_unavailable`) | Searches on workspaces' own SERP connections (`POST /search-results`). |
| `outflare_ai_visibility_screen_answers_total` | `engine` (`chatgpt`, `gemini`, `other`), `outcome` (as web searches) | AI visibility answers read from ChatGPT's and Gemini's own screens through workspaces' own answer readers (DataForSEO's LLM Scraper). |
| `outflare_ai_visibility_demand_reads_total` | `kind` (`questions`, `volume`, `benchmark`, `other`), `outcome` (as web searches) | Reads of what people ask AI assistants through workspaces' own demand readers: real questions (DataForSEO's LLM Mentions) and AI search volume (its AI keyword data). |
| `outflare_ai_visibility_stance_reads_total` | `outcome` (`judged`, `skipped`, `ai_failed`, `refused`, `ai_model_needed`, provider codes) | AI visibility answers read by workspaces' own AI for whether they recommend, list or warn against each brand. |
| `outflare_ai_visibility_prompt_drafts_total` | `outcome` (`success`, `ai_failed`, `connector_auth`, `ai_budget_reached`, `ai_model_needed`, `connector_needed`) | Drafts of AI visibility questions by workspaces' own AI. |
| `outflare_playbooks_automation_runs_total` | `kind` (`schedule`, `event`), `outcome` (`started`, `held`, `failed`, `capped`) | Playbook automations firing: runs started, held for a person (a limit, a connection, the approver), failed (retried in 15 minutes), or left for tomorrow by the daily cap. |
| `outflare_playbooks_automation_lag_seconds` | | How long the most overdue active playbook automation has waited past its run time, at the last pass. |
| `outflare_build_info` | `version`, `role` | The running version and role (`OUTFLARE_ROLE`); the dashboard marks each change of version as a deploy. |
| `outflare_metrics_scrape_errors`, `outflare_metrics_scrape_duration_seconds` | | Database queries that failed in the last scrape, and how long it took. |
| `outflare_recovery_deleted_total` | `kind` (`record`, `field_value`, `identity`, `relationship`, `event`) | Customer data deleted into Recently deleted, restorable for 7 days ([customers.md](customers.md#deleting-and-restoring)). |
| `outflare_recovery_restored_total` | `kind`, `outcome` (`restored`, `partly_restored`, `not_restored`) | Deletions restored. |
| `outflare_recovery_purged_total` | `kind` | Deletions purged for good after their 7 days. |
| `outflare_recovery_purge_overdue_seconds` | | How far past its 7 days the oldest deletion still waiting for the purge is. |
| `outflare_undo_unmerges_total` | `outcome` (`undone`, `refused`) | Merges undone, or refused (too old to undo, a later merge to undo first, already undone) ([customers.md](customers.md#undoing-a-merge-or-a-batch)). |
| `outflare_undo_batches_total` | `mode` (`untouched`, `everything`), `outcome` (`undone`, `queued`, `failed`) | Import and ingest batches undone; queued ones run in a job. |
| `outflare_undo_rows_reverted_total` | `kind` (`unmerge`, `batch`) | Rows moved back, removed or put back by undos. |
| `outflare_undo_batch_seconds` | | Time to undo one batch. |
| `outflare_hand_edits_expired_total` | `outcome` (`switched`, `kept`, `error`) | Hand edits whose time to win ran out: another source's value took over, none had one (the next to write replaces it), or it failed ([customers.md](customers.md#where-a-value-comes-from)). |
| `outflare_hand_edits_due`, `outflare_hand_edits_oldest_due_seconds` | | Expired hand edits the 5-minute sweep hasn't handed over yet, and how long the oldest has waited. |
| `outflare_transactions_saved_total` | `from` (`api`, `connector`), `outcome` (`created`, `duplicate`, `invalid`, `error`) | Payments, refunds, chargebacks and credits sent to Outflare ([transactions.md](transactions.md)). |
| `outflare_transactions_save_seconds` | | Time to save one transaction. |
| `outflare_transactions_unconverted` | | Transactions over 2 days old still without an amount in their brand's currency. |
| `outflare_connections_unassigned` | `kind` (`connection`, `account`) | Brand connections with no brand, and analytics or Search Console sites and ad accounts nobody assigned, across every workspace, counted every ten minutes. Their data shows in no brand ([connectors.md](connectors.md#connections-and-brands)). |
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

- `OutflareTxRetriesGaveUp` (warning): a request's write (a transaction or a customer events batch) lost a deadlock three times in a row and answered 500.
- `OutflareRedirectClicksNotWritten` (warning): redirect link clicks have failed to be written for 10 minutes; people are still sent on.
- `OutflareRedirectClickBacklog` (warning): over 20,000 redirect clicks have waited in one process for 5 minutes.
- `OutflareTrackerPostbacksFailing` (warning): tracker conversion postbacks have failed to be recorded for 10 minutes.
- `OutflareTrackerSourcePostbacksFailing` (warning): conversions sent back to traffic sources have been given up on (unreachable or an invalid URL) for 30 minutes.
- `OutflareTrackerSuspectClicksHigh` (info): over half of the tracker clicks in 30 minutes were flagged suspect (a hosting network or repeat clicks).
- `OutflareTrackerWeightSuggestionsSlow` (warning): suggested weights take over 5 seconds for the slowest 5% of links and flows.
- `OutflareTrackerWeightSuggestionsFailing` (warning): suggested weights failed for 15 minutes.
- `OutflareDrilldownReportSlow` (warning): the drill-down report takes over 10 seconds for the slowest 5%.
- `OutflareDrilldownReportFailing` (warning): the drill-down report failed for 15 minutes.
- `OutflareTrackerIPLookupBacklog` (warning): a tracker click address has waited over 15 minutes for its IP lookup, so converting clicks' postbacks are held up.
- `OutflareTrackerIPLookupsUnreachable` (info): every IP lookup for 30 minutes was given up because the provider couldn't be reached.
- `OutflareTrackerHoneypotFailing` (info): hits on the landers' hidden link (`/hp`) have failed to flag their clicks for 15 minutes, so bots crawling landers aren't being flagged.
- `OutflareTrackerLanderClicksFailing` (warning): lander clicks (`/click`) have failed for 10 minutes, so visitors on landers reach the fallback page instead of an offer.
- `OutflareGeoFilesStale` (warning): a server's DB-IP city or network file is over 40 days old, so new visits and clicks get older city and network data.
- `OutflareGeoDownloadsFailing` (info): DB-IP file downloads failed at every check for a day; the files in use stay until they're stale.
- `OutflareJobKindTakesWorkers`: one job kind has held over half the job workers for 10 minutes, so other jobs and, through the shared database connections, page loads wait.
- `OutflareJobsReaped`: jobs ran past their lease (their worker died mid-job) and were put back in the queue, so they may run twice.
- `OutflareJobsReleasedAtShutdown`: jobs of one kind were still running when a shutdown's grace ended, so their handler ignores cancellation; make it return when its context ends.
- `OutflareCoverageChecksOverdue`: Coverage checks have waited more than fifteen minutes.
- `OutflareCoverageChecksFailing`: a Coverage check exhausted its retries.
- `OutflareTelegramUnreachable`: most company Telegram account checks and sign-ins couldn't reach Telegram for an hour (Telegram down, the server's outbound network, or workspaces' proxies).
- `OutflareTelegramSignedOut` (info): a company Telegram account's session ended; its workspace has to scan the QR code again.
- `OutflareTelegramSyncBehind` (warning): a company Telegram account hasn't synced its chats into the CRM for over 30 minutes.
- `OutflareTelegramSummariesFailing` (info): most AI summaries of Telegram chats are failing for an hour.
- `OutflareCoveragePagesUnreachable`: most recent Coverage page reads could not reach their site.
- `OutflarePresenceSearchesFailing`: Presence web searches fail with connector errors more often than they work.
- `OutflarePresenceCompetitorScansFailing`: scans of where competitors appear fail with connector, provider or internal errors more often than they work.
- `OutflareCompetitorSuggestionsFailing`: the daily look for new competitors (SEO connections and rankings) fails more often than it works.
- `OutflareMCPClientFailing`: over half of Outflare's calls to MCP servers failed in 30 minutes.
- `OutflareActionsWaiting`: an agent's change has waited over two days for a person.
- `OutflareActionsStuckRunning`: an agent change started running and never recorded how it ended.
- `OutflareActionsFailing`: over 50 agent changes an hour ran and got an error answer.
- `OutflareAdChangesFailing`: more than three changes to ad campaigns (pausing, resuming, budgets) failed in an hour.
- `OutflareAdsFeedSendsFailing`: over a quarter of the conversions sent through the conversion feed (Data Manager, or the Ads API fallback) failed in an hour.
- `OutflareAdsFeedFallingBack`: the Data Manager API refused an account's destination, so its sends fell back to the Ads API.
- `OutflarePayersListRefreshFailing`: the daily refresh of the paying customers lists failed more than twice in a day.
- `OutflareContentPublishesFailing` (warning): over three publications to brands' CMSs failed in an hour; each publication's error on its item says why.
- `OutflareAccountIncidentsCritical` (warning): a critical incident on a brand's external account has been open for a day; automated use of it is stopped until a person resolves it.
- `OutflareAccountTakeoverWaiting` (warning): a takeover of a brand's external account (MFA, a CAPTCHA, a passkey or a consent screen) has waited more than four hours for a person to start it.
- `OutflareBrowserTasksWaiting` (warning): a browser task has waited more than 30 minutes for a worker with a browser; none is running (`OUTFLARE_ROLE=browser`), or they are all busy.
- `OutflareFunnelsTimingOut` (warning): more than three insight queries (funnels, trends, retention, shared dashboards, alert checks or the digest) hit their 30-second limit in half an hour.
- `OutflareFunnelsFailing` (warning): more than three insight queries failed with an error in half an hour.
- `OutflareBrandKnowledgeReadsFailing` (warning): reads of brands' websites for their knowledge sheet (the sheet paid ads are written from) failed more often than they worked over a day; the `brand.knowledge_read_failed` events carry the code.
- `OutflarePaidAdsLedgerBehind` (warning): the paid ads ledger (sign-ups, payments and refunds tied to the ad click that brought them) is over 30 minutes behind for a workspace with an ad account, so cost per new paying customer is stale.
- `OutflarePaidAdsLedgerFailing` (warning): more than three customer events failed in the paid ads ledger in an hour; a workspace's run stops at the failing event and falls behind.
- `OutflareBrowserTasksFailing` (warning): more than half of browser task runs in six hours failed; the tasks' logs say where (a site that changed, no AI, no Chrome).
- `OutflareAdChangesUnverified`: over five campaign changes in six hours were applied but the platform did not show them when read back.
- `OutflareAdSuggestionsFailing`: more than three runs of the paid ads suggestions job failed in six hours, so suggestions and verdicts are not updated.
- `OutflareAdTestsFailing`: more than three steps of tests inside ad networks (starting, reading or ending a Google Ads experiment or Meta split test) failed in six hours.
- `OutflareAdDraftsFailing`: more than three steps of approved new ads (making them paused on Google Ads or Meta, or turning them on) failed in six hours.
- `OutflareAdSyncLagging` (warning): no ad sync has succeeded for over three hours though they run hourly; today's spend, the alerts and the suggestions are stale.
- `OutflareAdSyncsFailing` (warning): hourly ad syncs fail more often than they work over three hours.
- `OutflareAdClickHarvestFailing` (warning): Google refused the click harvest (`click_view`) more than three times in six hours; the harvest stops at its last day and Google keeps only 90.
- `OutflareAdChangeEventsFailing` (warning): reads of ad platforms' change histories (Google `change_event`, every 15 minutes) fail more often than they work.
- `OutflareAdAlertJobFailing` (warning): the paid ads alert job failed more than three times in three hours, so alerts are neither raised nor expired.
- `OutflareAdAlertFlood` (warning): over fifty paid ads alerts raised in an hour across every workspace; a rule is misfiring.
- `OutflareAdFindingsStale` (warning): no run of the paid ads rules has succeeded in 25 hours while ad accounts keep syncing, so findings aren't refreshed, savings aren't applied and measured changes aren't judged.
- `OutflareAdFindingVerdictsBad` (info): over a week, changes of one kind of paid ads finding did not work more often than they worked; that rule's thresholds want a look.
- `OutflareAgentRunsOverdue`: a running agent run's next turn is over 15 minutes overdue.
- `OutflareAgentRunTurnsFailing`: more agent run turns fail than finish.

`deploy/monitoring/alerts.yml` has rules for:

- the server not answering
- over 2% of requests failing with a 5xx
- p95 latency over 2 seconds
- a due job waiting over 10 minutes
- website events waiting over 5 minutes to be stored, or batches set aside
- jobs of one kind dying
- jobs put back after their lease ran out
- the pool over 90% in use or callers waiting on it
- Postgres over 80% of `max_connections`
- a transaction open over 5 minutes
- deadlocks
- a request's write giving up after losing three deadlocks in a row
- over 20,000 goroutines
- a pipeline stage with an item waiting over 30 minutes, over 10% of a stage's items retrying or failing, items failing for good, or an outside account's breaker open for 10 minutes
- the database growing over 25% in a day (once it is over 1 GB)
- database metrics that can't be read
- one tool making over 5 AI calls a second for 15 minutes (a loop spending workspaces' money)
- over 20 AI spend limits reached in an hour
- a paid connection reaching its daily or monthly usage limit (info: its workspace's admins are emailed)
- over 500 calls an hour through one connector refused at a usage limit for 30 minutes (a feature retrying into the limit)
- an extension's quota checks failing for 15 minutes, so its limits aren't applied (warning; `OutflareQuotaChecksFailing`)
- over 1,000 quota refusals an hour for one resource for 30 minutes (info; `OutflareQuotaRefusalsSurge`)
- over 20 webhook deliveries with a bad signature for one connector in 15 minutes
- over 5 webhook events failing in 30 minutes
- webhook events waiting over 5 minutes to be handled
- over half of Domain Connect lookups failing (a DNS provider's API down, or the signing key's TXT record at outflare.ai missing)
- Query unable to set up its database role
- over a quarter of queries timing out
- meaning-based search on in workspaces while the database has no pgvector
- most embedding jobs failing
- most questions asked of the knowledge base failing, or answers from it taking over 20 seconds at the 95th percentile (info)
- most support replies failing to become suggested answers for the knowledge review queue
- a smart list not refreshed for over an hour
- smart lists whose last refresh failed (info)
- an absence rule's timer due for over 15 minutes, over 10,000 rule timers waiting, or rule jobs failing and retrying
- a lifecycle inactivity check due for over 15 minutes (`OutflareLifecycleSweepBehind`), over 10,000 waiting (`OutflareLifecycleDuePiling`), or lifecycle jobs failing and retrying (`OutflareLifecycleJobsFailing`)
- metric windows not moved on for over 30 minutes (`OutflareMetricSweepBehind`), metric jobs failing and retrying (`OutflareMetricJobsFailing`), or a metric refilling for over an hour (`OutflareMetricBackfillStuck`)
- a bulk edit queued or running for over 2 hours (`OutflareBulkEditStuck`), or one failing part way (`OutflareBulkEditsFailing`)
- an expired hand edit waiting over an hour for the sweep (`OutflareHandEditsBehind`), or hand edits failing to expire (`OutflareHandEditsFailing`)
- transactions failing to save (`OutflareTransactionsFailing`), or old ones still without an amount in the brand's currency (`OutflareTransactionsUnconverted`)
- published query endpoints failing to answer (`OutflareQueryEndpointsFailing`) or slow at p95 (`OutflareQueryEndpointsSlow`)
- over 5,000 marketing emails stopped by frequency caps in an hour (info)
- a journey run due for over 15 minutes
- over half of journey webhook, Slack, sequencer or CRM calls failing (info), or taking over 10 seconds at the 95th percentile (info)
- over half of the people journeys add to one sequencer's campaigns failing in 30 minutes
- an older mailbox sign-in that could not be moved into a Gmail or Outlook connection at start-up (`OutflareMailSignInsNotMoved`)
- over half of the people journeys create or update in one CRM failing in 30 minutes
- over half of the people journeys add to or remove from one ad platform's audiences failing in 30 minutes
- over a quarter of conversions sent to one ad platform failing
- refunds sent to an ad platform as conversion adjustments failing on every try
- a conversion waiting over 30 minutes to be sent
- over 500 meetings waiting 30 minutes for their meeting event on people's timelines
- a partner reward waiting over 30 minutes past its hold to be approved
- partner conversions failing with server errors
- partner link clicks not being stored
- over half of partner postbacks failing for an hour (info: usually partners' trackers, or outbound traffic blocked)
- several partner imports from Tolt failing in an hour (info)
- the same kind unreadable from Tolt in several partner imports within 6 hours
- over 2% of feature flag requests failing
- flags taking over 250 ms to decide at the 95th percentile
- over 2% of consent banner requests failing
- a network's tracking template macros arriving unfilled for an hour (info)
- over 80% of a network's paid landings in 6 hours with no click id or ad ids (info)
- over 5% of clicks on campaign redirect links failing to be stored
- consent banner requests taking over 500 ms at the 95th percentile
- over half of cookie scans reading no page
- over 5% of browsers' cookie reports failing
- A/B test results taking over 10 seconds to count at the 95th percentile (info)
- over 5 A/B test results failing to count in 30 minutes
- a review page not read for two days
- over half of review page reads failing
- a pricing page not read for nine days
- over half of pricing page reads failing (bot checks aside)
- a news search over an hour past its time
- over half of news search runs failing with a provider or internal error
- over half of news article reads failing to reach the page
- over half of story ideas and release drafts failing at the AI call
- over half of the AI's story work (ideas, release drafts, pitch openers, reading replies) failing at the AI call
- over half of quote approval emails failing or bouncing in their sequences
- the public quote approval page failing
- an approved social post over 10 minutes past its time
- over 5 social posts failing on one channel in an hour
- over half of link reads failing for reasons other than a budget
- over half of link prospect page checks failing
- over half of Internet Archive lookups failing (info)
- review pages that keep failing after retries with an error Outflare has to fix (`slow_page`, `page_failed`, `not_a_review_page`, `save_failed`; a site blocking the page reader, `blocked`, is the reader's to get past and doesn't alert)
- review page reads that couldn't be saved
- social accounts that keep failing after retries with an error Outflare has to fix (`slow_page`, `page_failed`, `no_follower_count`, `save_failed`; a site blocking the page reader, `blocked`, is the reader's to get past and doesn't alert)
- over half of social account reads failing
- over half of link pitch openers or of replies to pitches failing to be read by the AI
- link pitches waiting behind sequences' daily limits three times faster than they go out (info)
- over half of checks of agreed and live links unable to reach the page (info)
- a connection's analytics, Search Console, conversions, ads or SEO sync failing 3 runs in a row with Outflare's own error or the provider down
- any sync run failing on Outflare's side
- and over half of web searches failing for reasons other than a budget. The thresholds suit one server
- over half of web searches failing for reasons other than a budget
- playbook automations failing to start runs for an hour with none started
- and an active playbook automation over 15 minutes past its run time. The thresholds suit one server
- inbound email deliveries refused for a bad signature (the mail receiver and the engine disagree on the inbound secret), inbound messages failing to be processed, or deliveries failing before they are stored
- the newest exchange rate over 6 days old
- reports from Outflare for desktop failing on Outflare's side, the apps unable to read one AI tool on most computers (it likely changed its endpoint), or most linked computers silent for 12 hours (info)
- over a quarter of support replies failing to send, a support reply waiting over 5 minutes for a worker, or replies taking over 20 seconds to send at the 95th percentile (info)
- the support SLA check failing or not running for 15 minutes, or taking over 5 seconds at the 95th percentile (info)
- over 5% of website chat requests failing, or many chat requests rate limited (info)
- over 20% of support assistant runs failing (`OutflareSupportAssistantFailing`), or the assistant taking over 45 seconds to decide at the 95th percentile (`OutflareSupportAssistantSlow`, info)
- searches of the website and Slack for missing answers failing most of the time (`OutflareKnowledgeAnswerSearchFailing`)
- most website imports into knowledge failing (`OutflareKnowledgeWebsiteImportsFailing`, info)
- the daily re-check of imported websites failing (`OutflareKnowledgeWebsiteRechecksBroken`, warning)
- most runs that learn answers from past support replies failing (`OutflareKnowledgeReplyLearningFailing`, info)
- most held Auto email replies refused when their hold ends (`OutflareSupportHeldRepliesRefused`, warning)
- over 5% of public help centre pages failing
- help centre views not being counted (info)
- help centre pages taking over a second to build at the 95th percentile (info)
- a privacy export failing for Outflare's own reasons (`OutflarePrivacyExportsFailing`)
- brand queries running without their record type tables (`OutflareQueryTypeTablesFailing`)
- CSV downloads of query results failing on Outflare's side (`OutflareQueryExportsFailing`)
- deleted customer data staying over 6 hours past its 7 days in Recently deleted (`OutflareRecoveryPurgeBehind`: the purge is failing)
- a batch undo failing in the background (`OutflareBatchUndoFailing`)
- connections or their sites and ad accounts with no brand for 3 days (`OutflareConnectionsUnassigned`, info), or their count not reported for an hour (`OutflareConnectionsUnassignedCountMissing`)
- adjust them in your copy

For product events sent to brands (the event inbox), alerts fire when an event has waited over 5 minutes to be processed (warning), when over half of the events processed in an hour were rejected (info: a sender broke; the rejected-by-code panel and each brand's Events page say what to fix), and when rows stay over 6 hours past their retention (info: the sweep is failing).

For [Reddit campaigns](reddit-campaigns.md), alerts fire when over 5 actions fail to reach Engain in an hour, and when over half of the checks of sent actions fail over 6 hours.

Data network alerts: a withdrawal past its 30-day deadline (critical); a member's sharing over 2 days behind; most sharing jobs failing; over a quarter of lookups failing; and over 500 lookups an hour hitting a domain's daily limit (info: someone may be guessing names).

## Metrics from extensions

An extension registers its own Prometheus collectors with `Host.Metrics()`, so they are served on the same metrics address and reach the same Prometheus. Name them `outflare_<extension>_*`. The dashboards and alerts for them ship with the extension, not here.

- **OutflareReviewRequestsBehind**: review invitation dispatch is over five minutes behind.
- **OutflareReviewRequestsFailing**: review invitation batches repeatedly retry or die.
- **OutflareAIScreenAnswersFailing**: over half of AI visibility answers from ChatGPT's and Gemini's screens fail for reasons other than a budget.
- **OutflareAIDemandReadsFailing**: over half of reads of real AI questions and AI search volume fail for reasons other than a budget.
- **OutflareAIStanceReadsFailing**: over half of AI visibility answers can't be read for their stances.
- **OutflareAIPromptDraftsFailing**: over half of AI visibility question drafts fail.

The Review invitations dashboard row shows dispatch results, due-job delay and batch duration, using the common job metrics.

- `OutflareCoverageReviewsFailing`: repeated Coverage AI review failures need provider or response investigation.

Coverage records page outcomes in `outflare_coverage_checks_total{health}` and accuracy outcomes in `outflare_coverage_reviews_total{status}`. Presence discovery counts new finds in `outflare_coverage_found_total{source}` and web search runs in `outflare_coverage_search_runs_total{outcome}`. Scans of where competitors appear count each source's outcome in `outflare_coverage_competitor_scans_total{source,outcome}` and new pages in `outflare_coverage_competitor_found_total{arrangement}`; opportunities suggested and won are `outflare_coverage_opportunities_total{event}`. Page checks, daily discovery, web searches, accuracy reviews and commercial extraction use the shared job duration, backlog and error metrics. Fixed accuracy schedules reuse unchanged analyses before calling a model.

Partner task ownership, reach and evidence checks use the `partners.task_check`
queue kind. The partner panels show outcome rate and oldest due age; future
human-review holds do not count as overdue. `OutflarePartnerTaskChecksFailing`
warns about repeated retries or dead checks, and `OutflareJobBacklog` covers
checks waiting for workers.

Personal CRM Google capture uses the existing per-kind queue throughput, duration, lag and retry metrics (`crm_mail.google_sync`). Its dashboard panel shows outcomes; `OutflarePersonalGoogleCaptureFailing` and `OutflarePersonalGoogleCaptureDelayed` flag repeated failures or a delayed queue. No user, account or email data is placed in metric labels.

The email privacy check (`crm_mail.privacy_check`) counts each email it reads in `outflare_crm_mail_privacy_checks_total{outcome}` (work, private, failed, no_ai, budget), on a panel beside Gmail capture.

- `OutflareWorkspaceCalendarCaptureFailing` (warning): reading Workspace members' calendars into the CRM (`crm_calendar.workspace_sync`) keeps retrying or dying; check the connection's delegation for `calendar.readonly`. Its jobs share the Gmail capture panel.
- `OutflareMailPrivacyChecksFailing` (warning): over twenty email privacy checks failed, found no AI model or hit the AI limit in an hour, so colleagues stop seeing new work email.
