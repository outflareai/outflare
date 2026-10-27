-- Analytics history: what each connected analytics tool reported, one row
-- per site, report, day and key, so history can be queried, compared
-- across tools and added up without calling the providers again.

CREATE TABLE analytics_daily (
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    connector       text NOT NULL,
    site            text NOT NULL,
    -- The site's domain (example.com), the key that lines up one website
    -- across tools. Empty when the tool's site has no domain.
    host            text NOT NULL DEFAULT '',
    -- traffic, pages, sources, search_queries or events.
    report          text NOT NULL,
    day             date NOT NULL,
    -- The page, source, query or event; '' is the day's totals.
    key             text NOT NULL DEFAULT '',
    -- NULL: the tool does not measure it.
    visitors        double precision,
    pageviews       double precision,
    sessions        double precision,
    bounce_rate     double precision,
    avg_duration    double precision,
    clicks          double precision,
    impressions     double precision,
    ctr             double precision,
    position        double precision,
    events          double precision,
    conversions     double precision,
    synced_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, site, report, day, key)
);
CREATE INDEX analytics_daily_ws_idx ON analytics_daily (workspace_id, report, day);
CREATE INDEX analytics_daily_host_idx ON analytics_daily (workspace_id, host, day) WHERE host <> '';

-- One row per analytics connection: what the sync has stored and how its
-- last run went.
CREATE TABLE analytics_syncs (
    integration_id  uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site            text NOT NULL DEFAULT '',
    host            text NOT NULL DEFAULT '',
    -- The earliest and latest day stored.
    first_day       date,
    last_day        date,
    backfilled      boolean NOT NULL DEFAULT false,
    last_run_at     timestamptz,
    last_ok_at      timestamptz,
    last_error      text,
    -- Provider calls the last run made.
    last_calls      integer NOT NULL DEFAULT 0
);
CREATE INDEX analytics_syncs_ws_idx ON analytics_syncs (workspace_id);
