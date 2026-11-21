-- AI crawler requests a brand's own server reports, for a site that isn't
-- behind a CDN whose logs Outflare reads. One row per host, day and
-- crawler holds how many requests came: no user agent, path or address
-- is kept. Where a connection's logs (ai_bot_hits) cover the same host
-- and day, readers use the logs and leave these out.
CREATE TABLE ai_bot_reports (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    host         text NOT NULL CHECK (length(host) BETWEEN 1 AND 253),
    day          date NOT NULL,
    -- The crawler's token, such as GPTBot.
    token        text NOT NULL,
    requests     bigint NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, host, day, token)
);
CREATE INDEX ai_bot_reports_workspace_idx ON ai_bot_reports (workspace_id, day);
