-- The page a person chose to keep for a search several of a site's pages
-- compete for, over the one Outflare would pick (the page that targets
-- the search, else the one with the most clicks). One per site and search.
CREATE TABLE seo_cannibal_primaries (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    site_id      uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    query        text NOT NULL CHECK (length(query) BETWEEN 1 AND 500),
    url          text NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
    set_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, query)
);
CREATE INDEX seo_cannibal_primaries_brand_idx ON seo_cannibal_primaries (workspace_id, brand_id);

CREATE VIEW query.seo_cannibal_primaries WITH (security_barrier) AS
SELECT p.brand_id, p.site_id, p.query, p.url, p.set_by, p.created_at, p.updated_at
FROM seo_cannibal_primaries p JOIN query_scopes s ON s.pid = pg_backend_pid() AND p.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR p.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR p.brand_id = s.brand);
