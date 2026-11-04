-- Connections and brands (plans/brands/README.md, "Connections and brands
-- (owner, 2026-11-01)"): no brand, no data. Each connection has a kind,
-- chosen when connecting:
--
--   brand:  the whole connection belongs to one brand (integrations.brand_id).
--   split:  one sign-in whose accounts or properties (Google Analytics
--           properties, Search Console sites, ad accounts) are assigned to
--           brands one by one (analytics_site_brands, ad_accounts.brand_id).
--   shared: a tool every brand can use (DataForSEO, the AI key, Slack);
--           what it produces belongs to the brand that used it.
--
-- Expand only (docs/migrations.md): a nullable column, a function and a
-- backfill. A null kind is the connector's default from the catalog, which
-- is what every connection the previous version makes gets.

ALTER TABLE integrations ADD COLUMN brand_kind text
    CHECK (brand_kind IN ('brand', 'split', 'shared'));

-- Who assigned a site, and when it was first seen without a brand.
ALTER TABLE analytics_site_brands ADD COLUMN assigned_by uuid REFERENCES actors (id) ON DELETE SET NULL;

-- outflare_site_brand is the brand an analytics or Search Console site's
-- data belongs to: the brand someone assigned it to, else the brand its
-- connection belongs to, else the workspace's only brand when it has just
-- one (a single-brand company never has to assign anything). Otherwise it
-- is null: the site is unassigned, and its data, stored with no brand, is
-- shown in no brand until someone assigns it. Unlike analytics_site_brand
-- (which the previous version still reads), a domain match is only a
-- suggestion, never an assignment.
CREATE FUNCTION outflare_site_brand(integration uuid, site_id text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT o.brand_id FROM analytics_site_brands o WHERE o.integration_id = integration AND o.site = site_id),
        i.brand_id,
        (SELECT min(b.id::text)::uuid FROM brands b WHERE b.workspace_id = i.workspace_id AND b.status = 'active'
         HAVING count(*) = 1))
    FROM integrations i WHERE i.id = integration
$$;

-- What a domain used to place goes on as an assignment, so no brand loses
-- data it showed before: every site a sync knows whose brand came from a
-- brand's domains gets that brand written down.
INSERT INTO analytics_site_brands (workspace_id, integration_id, site, brand_id)
SELECT DISTINCT ON (s.integration_id, s.site) s.workspace_id, s.integration_id, s.site,
    analytics_site_brand(s.workspace_id, s.integration_id, s.site, s.host)
FROM (
    SELECT workspace_id, integration_id, site, host FROM analytics_syncs
    UNION ALL SELECT workspace_id, integration_id, site, host FROM search_syncs
) s
WHERE analytics_site_brand(s.workspace_id, s.integration_id, s.site, s.host) IS NOT NULL
ON CONFLICT (integration_id, site) DO NOTHING;
