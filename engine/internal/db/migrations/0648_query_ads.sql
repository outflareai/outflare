-- Paid ads, attribution and the click tracker in Query (AGENTS.md, "Data:
-- agents can see all of it"), so an agent can join cause (spend, campaigns,
-- keywords, clicks) to effect (sign-ups, payments) in one query: "cost per
-- first payment by campaign", "search terms that spend and never convert",
-- "which tracker offers pay back". Like every query view, each shows one
-- workspace's rows, the brands the caller may see and, in a brand's query,
-- that brand's alone, and needs the analytics.read scope the API asks for
-- the same data. Secrets and sign-in material are left out: account
-- configuration (meta, feed), postback URLs and parameters a traffic
-- source may carry keys in, hashed match keys and IP hashes. A row whose
-- own brand is empty follows its ad account's brand.

-- Ad accounts, campaigns, ad groups and ads.
CREATE VIEW query.ad_accounts WITH (security_barrier) AS
SELECT a.id, a.brand_id, a.integration_id, a.connector, a.external_id, a.name, a.currency, a.time_zone, a.status,
    a.sync_on, a.first_day, a.last_day, a.last_synced_at, a.last_error, a.target_cost_per_conversion_micros,
    a.created_at, a.updated_at
FROM ad_accounts a JOIN query_scopes s ON s.pid = pg_backend_pid() AND a.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR a.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR a.brand_id = s.brand);

CREATE VIEW query.ad_entities WITH (security_barrier) AS
SELECT e.id, COALESCE(e.brand_id, a.brand_id) AS brand_id, e.ad_account_id, e.level, e.parent_id, e.external_id,
    e.name, e.status, e.provider_status, e.objective, e.daily_budget_micros, a.currency, e.bidding_strategy,
    e.platform_target_cpa_micros, e.platform_target_roas, e.target_cost_per_conversion_micros, e.details,
    e.created_at, e.updated_at
FROM ad_entities e
JOIN ad_accounts a ON a.id = e.ad_account_id AND a.workspace_id = e.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(e.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(e.brand_id, a.brand_id) = s.brand);

-- What each campaign, ad group and ad did each day, as the network reports it.
CREATE VIEW query.ad_metrics_daily WITH (security_barrier) AS
SELECT m.ad_entity_id, COALESCE(m.brand_id, e.brand_id, a.brand_id) AS brand_id, e.ad_account_id, e.level, m.day,
    m.currency, m.spend_micros, m.impressions, m.clicks, m.conversions, m.conversion_value_micros,
    m.all_conversions, m.all_conversion_value_micros, m.reach, m.link_clicks, m.landing_page_views, m.video_views,
    m.updated_at
FROM ad_metrics_daily m
JOIN ad_entities e ON e.id = m.ad_entity_id AND e.workspace_id = m.workspace_id
JOIN ad_accounts a ON a.id = e.ad_account_id AND a.workspace_id = e.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(m.brand_id, e.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(m.brand_id, e.brand_id, a.brand_id) = s.brand);

CREATE VIEW query.ad_conversion_actions_daily WITH (security_barrier) AS
SELECT c.ad_entity_id, COALESCE(c.brand_id, e.brand_id, a.brand_id) AS brand_id, c.day, c.action, c.conversions,
    c.value_micros, a.currency
FROM ad_conversion_actions_daily c
JOIN ad_entities e ON e.id = c.ad_entity_id AND e.workspace_id = c.workspace_id
JOIN ad_accounts a ON a.id = e.ad_account_id AND a.workspace_id = e.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(c.brand_id, e.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(c.brand_id, e.brand_id, a.brand_id) = s.brand);

-- Keywords, their numbers by day, and the search terms people typed.
CREATE VIEW query.ad_keywords WITH (security_barrier) AS
SELECT k.id, COALESCE(k.brand_id, a.brand_id) AS brand_id, k.ad_account_id, k.ad_group_id, k.external_id, k.text,
    k.match_type, k.status, k.quality_score, k.first_page_cpc_micros, k.top_of_page_cpc_micros,
    k.first_position_cpc_micros, a.currency, k.updated_at
FROM ad_keywords k
JOIN ad_accounts a ON a.id = k.ad_account_id AND a.workspace_id = k.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND k.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(k.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(k.brand_id, a.brand_id) = s.brand);

CREATE VIEW query.ad_keyword_metrics_daily WITH (security_barrier) AS
SELECT m.keyword_id, COALESCE(m.brand_id, k.brand_id, a.brand_id) AS brand_id, m.day, m.currency, m.spend_micros,
    m.impressions, m.clicks, m.conversions, m.conversion_value_micros
FROM ad_keyword_metrics_daily m
JOIN ad_keywords k ON k.id = m.keyword_id AND k.workspace_id = m.workspace_id
JOIN ad_accounts a ON a.id = k.ad_account_id AND a.workspace_id = k.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(m.brand_id, k.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(m.brand_id, k.brand_id, a.brand_id) = s.brand);

CREATE VIEW query.ad_search_terms_daily WITH (security_barrier) AS
SELECT COALESCE(t.brand_id, a.brand_id) AS brand_id, t.ad_account_id, t.ad_group_id, t.day, t.term, t.search_term_match_type,
    t.keyword_text, t.keyword_match_type, t.currency, t.spend_micros, t.impressions, t.clicks, t.conversions,
    t.conversion_value_micros
FROM ad_search_terms_daily t
JOIN ad_accounts a ON a.id = t.ad_account_id AND a.workspace_id = t.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND t.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(t.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(t.brand_id, a.brand_id) = s.brand);

-- Ad clicks Outflare saw land (a gclid, fbclid and the like), and the
-- people they brought: which campaign, ad group, ad and keyword, when
-- they signed up and first paid.
CREATE VIEW query.ad_clicks WITH (security_barrier) AS
SELECT COALESCE(c.brand_id, a.brand_id) AS brand_id, c.ad_account_id, c.click_id, c.kind, c.day,
    c.campaign_external_id, c.ad_group_external_id, c.ad_external_id, c.keyword, c.match_type, c.device
FROM ad_clicks c
JOIN ad_accounts a ON a.id = c.ad_account_id AND a.workspace_id = c.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(c.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(c.brand_id, a.brand_id) = s.brand);

CREATE VIEW query.ad_acquisitions WITH (security_barrier) AS
SELECT q.id, q.brand_id, q.record_id, q.connector, q.ad_account_id, q.campaign_entity_id, q.ad_group_entity_id,
    q.ad_entity_id, q.campaign_external_id, q.ad_group_external_id, q.ad_external_id, q.keyword, q.click_id_kind,
    q.click_id, q.session_id, q.clicked_at, q.matched_by, q.window_days, q.signed_up_at, q.first_paid_at,
    q.first_payment_minor, q.currency, q.ad_consent, q.created_at, q.updated_at
FROM ad_acquisitions q JOIN query_scopes s ON s.pid = pg_backend_pid() AND q.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR q.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR q.brand_id = s.brand);

CREATE VIEW query.ad_revenue WITH (security_barrier) AS
SELECT r.event_id, r.brand_id, r.record_id, r.acquisition_id, r.touch_campaign_entity_id, r.touch_connector, r.bucket,
    r.value_minor, r.currency, r.occurred_at, r.created_at
FROM ad_revenue r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR r.brand_id = s.brand);

-- Changes made to the ad accounts (by people, agents and rules), what was
-- sent back to the networks as conversions, audiences and targets.
CREATE VIEW query.ad_changes WITH (security_barrier) AS
SELECT c.id, COALESCE(c.brand_id, e.brand_id, a.brand_id) AS brand_id, c.ad_entity_id, c.kind, c.before, c.after,
    c.status, c.error, c.verified_at, c.verify_note, c.actor_id, c.action_id, c.batch_id, c.created_at, c.applied_at
FROM ad_changes c
JOIN ad_entities e ON e.id = c.ad_entity_id AND e.workspace_id = c.workspace_id
JOIN ad_accounts a ON a.id = e.ad_account_id AND a.workspace_id = e.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(c.brand_id, e.brand_id, a.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(c.brand_id, e.brand_id, a.brand_id) = s.brand);

CREATE VIEW query.ad_conversion_sends WITH (security_barrier) AS
SELECT c.id, c.brand_id, c.rule_id, c.record_id, c.occurred_at, c.status, c.reason, c.value_micros, c.currency,
    c.country, c.country_source, c.transport, c.attempts, c.sent_at, c.created_at, c.updated_at
FROM ad_conversion_sends c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE VIEW query.ad_audiences WITH (security_barrier) AS
SELECT d.id, d.brand_id, d.name, d.list_id, d.enabled, d.exclude, d.created_at, d.updated_at
FROM ad_audiences d JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR d.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR d.brand_id = s.brand);

CREATE VIEW query.ad_brand_economics WITH (security_barrier) AS
SELECT b.brand_id, b.currency, b.target_cost_per_payer_micros, b.target_cost_per_signup_micros, b.acquisition_window_days,
    b.payback_months, b.gross_margin_percent, b.daily_budget_ceiling_micros, b.shadow_mode, b.updated_at
FROM ad_brand_economics b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR b.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR b.brand_id = s.brand);

-- Attribution: each conversion, and the credit each touch before it got
-- under each model.
CREATE VIEW query.attribution_conversions WITH (security_barrier) AS
SELECT c.event_id, c.brand_id, c.record_id, c.definition_id, c.name, c.occurred_at, c.value_minor, c.currency,
    c.touches, c.matched_by, c.credited_at
FROM attribution_conversions c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE VIEW query.attribution_credits WITH (security_barrier) AS
SELECT c.event_id, c.brand_id, c.model, c.position, c.session_id, c.touch_at, c.channel, c.source, c.campaign,
    c.tracking_campaign_id, c.tag_method, c.weight
FROM attribution_credits c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE VIEW query.attribution_models WITH (security_barrier) AS
SELECT m.id, m.brand_id, m.key, m.version, m.name, m.spec, m.created_at, m.archived_at
FROM attribution_models m JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR m.brand_id IS NULL OR m.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR m.brand_id IS NULL OR m.brand_id = s.brand);

-- Tracking campaigns and links, and every click on them.
CREATE VIEW query.tracking_campaigns WITH (security_barrier) AS
SELECT c.id, c.brand_id, c.name, c.slug, c.code, c.channel, c.source, c.medium, c.object_kind, c.object_id, c.landing_url,
    c.status, c.redirect_links, c.redirect_mode, c.traffic_source_id, c.cost_model, c.cost_amount_micros,
    c.cost_share_bp, c.cost_currency, c.created_at, c.updated_at
FROM tracking_campaigns c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE VIEW query.tracking_links WITH (security_barrier) AS
SELECT l.id, COALESCE(l.brand_id, c.brand_id) AS brand_id, l.campaign_id, l.flow_id, l.url, l.content, l.term,
    l.redirect_code, l.created_at
FROM tracking_links l
JOIN tracking_campaigns c ON c.id = l.campaign_id AND c.workspace_id = l.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR COALESCE(l.brand_id, c.brand_id) = ANY (s.brands))
    AND (s.brand IS NULL OR COALESCE(l.brand_id, c.brand_id) = s.brand);

CREATE VIEW query.tracking_link_clicks WITH (security_barrier) AS
SELECT k.id, k.brand_id, k.link_id, k.click_key, k.clicked_at, k.landed_at, k.session_id, k.likely_bot, k.direct,
    k.fraud_reasons, k.sub_id, k.source_click_id, k.referrer_host, k.cost_micros, k.network_cost_micros,
    k.destination_id, k.path_id, k.offer_id, k.lander_id, k.country, k.region, k.city, k.asn, k.isp, k.device,
    k.device_brand, k.device_model, k.os, k.os_version, k.browser, k.browser_version, k.language
FROM tracking_link_clicks k JOIN query_scopes s ON s.pid = pg_backend_pid() AND k.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND k.brand_id IS NOT NULL
    AND (s.brands IS NULL OR k.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR k.brand_id = s.brand);

CREATE VIEW query.tracking_destination_days WITH (security_barrier) AS
SELECT d.destination_id, d.brand_id, d.day, d.clicks
FROM tracking_destination_days d JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR d.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR d.brand_id = s.brand);

-- The click tracker: traffic sources, offers, landers, flows, their
-- clicks and conversions, and what the networks charged.
CREATE VIEW query.tracker_traffic_sources WITH (security_barrier) AS
SELECT t.id, t.brand_id, t.name, t.template, t.currency, t.status, t.postback_share, t.postback_types,
    t.postback_statuses, t.created_at, t.updated_at
FROM tracker_traffic_sources t JOIN query_scopes s ON s.pid = pg_backend_pid() AND t.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR t.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR t.brand_id = s.brand);

CREATE VIEW query.tracker_offers WITH (security_barrier) AS
SELECT o.id, o.brand_id, o.source_id, o.name, o.url, o.payout_mode, o.payout_micros, o.currency, o.country_payouts,
    o.conversion_type, o.daily_cap, o.cap_kind, o.fallback_offer_id, o.status, o.check_status, o.checked_at,
    o.broken_since, o.created_at, o.updated_at
FROM tracker_offers o JOIN query_scopes s ON s.pid = pg_backend_pid() AND o.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR o.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR o.brand_id = s.brand);

CREATE VIEW query.tracker_offer_days WITH (security_barrier) AS
SELECT d.offer_id, d.brand_id, d.day, d.clicks, d.conversions
FROM tracker_offer_days d JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR d.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR d.brand_id = s.brand);

CREATE VIEW query.tracker_landers WITH (security_barrier) AS
SELECT l.id, l.brand_id, l.name, l.url, l.ctas, l.status, l.check_status, l.checked_at, l.broken_since, l.created_at,
    l.updated_at
FROM tracker_landers l JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR l.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR l.brand_id = s.brand);

CREATE VIEW query.tracker_lander_clicks WITH (security_barrier) AS
SELECT c.id, c.brand_id, c.lander_id, c.click_id, c.click_key, c.cta, c.destination_id, c.offer_id, c.likely_bot,
    c.clicked_at
FROM tracker_lander_clicks c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE VIEW query.tracker_flows WITH (security_barrier) AS
SELECT f.id, f.brand_id, f.name, f.status, f.created_at, f.updated_at
FROM tracker_flows f JOIN query_scopes s ON s.pid = pg_backend_pid() AND f.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR f.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR f.brand_id = s.brand);

CREATE VIEW query.tracker_conversions WITH (security_barrier) AS
SELECT c.id, c.brand_id, c.source_id, c.click_id, c.click_key, c.campaign_id, c.link_id, c.offer_id, c.record_id,
    c.clicked_at, c.converted_at, c.transaction_id, c.type, c.status, c.payout_micros, c.currency, c.postbacks,
    c.fraud_reasons, c.created_at, c.updated_at
FROM tracker_conversions c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE VIEW query.tracker_network_costs WITH (security_barrier) AS
SELECT c.id, c.brand_id, c.source_id, c.network, c.campaign_ref, c.day, c.currency, c.spend_micros, c.matched_micros,
    c.spread_micros, c.unattributed_micros, c.clicks, c.targets, c.synced_at
FROM tracker_network_costs c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

-- Spend now says which ad (campaign, ad group or ad) a cost was for, so
-- spend joins ad_entities. A new column goes last, so the view keeps its
-- columns for the version before. Like every customer view since 0342, it
-- also keeps to the query's brand now.
CREATE OR REPLACE VIEW query.spend WITH (security_barrier) AS
SELECT e.id, e.brand_id, e.sequence_id, e.mission_id, e.channel, e.category, e.provider, e.units, e.unit_name,
    e.amount_cents, e.currency, e.source, e.occurred_at, e.ad_entity_id
FROM spend_entries e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR e.brand_id = s.brand);
