-- Search results pages read for the keywords tracked competitors rank for
-- and the site doesn't (keyword gaps): who ranks for each, competitor or
-- not, so a person can open a gap keyword and see its results, and the
-- domains that rank across several of them are suggested as competitors.
-- Read on the workspace's own SERP connection, within its budget; one row
-- per keyword, country and device, replaced on each read. Only adds, so
-- the version before still runs.
CREATE TABLE keyword_serps (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid REFERENCES brands (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    keyword        text NOT NULL,
    country        text NOT NULL,
    device         text NOT NULL DEFAULT 'desktop',
    engine         text NOT NULL,
    items          jsonb NOT NULL DEFAULT '[]',
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    cost_usd       numeric NOT NULL DEFAULT 0,
    read_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, keyword, country, device)
);
CREATE INDEX keyword_serps_site_idx ON keyword_serps (workspace_id, site_id, read_at);
