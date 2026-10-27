-- Analytics: answers from connected analytics tools (Google Analytics,
-- Search Console, PostHog, Plausible and the rest), kept for a while so
-- dashboards and agents do not spend the provider's rate limits. Nothing
-- here is the source of truth; any row can be dropped and fetched again.

CREATE TABLE analytics_cache (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- report, site, from, to and limit, joined.
    key            text NOT NULL,
    report         jsonb NOT NULL,
    fetched_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, integration_id, key)
);
