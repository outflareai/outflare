-- Match quality: what an ad network says about how well the conversions
-- Outflare sends it match its own accounts (Meta's event match quality: a
-- score out of 10 per event, how many events carried each identifier, and
-- the problems it found). A daily job reads it for each pixel a brand's
-- conversion rules send to and keeps one row per event and day, so the
-- send log can show the score, how it moved and what to fix.
--
-- Expand only (docs/migrations.md): a new table the previous version never
-- reads.
CREATE TABLE ad_match_quality (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    ad_account_id uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    -- The pixel (dataset) on the network, as on the conversion rule.
    target_id     text NOT NULL CHECK (length(target_id) BETWEEN 1 AND 200),
    -- The network's event name, such as Purchase.
    event         text NOT NULL CHECK (length(event) BETWEEN 1 AND 200),
    day           date NOT NULL,
    -- Out of 10. Null when the network gave no score for the event.
    score         numeric(4, 2) CHECK (score BETWEEN 0 AND 10),
    -- [{"key": "email", "percent": 98.2}]: the share of events that carried
    -- each identifier.
    keys          jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(keys) = 'array'),
    -- [{"name", "description", "solution", "percent"}]: what the network
    -- says is wrong and how to fix it.
    issues        jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(issues) = 'array'),
    fetched_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ad_account_id, target_id, event, day)
);

CREATE INDEX ad_match_quality_brand_idx ON ad_match_quality (workspace_id, brand_id, day DESC);

-- The same rows for SQL queries (docs/query.md), scoped like every query
-- view: the caller's workspace and the brands it may read.
CREATE VIEW query.ad_match_quality WITH (security_barrier) AS
SELECT m.brand_id, m.ad_account_id, m.target_id, m.event, m.day, m.score, m.keys, m.issues, m.fetched_at
FROM ad_match_quality m JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR m.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR m.brand_id = s.brand);
