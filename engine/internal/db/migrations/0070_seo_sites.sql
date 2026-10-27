-- SEO sites and the crawler (OutflareBot). A site is a website the
-- workspace audits: usually one of its brands' own, proved by a DNS record,
-- a meta tag or a file. A crawl run reads the site's pages one at a time,
-- obeying robots.txt (an owner who proved the site may turn that off for
-- their own site) and slowing down when the site asks.

CREATE TABLE seo_sites (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand the site belongs to; null is shared by every brand.
    brand_id          uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- Lowercase host, with a port only on installs that crawl private
    -- addresses: www.geonode.com.
    host              text NOT NULL,
    -- Where crawls start: https://www.geonode.com/.
    start_url         text NOT NULL,
    -- Put in the DNS record, meta tag or file that proves ownership.
    verify_token      text NOT NULL,
    verified_at       timestamptz,
    -- dns, meta, file, or test (a test workspace's simulated check).
    verify_method     text NOT NULL DEFAULT '' CHECK (verify_method IN ('', 'dns', 'meta', 'file', 'test')),
    verify_checked_at timestamptz,
    max_pages         integer NOT NULL DEFAULT 100 CHECK (max_pages BETWEEN 1 AND 10000),
    -- The pause between requests; robots.txt's Crawl-delay can lengthen it.
    crawl_delay_ms    integer NOT NULL DEFAULT 2000 CHECK (crawl_delay_ms BETWEEN 250 AND 60000),
    -- false only for a verified site whose owner chose to audit pages
    -- robots.txt hides; ignored again if verification lapses.
    obey_robots       boolean NOT NULL DEFAULT true,
    schedule          text NOT NULL DEFAULT 'off' CHECK (schedule IN ('off', 'daily', 'weekly')),
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, host)
);
CREATE INDEX seo_sites_brand_idx ON seo_sites (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX seo_sites_scheduled_idx ON seo_sites (schedule) WHERE schedule <> 'off' AND status = 'active';

CREATE TABLE crawl_runs (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id            uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    -- Copied from the site, so one active crawl per host across Outflare.
    host               text NOT NULL,
    status             text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'stopped', 'failed')),
    -- manual (someone asked) or schedule.
    trigger            text NOT NULL CHECK (trigger IN ('manual', 'schedule')),
    -- Why it ended: done (nothing left), limit (max pages), stopped,
    -- site_errors, opted_out, robots_unreachable.
    reason             text NOT NULL DEFAULT '',
    error_code         text NOT NULL DEFAULT '',
    -- The limits this run keeps, fixed when it starts.
    max_pages          integer NOT NULL,
    delay_ms           integer NOT NULL,
    obey_robots        boolean NOT NULL,
    -- Each job works for a while, then queues the next step; a job whose
    -- step is not the run's current one is a stale copy and does nothing.
    step               integer NOT NULL DEFAULT 0,
    -- Set while a step works, so a second run on the same host waits.
    lease_until        timestamptz,
    consecutive_errors integer NOT NULL DEFAULT 0,
    pages_fetched      integer NOT NULL DEFAULT 0,
    pages_blocked      integer NOT NULL DEFAULT 0,
    pages_failed       integer NOT NULL DEFAULT 0,
    pages_found        integer NOT NULL DEFAULT 0,
    bytes              bigint NOT NULL DEFAULT 0,
    created_by         uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    started_at         timestamptz,
    finished_at        timestamptz
);
CREATE UNIQUE INDEX crawl_runs_one_active_idx ON crawl_runs (site_id) WHERE status IN ('queued', 'running');
CREATE INDEX crawl_runs_site_idx ON crawl_runs (workspace_id, site_id, created_at DESC);
CREATE INDEX crawl_runs_host_idx ON crawl_runs (host) WHERE status IN ('queued', 'running');

-- Every URL a run found: its queue while crawling and its result after.
CREATE TABLE crawl_pages (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id       uuid NOT NULL REFERENCES crawl_runs (id) ON DELETE CASCADE,
    url          text NOT NULL,
    depth        integer NOT NULL,
    -- How it was found: start, link, sitemap or redirect.
    source       text NOT NULL CHECK (source IN ('start', 'link', 'sitemap', 'redirect')),
    found_on     text NOT NULL DEFAULT '',
    -- queued, fetched, blocked (robots.txt or meta), failed, skipped.
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'fetched', 'blocked', 'failed', 'skipped')),
    reason       text NOT NULL DEFAULT '',
    http_status  integer,
    content_type text NOT NULL DEFAULT '',
    bytes        integer NOT NULL DEFAULT 0,
    duration_ms  integer NOT NULL DEFAULT 0,
    -- Where a redirect led; the target is a page of its own when it is on
    -- the site, so chains can be followed hop by hop.
    redirect_to  text NOT NULL DEFAULT '',
    noindex      boolean NOT NULL DEFAULT false,
    nofollow     boolean NOT NULL DEFAULT false,
    links_out    integer NOT NULL DEFAULT 0,
    fetched_at   timestamptz,
    UNIQUE (run_id, url)
);
CREATE INDEX crawl_pages_queue_idx ON crawl_pages (run_id, depth, id) WHERE status = 'queued';

-- The robots.txt each run read, kept so an audit can show why a page was
-- skipped.
CREATE TABLE crawl_robots (
    run_id     uuid NOT NULL REFERENCES crawl_runs (id) ON DELETE CASCADE,
    host       text NOT NULL,
    -- The HTTP status, or 0 when the site did not answer.
    status     integer NOT NULL,
    body       text NOT NULL DEFAULT '',
    fetched_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, host)
);

-- Hosts whose owners asked by email not to be crawled from this install.
-- A host covers its subdomains. Kept by the operator, not by workspaces.
CREATE TABLE crawl_optouts (
    host       text PRIMARY KEY,
    note       text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now()
);
