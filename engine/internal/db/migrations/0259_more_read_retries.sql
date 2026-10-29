-- More scheduled reads are tried again soon after a failure
-- (engine/internal/retry: 15 minutes, then 1, 2 and every 4 hours) rather
-- than at their next interval: news searches (daily by default), tracked
-- competitors' weekly look, and each SEO site's weekly competitor and
-- backlink reads. failures counts the runs in a row that failed.
ALTER TABLE news_searches
    ADD COLUMN failures int NOT NULL DEFAULT 0;

ALTER TABLE seo_competitors
    ADD COLUMN check_failures int NOT NULL DEFAULT 0;

-- An SEO site's weekly read (competitors or backlinks) through one
-- connection that failed, until a read of it works again.
CREATE TABLE seo_site_read_failures (
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site_id         uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind            text NOT NULL CHECK (kind IN ('competitors', 'backlinks')),
    failures        int NOT NULL DEFAULT 1,
    last_failed_at  timestamptz NOT NULL DEFAULT now(),
    last_error_code text,
    PRIMARY KEY (integration_id, site_id, kind)
);
CREATE INDEX seo_site_read_failures_ws_idx ON seo_site_read_failures (workspace_id);
