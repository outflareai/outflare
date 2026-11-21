-- A holdout test for paid ads (docs/paid-media.md, "Holdout tests"): the
-- brand stops showing ads in some countries for a while, and Outflare
-- compares sign-ups there with everywhere else, before and during, to say
-- how many sign-ups the ads were really adding. The row is only what was
-- held out and when; the numbers are counted from visits and events on
-- every read. Only adds a table, so the version before still works.
CREATE TABLE ad_holdouts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    -- The network whose ads are held out (google_ads, meta_ads, ...); ''
    -- when every network's are.
    connector    text NOT NULL DEFAULT '' CHECK (length(connector) <= 40),
    -- ISO 3166-1 alpha-2 codes of the countries kept out.
    countries    text[] NOT NULL CHECK (cardinality(countries) BETWEEN 1 AND 50),
    starts_on    date NOT NULL,
    ends_on      date NOT NULL,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_on >= starts_on)
);
CREATE INDEX ad_holdouts_brand_idx ON ad_holdouts (brand_id, starts_on DESC);
CREATE INDEX ad_holdouts_ws_idx ON ad_holdouts (workspace_id);

-- The same rows for SQL queries (docs/query.md), scoped like every query
-- view: the caller's workspace and the brands it may read.
CREATE VIEW query.ad_holdouts WITH (security_barrier) AS
SELECT h.id, h.brand_id, h.name, h.connector, h.countries, h.starts_on, h.ends_on, h.created_at, h.updated_at
FROM ad_holdouts h JOIN query_scopes s ON s.pid = pg_backend_pid() AND h.workspace_id = s.workspace_id
WHERE 'analytics.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR h.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR h.brand_id = s.brand);
