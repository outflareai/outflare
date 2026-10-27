-- What a crawl's audit found: one row for each rule a page breaks, worked
-- out when the crawl ends, and the site's health score for that crawl.
CREATE TABLE crawl_issues (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id       uuid NOT NULL REFERENCES crawl_runs (id) ON DELETE CASCADE,
    page_id      bigint NOT NULL REFERENCES crawl_pages (id) ON DELETE CASCADE,
    -- The rule's id, like title_missing; the catalog is in the engine.
    rule         text NOT NULL,
    severity     text NOT NULL CHECK (severity IN ('error', 'warning', 'notice')),
    -- The page's URL template, like /blog/*, so an issue on many pages
    -- made from one template reads as one fix.
    template     text NOT NULL DEFAULT '',
    -- A short fact about this case: the broken link's target, the
    -- duplicate's URL, the title's length.
    detail       text NOT NULL DEFAULT '',
    UNIQUE (run_id, page_id, rule, detail)
);
CREATE INDEX crawl_issues_rule_idx ON crawl_issues (run_id, rule, id);
CREATE INDEX crawl_issues_page_idx ON crawl_issues (page_id);

ALTER TABLE crawl_runs
    -- 0 to 100: the share of the crawl's pages with no error. NULL until
    -- the audit has run.
    ADD COLUMN health_score integer,
    ADD COLUMN issues_errors   integer NOT NULL DEFAULT 0,
    ADD COLUMN issues_warnings integer NOT NULL DEFAULT 0,
    ADD COLUMN issues_notices  integer NOT NULL DEFAULT 0;

-- The page as served was an empty app shell that its scripts fill in
-- (js_only_content), whether or not a scraping connector then read it.
ALTER TABLE crawl_pages ADD COLUMN js_shell boolean NOT NULL DEFAULT false;
