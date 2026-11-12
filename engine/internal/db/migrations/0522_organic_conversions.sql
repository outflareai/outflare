-- Organic conversions from PostHog: first-touch Organic Search signups
-- and payments, stored by landing page so the SEO page and Query can
-- read them without calling PostHog again. A payment is counted once:
-- payment_succeeded when it exists, and a paid service_purchased (not a
-- trial, not a wallet top-up) only when no payment_succeeded has the
-- same person, calendar day and amount. Search groups are applied at
-- read time from the brand's current rules, so a group change does not
-- need a resync. Locale prefixes are stripped into canonical_path.
CREATE TABLE organic_conversions (
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    seo_site_id      uuid REFERENCES seo_sites (id) ON DELETE CASCADE,
    integration_id   uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site             text NOT NULL,
    day              date NOT NULL,
    host             text NOT NULL,
    path             text NOT NULL,
    canonical_path   text NOT NULL,
    organic_sessions integer NOT NULL DEFAULT 0,
    signups          integer NOT NULL DEFAULT 0,
    signup_people    integer NOT NULL DEFAULT 0,
    payments         integer NOT NULL DEFAULT 0,
    payment_people   integer NOT NULL DEFAULT 0,
    revenue_usd      numeric(18, 2) NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, site, day, host, path)
);
CREATE INDEX organic_conversions_site_day_idx ON organic_conversions (workspace_id, seo_site_id, day);
CREATE INDEX organic_conversions_brand_idx ON organic_conversions (brand_id, day);

CREATE TABLE organic_conversion_syncs (
    integration_id         uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site                   text NOT NULL,
    workspace_id           uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id               uuid REFERENCES brands (id) ON DELETE SET NULL,
    oldest_day             date,
    newest_day             date,
    payment_succeeded_from date,
    sessions_source        text,
    backfilled_at          timestamptz,
    last_run_at            timestamptz NOT NULL DEFAULT now(),
    last_error             text,
    last_error_code        text,
    failures               integer NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, site)
);
CREATE INDEX organic_conversion_syncs_ws_idx ON organic_conversion_syncs (workspace_id);

-- Fill brand_id on insert when the caller leaves it out, the same way as
-- page_conversions: from the SEO site the PostHog project is attached to.
CREATE OR REPLACE FUNCTION outflare_website_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    b uuid;
BEGIN
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF TG_TABLE_NAME IN ('consent_banners', 'cookie_policies', 'cookie_scans', 'site_cookies') THEN
        SELECT brand_id INTO b FROM site_keys WHERE id = NEW.site_key_id;
    ELSIF TG_TABLE_NAME IN ('site_event_inbox', 'site_key_days') THEN
        SELECT brand_id INTO b FROM site_keys WHERE id = NEW.key_id;
    ELSIF TG_TABLE_NAME = 'ab_test_checks' THEN
        SELECT brand_id INTO b FROM ab_tests WHERE id = NEW.test_id;
    ELSIF TG_TABLE_NAME = 'experiment_pages' THEN
        SELECT brand_id INTO b FROM experiments WHERE id = NEW.experiment_id;
    ELSIF TG_TABLE_NAME = 'tracking_links' THEN
        SELECT brand_id INTO b FROM tracking_campaigns WHERE id = NEW.campaign_id;
    ELSIF TG_TABLE_NAME = 'tracked_links' THEN
        SELECT COALESCE((SELECT brand_id FROM emails WHERE id = NEW.email_id),
                        (SELECT brand_id FROM sequences WHERE id = NEW.sequence_id)) INTO b;
    ELSIF TG_TABLE_NAME = 'link_clicks' THEN
        SELECT brand_id INTO b FROM tracked_links WHERE id = NEW.link_id;
    ELSIF TG_TABLE_NAME = 'campaign_links' THEN
        SELECT brand_id INTO b FROM sequences WHERE id = NEW.sequence_id;
    ELSIF TG_TABLE_NAME = 'attribution_credits' THEN
        SELECT brand_id INTO b FROM attribution_conversions WHERE event_id = NEW.event_id;
    ELSIF TG_TABLE_NAME IN ('analytics_daily', 'analytics_syncs', 'search_daily', 'search_syncs', 'search_sitemaps',
                            'url_inspections', 'page_conversions', 'page_conversion_syncs',
                            'organic_conversions', 'organic_conversion_syncs') THEN
        NEW.brand_id := outflare_site_brand(NEW.integration_id, NEW.site);
        RETURN NEW;
    ELSIF TG_TABLE_NAME = 'analytics_cache' THEN
        NEW.brand_id := outflare_site_brand(NEW.integration_id, '');
        RETURN NEW;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

CREATE TRIGGER organic_conversions_brand
    BEFORE INSERT ON organic_conversions
    FOR EACH ROW EXECUTE FUNCTION outflare_website_brand();
CREATE TRIGGER organic_conversion_syncs_brand
    BEFORE INSERT ON organic_conversion_syncs
    FOR EACH ROW EXECUTE FUNCTION outflare_website_brand();

-- Content-group matching for the cluster view, following the same
-- operators as seointel.Matches: rules are ORed; a value that starts
-- with / is compared to the path. Topic groups are ignored here because
-- this view has no search query. Invalid regex does not match.
CREATE FUNCTION organic_content_group_matches(rules jsonb, page text) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE AS $$
DECLARE
    r jsonb;
    op text;
    val text;
    x text;
BEGIN
    IF rules IS NULL THEN
        RETURN false;
    END IF;
    FOR r IN SELECT value FROM jsonb_array_elements(rules)
    LOOP
        op := r->>'operator';
        val := r->>'value';
        x := page;
        IF val LIKE '/%' THEN
            x := page;
        END IF;
        IF op = 'equals' AND x = val THEN
            RETURN true;
        ELSIF op = 'prefix' AND starts_with(x, val) THEN
            RETURN true;
        ELSIF op = 'contains' AND position(val IN x) > 0 THEN
            RETURN true;
        ELSIF op = 'regex' THEN
            BEGIN
                IF x ~ val THEN
                    RETURN true;
                END IF;
            EXCEPTION WHEN others THEN
                -- Invalid or unsafe patterns are skipped, same as a miss.
            END;
        END IF;
    END LOOP;
    RETURN false;
END
$$;

-- Organic conversions by landing page, for callers with seo.read. Groups
-- can overlap, so do not add cluster totals together; use this page view
-- or the API totals when you need a unique sum.
CREATE VIEW query.seo_organic_conversions_by_page WITH (security_barrier) AS
SELECT c.brand_id, c.seo_site_id AS site_id, c.day, c.host, c.path, c.canonical_path,
    c.organic_sessions, c.signups, c.signup_people, c.payments, c.payment_people, c.revenue_usd
FROM organic_conversions c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR c.brand_id IS NULL OR c.brand_id = ANY (s.brands));

CREATE VIEW query.seo_organic_conversions_by_cluster WITH (security_barrier) AS
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
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY (s.brands));
