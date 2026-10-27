-- Search Console page by page: what each page was shown and clicked for,
-- by query and day; how Google indexed each URL (URL Inspection); and the
-- sitemaps it read. The SEO module reads these for keyword targets,
-- cannibalisation, internal links and regressions. Rows come from the
-- workspace's own Search Console sign-in and line up with SEO sites by
-- host, like analytics_daily.

CREATE TABLE search_daily (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- The Search Console property: https://example.com/ or sc-domain:example.com.
    site           text NOT NULL,
    -- The property's domain without www., as in analytics_daily.
    host           text NOT NULL DEFAULT '',
    day            date NOT NULL,
    -- The page's full URL as Google reports it, and the query.
    page           text NOT NULL,
    query          text NOT NULL,
    clicks         integer NOT NULL DEFAULT 0,
    impressions    integer NOT NULL DEFAULT 0,
    ctr            double precision NOT NULL DEFAULT 0,
    -- Average position, 1 is the top.
    position       double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, site, day, page, query)
);
CREATE INDEX search_daily_host_idx ON search_daily (workspace_id, host, day);
CREATE INDEX search_daily_page_idx ON search_daily (workspace_id, host, page, day);

-- Where the page by page sync of each property has got to. The first run
-- reads the last 90 days; later runs re-read the recent days and go back a
-- few weeks at a time, to 16 months, which is all Search Console keeps.
CREATE TABLE search_syncs (
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site           text NOT NULL,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    host           text NOT NULL DEFAULT '',
    oldest_day     date,
    newest_day     date,
    -- The day the backfill reached the start of Search Console's history.
    backfilled_at  timestamptz,
    sitemaps_at    timestamptz,
    inspected_at   timestamptz,
    last_run_at    timestamptz,
    last_error     text,
    PRIMARY KEY (integration_id, site)
);
CREATE INDEX search_syncs_ws_idx ON search_syncs (workspace_id, host);

CREATE TABLE url_inspections (
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id   uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site             text NOT NULL,
    host             text NOT NULL DEFAULT '',
    url              text NOT NULL,
    -- Google's words: PASS, PARTIAL, FAIL or NEUTRAL, and why.
    verdict          text NOT NULL DEFAULT '',
    coverage         text NOT NULL DEFAULT '',
    indexing         text NOT NULL DEFAULT '',
    robots_txt       text NOT NULL DEFAULT '',
    page_fetch       text NOT NULL DEFAULT '',
    google_canonical text NOT NULL DEFAULT '',
    user_canonical   text NOT NULL DEFAULT '',
    last_crawl       timestamptz,
    crawled_as       text NOT NULL DEFAULT '',
    sitemaps         text[] NOT NULL DEFAULT '{}',
    mobile           text NOT NULL DEFAULT '',
    rich_results     text NOT NULL DEFAULT '',
    link             text NOT NULL DEFAULT '',
    -- The verdict the inspection before this one gave, so a page that
    -- drops out of the index shows as a change.
    previous_verdict text NOT NULL DEFAULT '',
    verdict_changed_at timestamptz,
    inspected_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, site, url)
);
CREATE INDEX url_inspections_host_idx ON url_inspections (workspace_id, host, inspected_at DESC);

CREATE TABLE search_sitemaps (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site           text NOT NULL,
    host           text NOT NULL DEFAULT '',
    path           text NOT NULL,
    last_submitted timestamptz,
    last_read      timestamptz,
    pending        boolean NOT NULL DEFAULT false,
    is_index       boolean NOT NULL DEFAULT false,
    errors         integer NOT NULL DEFAULT 0,
    warnings       integer NOT NULL DEFAULT 0,
    submitted      integer NOT NULL DEFAULT 0,
    indexed        integer NOT NULL DEFAULT 0,
    checked_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, site, path)
);
CREATE INDEX search_sitemaps_host_idx ON search_sitemaps (workspace_id, host);
