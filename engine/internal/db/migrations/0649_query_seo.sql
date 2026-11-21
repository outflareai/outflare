-- Website, search, SEO, AI visibility and competitors in Query (AGENTS.md,
-- "Data: agents can see all of it"), so an agent can join what search
-- engines and AI assistants show (rankings, Search Console clicks, AI
-- answers, mentions and citations, crawls, competitors' new pages) to what
-- the brand did and what came of it (visits, sign-ups, payments): "pages
-- that lost clicks after the last update", "prompts where a competitor is
-- named and we are not". Like every query view, each shows one workspace's
-- rows, the brands the caller may see and, in a brand's query, that brand's
-- alone, and needs the scope the API asks for the same data: seo.read for
-- search, SEO and AI visibility, analytics.read for site analytics,
-- customers.read for session recordings (deleted ones are left out). A row
-- whose own brand is empty follows its site's brand. Site verification
-- tokens and DNS set-up are left out.

CREATE VIEW query.search_daily WITH (security_barrier) AS
SELECT x.integration_id, x.site, x.host, x.day, x.page, x.query, x.clicks, x.impressions, x.ctr, x.position,
    x.brand_id
FROM search_daily x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.search_segments WITH (security_barrier) AS
SELECT x.brand_id, x.integration_id, x.site, x.host, x.day, x.page, x.country, x.device, x.clicks,
    x.impressions, x.position
FROM search_segments x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.search_vitals WITH (security_barrier) AS
SELECT COALESCE(x.brand_id, st.brand_id) AS brand_id, x.site_id, x.page, x.device, x.day, x.metric, x.p75,
    x.good_share
FROM search_vitals x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.search_sitemaps WITH (security_barrier) AS
SELECT x.integration_id, x.site, x.host, x.path, x.last_submitted, x.last_read, x.pending, x.is_index,
    x.errors, x.warnings, x.submitted, x.indexed, x.checked_at, x.brand_id
FROM search_sitemaps x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.search_ga4_outcomes WITH (security_barrier) AS
SELECT x.brand_id, x.integration_id, x.site, x.day, x.host, x.path, x.source, x.sessions, x.conversions,
    x.revenue_micros, x.currency
FROM search_ga4_outcomes x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.search_algorithm_updates WITH (security_barrier) AS
SELECT COALESCE(x.brand_id, st.brand_id) AS brand_id, x.site_id, x.id, x.name, x.started_at, x.ended_at
FROM search_algorithm_updates x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.url_inspections WITH (security_barrier) AS
SELECT x.integration_id, x.site, x.host, x.url, x.verdict, x.coverage, x.indexing, x.robots_txt,
    x.page_fetch, x.google_canonical, x.user_canonical, x.last_crawl, x.crawled_as, x.sitemaps, x.mobile,
    x.rich_results, x.link, x.previous_verdict, x.verdict_changed_at, x.inspected_at, x.brand_id
FROM url_inspections x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.indexability_snapshots WITH (security_barrier) AS
SELECT x.site_id, x.day, x.pages, x.crawled, x.indexable, x.in_sitemap, x.indexed, x.likely_indexed,
    x.not_indexed, x.unknown, x.issues, x.taken_at, COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM indexability_snapshots x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.seo_sites WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.host, x.start_url, x.verified_at, x.verify_method, x.verify_checked_at,
    x.max_pages, x.crawl_delay_ms, x.obey_robots, x.schedule, x.status, x.created_by, x.created_at,
    x.updated_at, x.verify_watch_until, x.render_js, x.use_sitemaps, x.follow_nofollow, x.ignore_params,
    x.max_depth, x.include_patterns, x.exclude_patterns, x.schedule_hour, x.schedule_day, x.schedule_tz,
    x.registered_on, x.registered_checked_at
FROM seo_sites x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.seo_snapshots WITH (security_barrier) AS
SELECT x.integration_id, x.connector, x.host, x.report, x.day, x.country, x.key, x.url, x.kind, x.metrics,
    x.synced_at, x.brand_id
FROM seo_snapshots x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.seo_regressions WITH (security_barrier) AS
SELECT x.id, x.site_id, x.kind, x.subject, x.before, x.after, x.detail, x.status, x.detected_at,
    x.last_seen_at, x.resolved_at, COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM seo_regressions x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.seo_competitors WITH (security_barrier) AS
SELECT x.id, x.site_id, COALESCE(x.brand_id, st.brand_id) AS brand_id, x.host, x.name, x.aliases, x.status,
    x.source, x.common_keywords, x.record_id, x.title, x.description, x.pages, x.checked_at, x.check_error,
    x.created_by, x.created_at, x.updated_at, x.check_failures, x.presence_every_days, x.presence_queued_at,
    x.presence_scanned_at, x.presence_outcome, x.links, x.registered_on, x.registered_checked_at
FROM seo_competitors x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.tracked_keywords WITH (security_barrier) AS
SELECT x.id, x.site_id, x.keyword, x.country, x.device, x.engine, x.integration_id, x.frequency, x.status,
    x.last_checked_at, x.last_error, x.created_by, x.created_at, x.updated_at, x.failures, x.last_failed_at,
    COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM tracked_keywords x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.rank_observations WITH (security_barrier) AS
SELECT x.id, x.tracked_id, x.day, x.position, x.url, x.depth, x.engine, x.top, x.cost_usd, x.integration_id,
    x.checked_at, x.brand_id
FROM rank_observations x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.keyword_serps WITH (security_barrier) AS
SELECT x.id, COALESCE(x.brand_id, st.brand_id) AS brand_id, x.site_id, x.keyword, x.country, x.device,
    x.engine, x.items, x.integration_id, x.cost_usd, x.read_at
FROM keyword_serps x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.crawl_runs WITH (security_barrier) AS
SELECT x.id, x.site_id, x.host, x.status, x.trigger, x.reason, x.error_code, x.max_pages, x.delay_ms,
    x.obey_robots, x.step, x.consecutive_errors, x.pages_fetched, x.pages_blocked, x.pages_failed,
    x.pages_found, x.bytes, x.created_by, x.created_at, x.started_at, x.finished_at, x.pages_fallback,
    x.pages_firewalled, x.fallback_hint, x.health_score, x.issues_errors, x.issues_warnings,
    x.issues_notices, x.llms_txt_status, x.llms_txt_bytes, x.settings, x.pages_excluded, x.pages_rendered,
    COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM crawl_runs x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.crawl_pages WITH (security_barrier) AS
SELECT x.id, x.run_id, x.url, x.depth, x.source, x.found_on, x.status, x.reason, x.http_status,
    x.content_type, x.bytes, x.duration_ms, x.redirect_to, x.noindex, x.nofollow, x.links_out, x.fetched_at,
    x.title, x.title_count, x.meta_description, x.canonical, x.canonical_count, x.h1, x.lang, x.hreflang,
    x.schema_types, x.word_count, x.images, x.images_no_alt, x.content_hash, x.viewport, x.x_robots_tag,
    x.links_in, x.fetched_via, x.js_shell, x.og_title, x.og_image, x.insecure_resources, x.in_sitemap,
    x.brand_id, x.content_links_in, x.ai_signals
FROM crawl_pages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crawl_issues WITH (security_barrier) AS
SELECT x.id, x.run_id, x.page_id, x.rule, x.severity, x.template, x.detail, x.brand_id
FROM crawl_issues x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crawl_links WITH (security_barrier) AS
SELECT x.id, x.run_id, x.from_page, x.to_url, x.anchor, x.internal, x.nofollow, x.brand_id, x.boilerplate
FROM crawl_links x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.crawl_ai_bots WITH (security_barrier) AS
SELECT x.run_id, x.token, x.purpose, x.root_allowed, x.pages, x.blocked_pages, x.brand_id
FROM crawl_ai_bots x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.backlink_events WITH (security_barrier) AS
SELECT x.id, x.site_id, x.backlink_id, x.kind, x.at, x.connector, x.run_id, x.source_rating, x.reason,
    x.created_at, COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM backlink_events x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.competitor_pages WITH (security_barrier) AS
SELECT x.competitor_id, x.url, x.lastmod, x.first_seen_at, x.last_seen_at, x.gone_at, x.brand_id
FROM competitor_pages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.competitor_events WITH (security_barrier) AS
SELECT x.id, x.site_id, x.competitor_id, x.kind, x.connector, x.data, x.at,
    COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM competitor_events x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.page_conversions WITH (security_barrier) AS
SELECT x.integration_id, x.site, x.day, x.host, x.path, x.sessions, x.visitors, x.conversions,
    x.search_sessions, x.search_conversions, x.brand_id
FROM page_conversions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.page_targets WITH (security_barrier) AS
SELECT x.id, x.site_id, x.url, x.keyword, x.secondary, x.status, x.origin, x.created_by, x.created_at,
    x.updated_at, COALESCE(x.brand_id, st.brand_id) AS brand_id
FROM page_targets x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.link_pitches WITH (security_barrier) AS
SELECT x.id, x.prospect_id, x.sequence_id, x.record_id, x.lead_id, x.status, x.opener, x.reason,
    x.reply_kind, x.replied_at, x.approved_by, x.approved_at, x.created_by, x.created_at, x.updated_at,
    x.brand_id
FROM link_pitches x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_prompts WITH (security_barrier) AS
SELECT x.id, x.cohort_id, x.text, x.intent, x.status, x.created_by, x.created_at, x.updated_at, x.brand_id,
    x.awareness, x.question_type, x.persona, x.topic, x.competitor, x.demand, x.demand_source, x.source, x.why
FROM ai_prompts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_runs WITH (security_barrier) AS
SELECT x.id, x.cohort_id, x.trigger, x.status, x.reason, x.error_code, x.models, x.attempts,
    x.samples_planned, x.samples_done, x.samples_failed, x.cost_cents, x.step, x.created_by, x.created_at,
    x.started_at, x.finished_at, x.brand_id, x.cost_micros
FROM ai_runs x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_samples WITH (security_barrier) AS
SELECT x.id, x.run_id, x.cohort_id, x.prompt_id, x.brand_id, x.provider, x.integration_id, x.model,
    x.web_search, x.attempt, x.status, x.error_code, x.answer, x.input_tokens, x.output_tokens, x.cost_cents,
    x.duration_ms, x.sampled_at, x.facts_status, x.facts_cost_cents, x.surface, x.cost_micros,
    x.answer_model, x.searches, x.brands_named, x.check_url, x.stance_status, x.other_brands,
    x.stance_cost_cents
FROM ai_samples x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_mentions WITH (security_barrier) AS
SELECT x.sample_id, x.cohort_id, x.entity, x.name, x.position, x.count, x.sampled_at, x.brand_id, x.stance,
    x.stance_quote
FROM ai_mentions x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_citations WITH (security_barrier) AS
SELECT x.sample_id, x.cohort_id, x.url, x.host, x.title, x.position, x.owner, x.competitor, x.sampled_at,
    x.brand_id, x.source_type
FROM ai_citations x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_named_brands WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.name_key, x.kind, x.label, x.host, x.sure, x.competitor_id, x.checked_at,
    x.created_at
FROM ai_named_brands x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_cohorts WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.name, x.description, x.locale, x.aliases, x.competitors, x.schedule, x.status,
    x.created_by, x.created_at, x.updated_at
FROM ai_cohorts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_brand_facts WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.topic, x.statement, x.status, x.created_by, x.created_at, x.updated_at
FROM ai_brand_facts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_fact_checks WITH (security_barrier) AS
SELECT x.sample_id, x.fact_id, x.cohort_id, x.run_id, x.verdict, x.quote, x.sampled_at, x.checked_at,
    x.brand_id
FROM ai_fact_checks x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_claims WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.cohort_id, x.prompt_id, x.sample_id, x.entity, x.name, x.kind, x.text, x.key,
    x.sentiment, x.quote, x.sampled_at
FROM ai_claims x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_bot_hits WITH (security_barrier) AS
SELECT x.integration_id, x.host, x.day, x.token, x.requests, x.synced_at, x.brand_id
FROM ai_bot_hits x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_bot_reports WITH (security_barrier) AS
SELECT x.brand_id, x.host, x.day, x.token, x.requests, x.updated_at
FROM ai_bot_reports x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_page_checks WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.url, x.status, x.title, x.found, x.requested_at, x.checked_at
FROM ai_page_checks x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.ai_benchmarks WITH (security_barrier) AS
SELECT x.brand_id, x.cohort_id, x.day, x.entity, x.name, x.platform, x.mentions, x.ai_search_volume,
    x.created_at
FROM ai_benchmarks x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.analytics_daily WITH (security_barrier) AS
SELECT x.integration_id, x.connector, x.site, x.host, x.report, x.day, x.key, x.visitors, x.pageviews,
    x.sessions, x.bounce_rate, x.avg_duration, x.clicks, x.impressions, x.ctr, x.position, x.events,
    x.conversions, x.synced_at, x.brand_id
FROM analytics_daily x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.page_vitals WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.key_id, x.host, x.path, x.device, x.country, x.lcp_ms, x.inp_ms, x.cls, x.fcp_ms,
    x.ttfb_ms, x.occurred_at
FROM page_vitals x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.heatmap_clicks WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.path, x.day, x.device, x.x, x.y, x.clicks
FROM heatmap_clicks x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.heatmap_scrolls WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.path, x.day, x.device, x.depth, x.views, x.height
FROM heatmap_scrolls x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.experiments WITH (security_barrier) AS
SELECT x.id, x.site_id, COALESCE(x.brand_id, st.brand_id) AS brand_id, x.name, x.hypothesis, x.design,
    x.pattern, x.metric, x.holdout_percent, x.baseline_days, x.measure_days, x.start_on, x.status,
    x.changed_at, x.deploy_id, x.result, x.concluded_at, x.created_by, x.created_at, x.updated_at
FROM experiments x
LEFT JOIN seo_sites st ON st.id = x.site_id AND st.workspace_id = x.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(x.brand_id, st.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(x.brand_id, st.brand_id) = s.brand);

CREATE VIEW query.experiment_pages WITH (security_barrier) AS
SELECT x.experiment_id, x.url, x.grp, x.baseline, x.brand_id
FROM experiment_pages x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.site_visitors WITH (security_barrier) AS
SELECT x.anonymous_id, x.record_id, x.account_id, x.brand_id, x.identified_by, x.first_seen_at,
    x.last_seen_at, x.pageviews, x.key_pageviews, x.sessions, x.last_path, x.reveal_checked_at, x.network,
    x.company_domain, x.company_name
FROM site_visitors x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE VIEW query.session_recordings WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.session_id, x.anonymous_id, x.started_at, x.last_at, x.parts, x.bytes, x.clicks,
    x.paths, x.device, x.browser, x.os, x.country, x.width, x.height, x.viewed_at, x.created_at, x.host
FROM session_recordings x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand)
    AND x.deleted_at IS NULL;
