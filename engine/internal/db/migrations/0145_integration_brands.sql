-- Connections shared with brands. A workspace connection (brand_id null) is
-- connected once and used by every brand, or only by the brands listed in
-- brand_ids. A brand's own connection (brand_id set) is used by that brand
-- alone, in place of the workspace's connections to the same tool. So one
-- Google Analytics sign-in serves every brand, and a brand with its own
-- account adds it once without touching the others.
ALTER TABLE integrations ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE CASCADE;
-- The brands a workspace connection is shared with. Empty means every
-- brand. Always empty on a brand's own connection.
ALTER TABLE integrations ADD COLUMN brand_ids uuid[] NOT NULL DEFAULT '{}';
ALTER TABLE integrations ADD CONSTRAINT integrations_owner_or_shared CHECK (brand_id IS NULL OR cardinality(brand_ids) = 0);
CREATE INDEX integrations_brand_idx ON integrations (brand_id) WHERE brand_id IS NOT NULL;

-- integration_serves reports whether a connection is used by a brand: the
-- brand's own, or a workspace connection shared with it when the brand has
-- no connection of its own to the same tool. A null brand (all brands, or
-- a workspace without brands) is served by every connection.
CREATE FUNCTION integration_serves(i integrations, brand uuid) RETURNS boolean
LANGUAGE sql STABLE AS $$
    SELECT brand IS NULL
        OR i.brand_id = brand
        OR (i.brand_id IS NULL
            AND (cardinality(i.brand_ids) = 0 OR brand = ANY (i.brand_ids))
            AND NOT EXISTS (SELECT 1 FROM integrations o
                            WHERE o.workspace_id = i.workspace_id AND o.connector = i.connector AND o.brand_id = brand))
$$;

-- An analytics site read through a brand's own connection reports for that
-- brand unless analytics_site_brands says otherwise, whatever its domain.
-- Sites read through a workspace connection still go to the brand whose
-- domains include theirs, among the brands the connection is shared with.
CREATE OR REPLACE FUNCTION analytics_site_brand(ws uuid, integration uuid, site_id text, host text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN o.integration_id IS NOT NULL THEN o.brand_id
        WHEN i.brand_id IS NOT NULL THEN i.brand_id
        ELSE (
            SELECT b.id FROM brands b, unnest(b.domains) d
            WHERE b.workspace_id = ws AND b.status = 'active' AND host <> '' AND (host = d OR host LIKE '%.' || d)
              AND (i.id IS NULL OR cardinality(i.brand_ids) = 0 OR b.id = ANY (i.brand_ids))
            ORDER BY length(d) DESC, b.created_at LIMIT 1
        ) END
    FROM (SELECT 1) one
    LEFT JOIN analytics_site_brands o ON o.integration_id = integration AND o.site = site_id
    LEFT JOIN integrations i ON i.id = integration AND i.workspace_id = ws
$$;
