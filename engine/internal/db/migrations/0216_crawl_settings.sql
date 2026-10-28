-- Crawl settings modelled on what site owners expect from a site audit:
-- whether pages run their scripts first, which pages are in scope, how
-- deep the crawl goes, and when a scheduled crawl starts. Each run keeps a
-- copy of the settings it started with, so its results say how it crawled.

ALTER TABLE seo_sites
    -- off: read pages as served. auto: run a page's scripts only when it is
    -- an empty app shell. always: run every page's scripts. Scripts run
    -- through the workspace's own scraping connection (Geonode), and only
    -- on a site it has proved it owns.
    ADD COLUMN render_js        text NOT NULL DEFAULT 'auto' CHECK (render_js IN ('off', 'auto', 'always')),
    ADD COLUMN use_sitemaps     boolean NOT NULL DEFAULT true,
    -- Follow links marked rel="nofollow" too.
    ADD COLUMN follow_nofollow  boolean NOT NULL DEFAULT false,
    -- Crawl /page?a=1 and /page?a=2 as /page.
    ADD COLUMN ignore_params    boolean NOT NULL DEFAULT false,
    -- Clicks from the start page; 0 is no limit.
    ADD COLUMN max_depth        integer NOT NULL DEFAULT 0 CHECK (max_depth BETWEEN 0 AND 100),
    -- Regular expressions over a URL's path and query. With include
    -- patterns, only matching pages are crawled; exclude patterns win.
    ADD COLUMN include_patterns text[] NOT NULL DEFAULT '{}',
    ADD COLUMN exclude_patterns text[] NOT NULL DEFAULT '{}',
    -- When a scheduled crawl starts: the hour (and, weekly, the day, 0 is
    -- Sunday) in schedule_tz. Null starts it any time.
    ADD COLUMN schedule_hour    smallint CHECK (schedule_hour BETWEEN 0 AND 23),
    ADD COLUMN schedule_day     smallint CHECK (schedule_day BETWEEN 0 AND 6),
    ADD COLUMN schedule_tz      text NOT NULL DEFAULT 'UTC';

ALTER TABLE crawl_runs
    ADD COLUMN settings       jsonb NOT NULL DEFAULT '{}',
    -- Links left out by the scope settings (patterns and depth).
    ADD COLUMN pages_excluded integer NOT NULL DEFAULT 0,
    -- Pages read after running their scripts.
    ADD COLUMN pages_rendered integer NOT NULL DEFAULT 0;
