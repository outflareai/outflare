-- Insights and dashboards (docs/insights.md): saved analytics questions,
-- such as a funnel from an ad visit to a first payment, and dashboards
-- that put several on one page. A saved insight keeps only its spec (the
-- same JSON the API runs), never results: every view asks again.

CREATE TABLE insights (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('funnel', 'trend', 'retention')),
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    -- Dates are kept as a range relative to today (last 30 days) or fixed.
    spec         jsonb NOT NULL CHECK (jsonb_typeof(spec) = 'object' AND pg_column_size(spec) <= 65536),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX insights_brand_idx ON insights (brand_id, updated_at DESC, id DESC);

CREATE TABLE dashboards (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    -- The date range and filters every tile takes unless it pins its own.
    settings     jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(settings) = 'object' AND pg_column_size(settings) <= 16384),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX dashboards_brand_idx ON dashboards (brand_id, updated_at DESC, id DESC);

-- A tile shows a saved insight, or a note when insight_id is null. The
-- note is always written ('' beside an insight), so a row says which it is.
CREATE TABLE dashboard_tiles (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    dashboard_id uuid NOT NULL REFERENCES dashboards (id) ON DELETE CASCADE,
    insight_id   uuid REFERENCES insights (id) ON DELETE CASCADE,
    note         text NOT NULL CHECK (length(note) <= 4000),
    -- Place on a 12-column grid.
    x            integer NOT NULL DEFAULT 0 CHECK (x BETWEEN 0 AND 11),
    y            integer NOT NULL DEFAULT 0 CHECK (y BETWEEN 0 AND 10000),
    w            integer NOT NULL DEFAULT 6 CHECK (w BETWEEN 1 AND 12),
    h            integer NOT NULL DEFAULT 4 CHECK (h BETWEEN 1 AND 24),
    created_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (insight_id IS NOT NULL OR note <> '')
);
CREATE INDEX dashboard_tiles_dashboard_idx ON dashboard_tiles (dashboard_id, y, x);
CREATE INDEX dashboard_tiles_insight_idx ON dashboard_tiles (insight_id) WHERE insight_id IS NOT NULL;
