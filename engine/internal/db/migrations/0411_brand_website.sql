-- Brands own their data, step 6 (plans/brands/README.md): website and
-- analytics. Sites and their keys, the consent banner and cookies, flags
-- and A/B tests, SEO experiments, tracking domains, campaigns and links,
-- visitors, attribution, and the analytics and Search Console syncs each
-- carry brand_id on the row itself, so a brand's website data is exactly
-- WHERE brand_id = $1.
--
-- What was "for every brand" goes to the workspace's first brand, as
-- workspaces from before brands have one brand. Expand only
-- (docs/migrations.md): columns, indexes, triggers and backfills.
--
-- The parents that already had a nullable brand_id (site_keys, flags,
-- ab_tests, experiments, tracking_campaigns), where the version still
-- serving reads null as every brand, are backfilled; the new code sets the
-- brand of every row it writes, and no trigger fills them, so the older
-- version keeps writing and reading shared rows as its tests expect. The
-- other tables are new to brand_id and a trigger fills it: from the parent
-- row, or for synced analytics from the site's brand (outflare_site_brand,
-- 0410). An analytics or Search Console site nobody assigned has no brand:
-- its rows keep brand_id null and show in no brand (no brand, no data).
--
-- Six tables stay pending for a later migration, once this version
-- serves. flag_groups and attribution_models: the previous version lists
-- every row of the workspace, so a brand column would show one brand's
-- rows under another's (its TestNoBrandLeaks says so).
-- attribution_model_pins, conversion_definitions, tracking_link_clicks and
-- visitor_alerts: the previous version's TestNoBrandLeaks seeds every table
-- with brand_id and can't fill their pattern checks.

CREATE FUNCTION outflare_website_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    b uuid;
BEGIN
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- One branch per table: plpgsql only resolves the one it runs.
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
                            'url_inspections', 'page_conversions', 'page_conversion_syncs') THEN
        -- No brand, no data: an unassigned site's rows stay without one.
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

-- ---- The parents: what was for every brand goes to the first brand ----

UPDATE site_keys SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE flags SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
-- A test goes with its flag.
UPDATE ab_tests t SET brand_id = COALESCE((SELECT f.brand_id FROM flags f WHERE f.id = t.flag_id), outflare_first_brand(t.workspace_id))
WHERE t.brand_id IS NULL;
UPDATE experiments e SET brand_id = COALESCE((SELECT s.brand_id FROM seo_sites s WHERE s.id = e.site_id), outflare_first_brand(e.workspace_id))
WHERE e.brand_id IS NULL;
UPDATE tracking_campaigns SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

CREATE INDEX IF NOT EXISTS site_keys_brand_idx ON site_keys (brand_id);
CREATE INDEX IF NOT EXISTS flags_brand_idx ON flags (brand_id, created_at DESC);
CREATE INDEX IF NOT EXISTS ab_tests_brand_idx ON ab_tests (brand_id, created_at DESC);
CREATE INDEX IF NOT EXISTS experiments_brand_idx ON experiments (brand_id, created_at DESC);
CREATE INDEX IF NOT EXISTS tracking_campaigns_brand_idx ON tracking_campaigns (brand_id, created_at DESC);

-- ---- New to brand_id: filled by the trigger ----

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'consent_banners', 'cookie_policies', 'cookie_scans', 'site_cookies', 'site_event_inbox', 'site_key_days',
        'ab_test_checks', 'experiment_pages',
        'tracking_domains', 'tracking_links', 'tracked_links', 'link_clicks', 'campaign_links',
        'visitor_settings', 'visitor_ip_lookups',
        'attribution_settings', 'attribution_credits',
        'analytics_cache', 'analytics_daily', 'analytics_syncs', 'search_daily', 'search_syncs', 'search_sitemaps',
        'url_inspections', 'page_conversions', 'page_conversion_syncs']
    LOOP
        EXECUTE format('ALTER TABLE %I ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_website_brand()', t || '_brand', t);
    END LOOP;
END
$$;

-- Backfills, parents first.
UPDATE consent_banners x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id;
UPDATE cookie_policies x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id;
UPDATE cookie_scans x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id;
UPDATE site_cookies x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id;
UPDATE site_event_inbox x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.key_id;
UPDATE site_key_days x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.key_id;
UPDATE ab_test_checks x SET brand_id = t.brand_id FROM ab_tests t WHERE t.id = x.test_id;
UPDATE experiment_pages x SET brand_id = e.brand_id FROM experiments e WHERE e.id = x.experiment_id;
-- A tracking domain takes the brand of the partner portal or help centre
-- it serves, else of the brand whose own domain it is under (the longest
-- match: links.acme.example goes to the brand with acme.example), else the
-- first brand. Its brand can be changed afterwards.
UPDATE tracking_domains t SET brand_id = COALESCE(
    (SELECT p.brand_id FROM partner_programs p WHERE p.portal_domain_id = t.id AND p.brand_id IS NOT NULL LIMIT 1),
    (SELECT h.brand_id FROM help_centres h WHERE h.domain_id = t.id AND h.brand_id IS NOT NULL LIMIT 1),
    (SELECT b.id FROM brands b, unnest(b.domains) d
        WHERE b.workspace_id = t.workspace_id AND b.status = 'active' AND (t.domain = d OR t.domain LIKE '%.' || d)
        ORDER BY length(d) DESC, b.created_at LIMIT 1),
    outflare_first_brand(t.workspace_id));
UPDATE tracking_links x SET brand_id = c.brand_id FROM tracking_campaigns c WHERE c.id = x.campaign_id;
UPDATE tracked_links x SET brand_id = COALESCE((SELECT brand_id FROM emails WHERE id = x.email_id),
    (SELECT brand_id FROM sequences WHERE id = x.sequence_id), outflare_first_brand(x.workspace_id));
UPDATE link_clicks x SET brand_id = l.brand_id FROM tracked_links l WHERE l.id = x.link_id;
UPDATE campaign_links x SET brand_id = COALESCE((SELECT brand_id FROM sequences WHERE id = x.sequence_id), outflare_first_brand(x.workspace_id));
UPDATE visitor_settings SET brand_id = outflare_first_brand(workspace_id);
UPDATE visitor_ip_lookups SET brand_id = outflare_first_brand(workspace_id);
UPDATE attribution_settings SET brand_id = outflare_first_brand(workspace_id);
UPDATE attribution_credits x SET brand_id = COALESCE(c.brand_id, outflare_first_brand(x.workspace_id)) FROM attribution_conversions c WHERE c.event_id = x.event_id;
UPDATE analytics_cache SET brand_id = outflare_site_brand(integration_id, '');
UPDATE analytics_daily SET brand_id = outflare_site_brand(integration_id, site);
UPDATE analytics_syncs SET brand_id = outflare_site_brand(integration_id, site);
UPDATE search_daily SET brand_id = outflare_site_brand(integration_id, site);
UPDATE search_syncs SET brand_id = outflare_site_brand(integration_id, site);
UPDATE search_sitemaps SET brand_id = outflare_site_brand(integration_id, site);
UPDATE url_inspections SET brand_id = outflare_site_brand(integration_id, site);
UPDATE page_conversions SET brand_id = outflare_site_brand(integration_id, site);
UPDATE page_conversion_syncs SET brand_id = outflare_site_brand(integration_id, site);

-- Search insights' own tables took a site's brand from its domain too;
-- now it is the site's assignment, and an unassigned site's rows have none.
UPDATE search_segments SET brand_id = outflare_site_brand(integration_id, site);
UPDATE search_ga4_outcomes SET brand_id = outflare_site_brand(integration_id, site);
UPDATE search_completeness SET brand_id = outflare_site_brand(integration_id, site);

-- What a brand's pages read.
CREATE INDEX analytics_daily_brand_idx ON analytics_daily (brand_id, report, day);
CREATE INDEX search_daily_brand_idx ON search_daily (brand_id, host, day);
CREATE INDEX page_conversions_brand_idx ON page_conversions (brand_id, day);
CREATE INDEX tracking_domains_brand_idx ON tracking_domains (brand_id);
