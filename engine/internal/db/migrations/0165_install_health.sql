-- Install health: what a workspace put on its site or wired to Outflare
-- that has gone quiet, fallen behind or broken, such as a tracking script
-- with no visits for two days, an old copy of the script, a site whose
-- ownership proof is gone, a connection that fails, or an SDK or MCP
-- server older than the API supports. Health is worked out on read; this
-- table remembers when each problem was first and last seen, so it is
-- announced once (install.issue_opened) and again when it clears
-- (install.issue_resolved).
CREATE TABLE install_issues (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    -- Stable per problem and thing: site_script_silent:<site key id>.
    key          text NOT NULL,
    kind         text NOT NULL,
    severity     text NOT NULL CHECK (severity IN ('critical', 'warning', 'info')),
    title        text NOT NULL,
    opened_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz
);

CREATE UNIQUE INDEX install_issues_open_idx ON install_issues (workspace_id, key) WHERE resolved_at IS NULL;
CREATE INDEX install_issues_workspace_idx ON install_issues (workspace_id, opened_at DESC);

-- The version of the tracking script that last sent events with a key;
-- empty for copies from before the script said.
ALTER TABLE site_keys ADD COLUMN script_version text NOT NULL DEFAULT '';
