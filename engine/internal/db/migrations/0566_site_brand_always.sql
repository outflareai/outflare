-- Every analytics site reports for a brand: none is shared by every brand
-- or left without one. A site takes the brand someone picked for it, else
-- its connection's brand, else the workspace's first brand. A domain still
-- only suggests a brand. The first brand is a default nobody chose:
-- outflare_site_brand_chosen leaves it out, so the app can still ask which
-- brand such a site is for.
--
-- outflare_site_brand keeps its old meaning (null when nothing decided) so
-- the previous version still runs on this schema; this version calls
-- outflare_site_brand_or_first instead. A later release can point the old
-- name at the new one.

CREATE FUNCTION outflare_site_brand_chosen(integration uuid, site_id text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT o.brand_id FROM analytics_site_brands o WHERE o.integration_id = integration AND o.site = site_id),
        i.brand_id)
    FROM integrations i WHERE i.id = integration
$$;

CREATE FUNCTION outflare_site_brand_or_first(integration uuid, site_id text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(outflare_site_brand_chosen(integration, site_id), outflare_first_brand(i.workspace_id))
    FROM integrations i WHERE i.id = integration
$$;

-- Rows a sync writes take the site's brand, or the first brand.
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
        NEW.brand_id := outflare_site_brand_or_first(NEW.integration_id, NEW.site);
        RETURN NEW;
    ELSIF TG_TABLE_NAME = 'analytics_cache' THEN
        NEW.brand_id := outflare_site_brand_or_first(NEW.integration_id, '');
        RETURN NEW;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

-- "Shared by all brands" is gone: a site someone made shared follows its
-- connection or the first brand like any other.
DELETE FROM analytics_site_brands WHERE brand_id IS NULL;

-- Everything stored for a site without a brand moves to the site's brand.
UPDATE search_segments SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_ga4_outcomes SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_completeness SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE analytics_daily SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE analytics_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_daily SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_sitemaps SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE url_inspections SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE page_conversions SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE page_conversion_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE organic_conversions SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE organic_conversion_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
