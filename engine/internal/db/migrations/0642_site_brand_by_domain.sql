-- An analytics site nobody placed reports for the brand whose website it
-- is. Before, a site nobody picked followed its connection's brand, else
-- the workspace's first brand, so a Search Console sign-in that holds
-- several brands' properties put all of them under the first brand, and
-- that brand's reports listed the other brands' pages.
--
-- A site now takes, in order: the brand someone picked for it; the brand
-- one of whose domains is the site's domain (the longest match, so
-- docs.example.com can belong to another brand than example.com); its
-- connection's brand; the workspace's first brand. A site whose domain is
-- no brand's still follows its connection, as before.
--
-- New functions beside the old ones, so the previous version runs on this
-- schema.

-- outflare_site_domain is the domain a site's sync stored for it, else
-- the domain in its id (sc-domain:example.com, https://example.com/).
CREATE FUNCTION outflare_site_domain(integration uuid, site_id text) RETURNS text
LANGUAGE sql STABLE AS $$
    SELECT lower(COALESCE(
        (SELECT NULLIF(s.host, '') FROM analytics_syncs s WHERE s.integration_id = integration AND s.site = site_id),
        substring(site_id from '^(?:sc-domain:|https?://)(?:www\.)?([^/:?#]+)')))
$$;

-- outflare_domain_brand is the active brand one of whose domains is host
-- or a parent of it, the longest match first; null when none is.
CREATE FUNCTION outflare_domain_brand(ws uuid, host text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT b.id FROM brands b, unnest(b.domains) d
    WHERE b.workspace_id = ws AND b.status = 'active' AND host IS NOT NULL AND host <> '' AND d <> ''
        AND (lower(host) = lower(d) OR lower(host) LIKE '%.' || lower(d))
    ORDER BY length(d) DESC, b.created_at LIMIT 1
$$;

-- outflare_site_brand_placed is who placed a site: the brand someone
-- picked, else the brand of its domain, else its connection's brand; null
-- when nothing did. outflare_site_brand_now adds the first brand.
CREATE FUNCTION outflare_site_brand_placed(integration uuid, site_id text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT o.brand_id FROM analytics_site_brands o WHERE o.integration_id = integration AND o.site = site_id),
        outflare_domain_brand(i.workspace_id, outflare_site_domain(integration, site_id)),
        i.brand_id)
    FROM integrations i WHERE i.id = integration
$$;

CREATE FUNCTION outflare_site_brand_now(integration uuid, site_id text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(outflare_site_brand_placed(integration, site_id), outflare_first_brand(i.workspace_id))
    FROM integrations i WHERE i.id = integration
$$;

-- Rows a sync writes take the site's brand as above. outflare_site_brand_chosen
-- and outflare_site_brand_or_first keep their meaning for the previous
-- version; a later release can point them at the new ones.
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
        NEW.brand_id := outflare_site_brand_now(NEW.integration_id, NEW.site);
        RETURN NEW;
    ELSIF TG_TABLE_NAME = 'analytics_cache' THEN
        NEW.brand_id := outflare_site_brand_now(NEW.integration_id, '');
        RETURN NEW;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

-- Sites nobody picked whose brand is now another one, and their rows.
CREATE TEMP TABLE site_brand_moves ON COMMIT DROP AS
SELECT k.integration_id, k.site, outflare_site_brand_now(k.integration_id, k.site) AS brand_id
FROM (SELECT integration_id, site FROM analytics_syncs
      UNION SELECT integration_id, site FROM search_syncs) k
WHERE NOT EXISTS (SELECT 1 FROM analytics_site_brands o WHERE o.integration_id = k.integration_id AND o.site = k.site);

UPDATE search_segments t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE search_ga4_outcomes t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE search_completeness t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE analytics_daily t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE analytics_syncs t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE search_daily t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE search_syncs t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE search_sitemaps t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE url_inspections t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE page_conversions t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE page_conversion_syncs t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE organic_conversions t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
UPDATE organic_conversion_syncs t SET brand_id = m.brand_id FROM site_brand_moves m WHERE t.integration_id = m.integration_id AND t.site = m.site AND t.brand_id IS DISTINCT FROM m.brand_id;
