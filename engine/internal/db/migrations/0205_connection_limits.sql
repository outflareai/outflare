-- Usage limits for paid connections: a daily and a monthly cap per
-- connection, in the provider's own unit (credits, API units, tokens) or
-- in US dollars, counted from the spend ledger. Every paid call checks
-- them first, and owners and admins are warned at 80% and when a cap is
-- reached. The calls run on the workspace's own account with the provider;
-- these caps only stop Outflare from making more of them.
CREATE TABLE connection_limits (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    -- What the caps count: USD (money in the ledger), or a unit name the
    -- ledger books for this connection (credit, API unit, token).
    unit           text NOT NULL CHECK (unit ~ '^[A-Za-z][A-Za-z ]{0,49}$'),
    daily_limit    numeric CHECK (daily_limit >= 0),
    monthly_limit  numeric CHECK (monthly_limit >= 0),
    -- false only warns: calls go on past the cap.
    hard_stop      boolean NOT NULL DEFAULT true,
    updated_at     timestamptz NOT NULL DEFAULT now(),
    updated_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    PRIMARY KEY (workspace_id, integration_id)
);

CREATE INDEX connection_limits_integration ON connection_limits (integration_id);

-- Which warnings a connection's admins were sent, so each goes out once a
-- period: level 80 at 80% of a cap, 100 when it is reached.
CREATE TABLE connection_limit_alerts (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    period         text NOT NULL CHECK (period IN ('day', 'month')),
    period_start   date NOT NULL,
    level          int NOT NULL CHECK (level IN (80, 100)),
    unit           text NOT NULL,
    limit_value    numeric NOT NULL,
    used           numeric NOT NULL,
    sent_at        timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, period, period_start, level)
);

CREATE INDEX connection_limit_alerts_ws ON connection_limit_alerts (workspace_id, sent_at DESC);

-- Usage per connection is summed from the ledger.
CREATE INDEX spend_entries_integration_idx ON spend_entries (integration_id, occurred_at) WHERE integration_id IS NOT NULL;

-- SEO connections kept their monthly budget in their config. It becomes
-- the connection's monthly limit; the config value stays so the previous
-- version still reads it.
INSERT INTO connection_limits (workspace_id, integration_id, unit, monthly_limit)
SELECT workspace_id, id, CASE WHEN connector = 'dataforseo' THEN 'USD' ELSE 'API unit' END, (config->>'monthly_budget')::numeric
FROM integrations
WHERE connector IN ('ahrefs', 'dataforseo') AND jsonb_typeof(config->'monthly_budget') = 'number' AND (config->>'monthly_budget')::numeric >= 0
ON CONFLICT DO NOTHING;
