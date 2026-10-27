-- Conversions by landing page: each visit counted under the page it
-- started on, and whether it went on to one of the events the site counts
-- as conversions, from product analytics with raw events (PostHog). The
-- SEO module joins it to Search Console by page.
CREATE TABLE page_conversions (
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id     uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- The site at the tool (a PostHog project id).
    site               text NOT NULL,
    day                date NOT NULL,
    -- The host and path the visit landed on, as the tool recorded them.
    host               text NOT NULL,
    path               text NOT NULL,
    sessions           integer NOT NULL DEFAULT 0,
    visitors           integer NOT NULL DEFAULT 0,
    conversions        integer NOT NULL DEFAULT 0,
    -- Visits that came from a search engine's results, and their conversions.
    search_sessions    integer NOT NULL DEFAULT 0,
    search_conversions integer NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, site, day, host, path)
);
CREATE INDEX page_conversions_ws_idx ON page_conversions (workspace_id, host, day);

-- One row per site a connection reads conversions for.
CREATE TABLE page_conversion_syncs (
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site           text NOT NULL,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The events counted when the stored days were read; changing them
    -- reads the 90 days again.
    events         text[] NOT NULL DEFAULT '{}',
    oldest_day     date,
    newest_day     date,
    backfilled_at  timestamptz,
    last_run_at    timestamptz NOT NULL DEFAULT now(),
    last_error     text,
    PRIMARY KEY (integration_id, site)
);
CREATE INDEX page_conversion_syncs_ws_idx ON page_conversion_syncs (workspace_id);
