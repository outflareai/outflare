-- Rank tracking: the keywords a workspace follows for an SEO site, and
-- where the site ranked for each, check by check. Checks read a search
-- engine's results through the workspace's own SERP connection (DataForSEO
-- for Google), within that connection's monthly budget; each check's cost
-- is in the Outcomes ledger.
CREATE TABLE tracked_keywords (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id        uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    -- Lowercase, single spaces.
    keyword        text NOT NULL,
    country        text NOT NULL DEFAULT 'US',
    device         text NOT NULL DEFAULT 'desktop' CHECK (device IN ('desktop', 'mobile')),
    engine         text NOT NULL DEFAULT 'google',
    -- The SERP connection that checks it; NULL uses the workspace's first.
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- How often it is checked: daily or weekly.
    frequency      text NOT NULL DEFAULT 'weekly' CHECK (frequency IN ('daily', 'weekly')),
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    last_checked_at timestamptz,
    last_error     text,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, keyword, country, device, engine)
);
CREATE INDEX tracked_keywords_ws_idx ON tracked_keywords (workspace_id, site_id);
CREATE INDEX tracked_keywords_due_idx ON tracked_keywords (last_checked_at) WHERE status = 'active';

-- One check of a tracked keyword a day.
CREATE TABLE rank_observations (
    id             bigserial PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    tracked_id     uuid NOT NULL REFERENCES tracked_keywords (id) ON DELETE CASCADE,
    day            date NOT NULL,
    -- The site's best organic position; NULL when it is not in the
    -- results read (depth).
    position       integer,
    url            text NOT NULL DEFAULT '',
    depth          integer NOT NULL,
    engine         text NOT NULL,
    -- The top 10 results: [{"position": 1, "domain": "...", "url": "..."}].
    top            jsonb NOT NULL DEFAULT '[]',
    cost_usd       numeric(10, 4) NOT NULL DEFAULT 0,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    checked_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (tracked_id, day)
);
CREATE INDEX rank_observations_ws_idx ON rank_observations (workspace_id, tracked_id, day DESC);
