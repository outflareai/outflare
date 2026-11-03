-- Metrics (plans/customer-data-platform, decided by the owner on
-- 2026-11-02): live totals per company and person over a rolling window,
-- such as spend in the last 30 days, optionally split by product. They are
-- kept live as events arrive (per-day buckets), filled quietly from stored
-- events when a metric is added or changed, and written to the record's
-- attributes under "metrics", so rules, journeys, smart lists and Query
-- read them like any field. A backfill never fires anything: a record
-- already past a rule's threshold counts as having crossed it.

-- Whether an event counts as using the product: one the brand's product
-- sent through ingest (track, page or screen), not a billing sync, a
-- derived event or the website snippet.
ALTER TABLE customer_events ADD COLUMN usage boolean NOT NULL DEFAULT false;

-- A brand's metrics. Each brand starts with the defaults when it turns
-- metrics on.
CREATE TABLE metric_definitions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The name it has under attributes.metrics (the API checks the form).
    key           text NOT NULL CHECK (length(key) BETWEEN 1 AND 40),
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description   text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    -- Which records keep it: company, person or both.
    levels        text[] NOT NULL DEFAULT '{company,person}',
    -- count, sum, min, max (of property), days (days with a matching
    -- event) or last (when the latest matching event happened).
    aggregate     text NOT NULL CHECK (aggregate IN ('count', 'sum', 'min', 'max', 'days', 'last')),
    -- What it counts: these event names, and/or usage (any event the
    -- product sent through ingest).
    events        text[] NOT NULL DEFAULT '{}',
    usage         boolean NOT NULL DEFAULT false,
    -- The number property sum, min and max read, such as amount_minor.
    property      text,
    -- The window: the last window_days days, ending offset_days ago (UTC
    -- days, today included when offset_days is 0).
    window_days   integer NOT NULL DEFAULT 30 CHECK (window_days BETWEEN 1 AND 365),
    offset_days   integer NOT NULL DEFAULT 0 CHECK (offset_days BETWEEN 0 AND 365),
    -- Also keep it per product (attributes.metrics.by_product.<key>).
    per_product   boolean NOT NULL DEFAULT true,
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    version       integer NOT NULL DEFAULT 1,
    -- Set while a backfill runs; cleared when it finishes.
    backfilling   boolean NOT NULL DEFAULT false,
    backfilled_at timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX metric_definitions_key_idx ON metric_definitions (brand_id, key) WHERE status = 'active';
CREATE INDEX metric_definitions_active_idx ON metric_definitions (brand_id) WHERE status = 'active';

-- A brand's metrics setting: on or off.
CREATE TABLE metric_settings (
    brand_id      uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enabled_at    timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- The per-day totals a metric is computed from. product_id is the nil
-- uuid for the overall total.
CREATE TABLE metric_buckets (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    metric_id     uuid NOT NULL REFERENCES metric_definitions (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    product_id    uuid NOT NULL,
    day           date NOT NULL,
    n             bigint NOT NULL DEFAULT 0,
    total         numeric NOT NULL DEFAULT 0,
    lo            numeric,
    hi            numeric,
    last_at       timestamptz,
    PRIMARY KEY (metric_id, record_id, product_id, day)
);
CREATE INDEX metric_buckets_record_idx ON metric_buckets (workspace_id, record_id);

-- Each record's current value of each metric (overall and per product),
-- and when it next changes as days leave the window (for the sweep).
CREATE TABLE metric_values (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    metric_id     uuid NOT NULL REFERENCES metric_definitions (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    product_id    uuid NOT NULL,
    value         jsonb NOT NULL,
    due_at        timestamptz,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (metric_id, record_id, product_id)
);
CREATE INDEX metric_values_due_idx ON metric_values (due_at) WHERE due_at IS NOT NULL;
CREATE INDEX metric_values_record_idx ON metric_values (workspace_id, record_id);

-- Query (docs/query.md).
CREATE VIEW query.metrics WITH (security_barrier) AS
SELECT d.id, d.brand_id, d.key, d.name, d.levels, d.aggregate, d.events, d.usage, d.property, d.window_days, d.offset_days, d.per_product,
    d.status, d.version, d.backfilled_at
FROM metric_definitions d JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR d.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR d.brand_id = s.brand);

CREATE VIEW query.metric_values WITH (security_barrier) AS
SELECT v.metric_id, d.key AS metric, v.record_id, NULLIF(v.product_id, '00000000-0000-0000-0000-000000000000') AS product_id, v.brand_id,
    v.value, v.updated_at
FROM metric_values v JOIN metric_definitions d ON d.id = v.metric_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR v.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR v.brand_id = s.brand);

-- A record's metrics as stored under attributes.metrics: each metric's
-- overall value by key, and per product under by_product.<product key>
-- (lowercase letters, digits and underscores). Empty when it has none.
CREATE FUNCTION outflare_record_metrics(rec uuid) RETURNS jsonb LANGUAGE sql STABLE AS $$
    SELECT COALESCE((SELECT jsonb_object_agg(d.key, v.value) FROM metric_values v JOIN metric_definitions d ON d.id = v.metric_id
            WHERE v.record_id = rec AND d.status = 'active' AND v.product_id = '00000000-0000-0000-0000-000000000000'), '{}'::jsonb)
        || COALESCE((SELECT CASE WHEN count(*) > 0 THEN jsonb_build_object('by_product', jsonb_object_agg(pk, vals)) END FROM (
            SELECT regexp_replace(p.key, '[^a-z0-9_]', '_', 'g') AS pk, jsonb_object_agg(d.key, v.value) AS vals
            FROM metric_values v JOIN metric_definitions d ON d.id = v.metric_id JOIN products p ON p.id = v.product_id
            WHERE v.record_id = rec AND d.status = 'active' AND v.product_id <> '00000000-0000-0000-0000-000000000000'
            GROUP BY 1) x), '{}'::jsonb)
$$;
