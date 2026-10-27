-- SEO experiments: did a change to a website's pages help? A before and
-- after experiment compares the pages with themselves; a holdout
-- experiment splits them into matched groups, the change goes only to the
-- treatment group, and the control group shows what would have happened
-- anyway (seasonality, a Google update). The change starts the
-- measurement: a production deploy of the site (deploys) or a person
-- saying when it went live. Outcomes come from the workspace's own Search
-- Console data (search_daily).

CREATE TABLE experiments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id         uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    -- The site's brand, for brand filters; null is shared.
    brand_id        uuid REFERENCES brands (id) ON DELETE SET NULL,
    name            text NOT NULL,
    hypothesis      text NOT NULL DEFAULT '',
    design          text NOT NULL CHECK (design IN ('before_after', 'holdout')),
    -- Which pages: a path pattern such as /blog/*, or empty for every page
    -- Search Console reports.
    pattern         text NOT NULL DEFAULT '',
    metric          text NOT NULL DEFAULT 'clicks' CHECK (metric IN ('clicks', 'impressions', 'ctr', 'position')),
    -- The share of pages kept as the control group, for holdouts.
    holdout_percent smallint NOT NULL DEFAULT 50 CHECK (holdout_percent BETWEEN 10 AND 90),
    baseline_days   smallint NOT NULL DEFAULT 28 CHECK (baseline_days BETWEEN 7 AND 90),
    measure_days    smallint NOT NULL DEFAULT 28 CHECK (measure_days BETWEEN 7 AND 90),
    -- What starts the measurement: the site's next production deploy, or
    -- a person saying when the change went live.
    start_on        text NOT NULL DEFAULT 'deploy' CHECK (start_on IN ('deploy', 'manual')),
    -- draft (pages picked), waiting (for the deploy), measuring, done or
    -- stopped.
    status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'waiting', 'measuring', 'done', 'stopped')),
    changed_at      timestamptz,
    deploy_id       uuid REFERENCES deploys (id) ON DELETE SET NULL,
    -- The result when it ended, as the API returns it.
    result          jsonb,
    concluded_at    timestamptz,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX experiments_ws_idx ON experiments (workspace_id, created_at DESC);
CREATE INDEX experiments_site_idx ON experiments (site_id, status);

-- The pages in an experiment and their group. Groups are matched: pages
-- are ranked by their baseline and spread across the groups in turn, so
-- both have the same mix of busy and quiet pages.
CREATE TABLE experiment_pages (
    experiment_id  uuid NOT NULL REFERENCES experiments (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    url            text NOT NULL,
    grp            text NOT NULL CHECK (grp IN ('treatment', 'control')),
    -- The page's metric in the days before the experiment was made.
    baseline       double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (experiment_id, url)
);
