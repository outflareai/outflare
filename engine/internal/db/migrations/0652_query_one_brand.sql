-- The older query views keep to one brand too (AGENTS.md, "Brands own
-- their data"). A query for a brand has shown only that brand's rows in
-- every customer view since 0342; these ten views, made before or beside
-- it, still showed every brand the caller may see, so a brand's visits,
-- backlinks, reviews, posts, link orders and organic conversions sat next
-- to its sister brands'. Each now also keeps to the query's brand, on the
-- row's own brand_id; a row with no brand is not that brand's.
--
-- Rollback-safe: only the WHERE clauses change, the columns are the same,
-- and a query with no brand (brand is null) reads what it read. The one
-- new view, Sales alert settings, is at the end.

CREATE OR REPLACE VIEW query.backlinks WITH (security_barrier) AS
SELECT b.id, b.brand_id, b.site_id, b.source_url, b.target_url, b.source_domain, b.anchor, b.title, b.nofollow, b.sponsored, b.ugc,
    b.link_type, b.in_content, b.status, b.source_rating, b.url_rating, b.domain_traffic, b.page_traffic, b.spam_score, b.review,
    b.first_seen_at, b.last_seen_at, b.lost_at, b.regained_at, b.checked_at
FROM backlinks b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.brand_id IS NULL OR b.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR b.brand_id = s.brand);

CREATE OR REPLACE VIEW query.coverage_donor_profiles WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.publisher_id, pub.domain, p.pbn, p.country, p.languages, p.niches, p.casino_allowed,
    p.rel_offered, p.slot_types, p.duration, p.duration_days, p.tat_days, p.guarantee_days, p.vendor_id, p.source,
    p.domain_rating, p.traffic, p.spam_score, p.metrics_at, p.metrics_source, p.archived_at, p.created_at, p.updated_at
FROM coverage_donor_profiles p
JOIN coverage_publishers pub ON pub.id = p.publisher_id AND pub.workspace_id = p.workspace_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'coverage.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR p.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR p.brand_id = s.brand);

CREATE OR REPLACE VIEW query.coverage_link_orders WITH (security_barrier) AS
SELECT o.id, o.brand_id, o.publisher_id, o.donor_profile_id, o.target_url, o.slot_type, o.rel_requested, o.status,
    o.quoted_amount_minor, o.quoted_currency, o.donor_amount_minor, o.currency, o.guarantee_days, o.guarantee_until,
    o.published_url, o.proof_url, o.published_at, o.placement_id, o.agreement_id, o.invoice_id, o.payment_id,
    o.credit_payment_id, o.replaces_order_id, o.owner_user_id, o.last_check_at, o.last_check_live,
    o.last_check_target_match, o.last_check_rel, o.last_check_anchor, o.approved_at, o.paid_at, o.lost_at,
    o.created_at, o.updated_at
FROM coverage_link_orders o JOIN query_scopes s ON s.pid = pg_backend_pid() AND o.workspace_id = s.workspace_id
WHERE 'coverage.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR o.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR o.brand_id = s.brand);

CREATE OR REPLACE VIEW query.link_prospects WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.site_id, p.record_id, p.page_url, p.method, p.target_url, p.evidence, p.domain_rating, p.traffic,
    p.spam_score, p.score, p.stage, p.excluded_reason, p.stage_changed_at, p.pitched_at, p.agreed_at, p.live_at, p.removed_at, p.overdue_at,
    p.ended_at, p.source, p.created_at
FROM link_prospects p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id IS NULL OR p.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR p.brand_id = s.brand);

CREATE OR REPLACE VIEW query.review_page_days WITH (security_barrier) AS
SELECT d.page_id, d.brand_id, p.platform, d.day, d.score, d.review_count, d.new_reviews, d.stars_1, d.stars_2, d.stars_3, d.stars_4, d.stars_5,
    d.outflare_score, d.momentum
FROM review_page_days d JOIN review_pages p ON p.id = d.page_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id AND p.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR d.brand_id IS NULL OR d.brand_id = ANY (s.brands)) AND p.competitor_id IS NULL
    AND (s.brand IS NULL OR d.brand_id = s.brand);

CREATE OR REPLACE VIEW query.reviews WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.page_id, r.platform, r.url, r.rating, r.title, r.body, r.language, r.author_name, r.author_country, r.verified,
    r.posted_at, r.edited_at, r.sentiment, r.sentiment_source, r.reply_body, r.replied_at, r.state, r.first_seen_at
FROM reviews r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR r.brand_id = s.brand);

CREATE OR REPLACE VIEW query.seo_organic_conversions_by_cluster WITH (security_barrier) AS
SELECT x.brand_id, x.site_id, x.day, x.group_id, x.group_name,
    x.organic_sessions, x.signups, x.signup_people, x.payments, x.payment_people, x.revenue_usd
FROM (
    SELECT c.workspace_id, c.brand_id, c.seo_site_id AS site_id, c.day,
        g.id AS group_id, COALESCE(g.name, 'Ungrouped') AS group_name,
        sum(c.organic_sessions) AS organic_sessions,
        sum(c.signups) AS signups,
        sum(c.signup_people) AS signup_people,
        sum(c.payments) AS payments,
        sum(c.payment_people) AS payment_people,
        sum(c.revenue_usd) AS revenue_usd
    FROM organic_conversions c
    LEFT JOIN LATERAL (
        SELECT sg.id, sg.name
        FROM search_groups sg
        WHERE sg.site_id = c.seo_site_id AND sg.kind = 'content'
          AND organic_content_group_matches(sg.rules, c.canonical_path)
    ) g ON true
    GROUP BY c.workspace_id, c.brand_id, c.seo_site_id, c.day, g.id, g.name
) x JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

CREATE OR REPLACE VIEW query.seo_organic_conversions_by_page WITH (security_barrier) AS
SELECT c.brand_id, c.seo_site_id AS site_id, c.day, c.host, c.path, c.canonical_path,
    c.organic_sessions, c.signups, c.signup_people, c.payments, c.payment_people, c.revenue_usd
FROM organic_conversions c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR c.brand_id IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);

CREATE OR REPLACE VIEW query.social_posts WITH (security_barrier) AS
SELECT p.id, p.brand_id, p.account_id, p.channel, p.url, p.body, p.posted_at, p.impressions, p.likes, p.replies, p.reposts, p.quotes,
    p.bookmarks, p.clicks, p.metrics_at
FROM social_posts p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id IS NULL OR p.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR p.brand_id = s.brand);

CREATE OR REPLACE VIEW query.visits WITH (security_barrier) AS
SELECT v.id, v.brand_id, v.record_id, v.anonymous_id, v.started_at, v.last_seen_at, v.events, v.pageviews, v.landing_path, v.referrer_host,
    v.utm_source, v.utm_medium, v.utm_campaign, v.utm_term, v.utm_content, v.country, v.device, v.browser, v.os,
    v.region, v.city, v.asn, v.isp, v.device_brand, v.device_model, v.os_version, v.browser_version, v.language
FROM site_sessions v JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes) AND (s.brands IS NULL OR v.brand_id IS NULL OR v.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR v.brand_id = s.brand);

-- Where a brand's Sales alerts go in Slack, and whether they are on.
CREATE VIEW query.sales_alert_settings WITH (security_barrier) AS
SELECT x.brand_id, x.integration_id, x.channel_id, x.channel_name, x.enabled, x.replies, x.enabled_at, x.error_code,
    x.updated_at
FROM sales_alert_settings x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
