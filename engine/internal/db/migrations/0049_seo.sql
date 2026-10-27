-- SEO history: what each SEO connection (DataForSEO, Ahrefs) reported about
-- a domain on a day: one overview row (key '') and one row per keyword,
-- linking domain, competitor or audit issue. Snapshots are weekly, because
-- every call costs money.

CREATE TABLE seo_snapshots (
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    connector       text NOT NULL,
    -- The domain, which lines SEO up with the analytics tools.
    host            text NOT NULL,
    -- overview, keywords, backlinks, competitors or audit.
    report          text NOT NULL,
    day             date NOT NULL,
    country         text NOT NULL DEFAULT '',
    key             text NOT NULL DEFAULT '',
    url             text NOT NULL DEFAULT '',
    kind            text NOT NULL DEFAULT '',
    -- connectors.SEOMetrics; a missing field is not measured.
    metrics         jsonb NOT NULL DEFAULT '{}',
    synced_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, host, report, day, key)
);
CREATE INDEX seo_snapshots_ws_idx ON seo_snapshots (workspace_id, host, report, day);

-- One row per SEO connection and domain: how its last snapshot went.
CREATE TABLE seo_syncs (
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    host            text NOT NULL,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    last_run_at     timestamptz,
    last_ok_at      timestamptz,
    last_error      text,
    -- What the last run cost: dollars (DataForSEO) or API units (Ahrefs).
    last_cost       double precision NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, host)
);
CREATE INDEX seo_syncs_ws_idx ON seo_syncs (workspace_id);
