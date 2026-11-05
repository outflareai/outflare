-- Dashboards shared by link, and alerts on a number (docs/insights.md).

-- A dashboard's share link: anyone with the link (and the password, when
-- one is set) sees the dashboard read-only, with every tile run live.
-- One link per dashboard; making a new one replaces it. Only the hash of
-- the token is kept, like an invite's.
CREATE TABLE dashboard_shares (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    dashboard_id   uuid NOT NULL UNIQUE REFERENCES dashboards (id) ON DELETE CASCADE,
    token_hash     bytea NOT NULL UNIQUE,
    -- An argon2id hash, or null when the link alone opens it.
    password_hash  text,
    expires_at     timestamptz,
    views          integer NOT NULL DEFAULT 0,
    last_viewed_at timestamptz,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);

-- An alert on a number: a saved trend checked every hour, raising an
-- alert (kind insight.threshold) when its total crosses a line, once per
-- crossing.
CREATE TABLE insight_alerts (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    insight_id      uuid NOT NULL REFERENCES insights (id) ON DELETE CASCADE,
    -- above or below a value; rises_by or falls_by a percentage against
    -- the period before.
    condition       text NOT NULL CHECK (condition IN ('above', 'below', 'rises_by', 'falls_by')),
    threshold       double precision NOT NULL,
    -- ok, firing, or unknown before the first check (or when the trend
    -- had no number).
    state           text NOT NULL DEFAULT 'unknown' CHECK (state IN ('unknown', 'ok', 'firing')),
    last_value      double precision,
    last_checked_at timestamptz,
    fired_at        timestamptz,
    next_check_at   timestamptz NOT NULL DEFAULT now(),
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX insight_alerts_brand_idx ON insight_alerts (brand_id, created_at DESC);
CREATE INDEX insight_alerts_due_idx ON insight_alerts (next_check_at);
