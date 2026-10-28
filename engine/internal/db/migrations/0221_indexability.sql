-- Indexability: which of a site's pages Google has indexed, from every
-- source Outflare has (the crawl, the sitemaps it read, Search Console's
-- URL inspections and impressions, and the rankings an SEO tool sees), and
-- how that changes over time.

-- Whether a sitemap the crawl read lists the page. A page found by a link
-- first keeps its source but is marked here too. Older crawls only have
-- source = 'sitemap', which readers count as well.
ALTER TABLE crawl_pages ADD COLUMN in_sitemap boolean NOT NULL DEFAULT false;

-- One row per site and day: the indexability report's counts, taken after
-- each crawl and each Search Console sync, so the report has history.
CREATE TABLE indexability_snapshots (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    day            date NOT NULL,
    -- Every page any source knows.
    pages          integer NOT NULL DEFAULT 0,
    crawled        integer NOT NULL DEFAULT 0,
    -- Pages the crawl found that can be indexed: they answer 200, aren't
    -- noindex and don't point their canonical elsewhere.
    indexable      integer NOT NULL DEFAULT 0,
    in_sitemap     integer NOT NULL DEFAULT 0,
    -- The verdicts: Google showed or confirmed it, an SEO tool sees it
    -- ranking, Google says it isn't indexed, or nothing says.
    indexed        integer NOT NULL DEFAULT 0,
    likely_indexed integer NOT NULL DEFAULT 0,
    not_indexed    integer NOT NULL DEFAULT 0,
    unknown        integer NOT NULL DEFAULT 0,
    -- Pages per issue, by issue id.
    issues         jsonb NOT NULL DEFAULT '{}',
    taken_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (site_id, day)
);
CREATE INDEX indexability_snapshots_ws_idx ON indexability_snapshots (workspace_id, site_id, day);
