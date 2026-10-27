-- AI crawlers in a site's request logs: how many requests each AI
-- crawler made to each host per day, read from a CDN connection that has
-- logs (Cloudflare), so a workspace sees which AI products actually read
-- its site, next to what robots.txt allows.

CREATE TABLE ai_bot_hits (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    host           text NOT NULL,
    day            date NOT NULL,
    -- The crawler's robots.txt token, such as GPTBot.
    token          text NOT NULL,
    requests       bigint NOT NULL,
    synced_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, host, day, token)
);
CREATE INDEX ai_bot_hits_workspace_idx ON ai_bot_hits (workspace_id, day);

-- Each connection's last read of its logs: ok, or why not (a token
-- without the zone permissions, say), without failing the rest of its sync.
CREATE TABLE ai_bot_syncs (
    integration_id uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    status         text NOT NULL CHECK (status IN ('ok', 'error')),
    error_code     text NOT NULL DEFAULT '',
    message        text NOT NULL DEFAULT '',
    rows           integer NOT NULL DEFAULT 0,
    last_run_at    timestamptz NOT NULL DEFAULT now()
);
