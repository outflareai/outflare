-- Organic traffic and money by country: first-touch Organic Search
-- sessions (with their time on the site), signups and payments from
-- PostHog, by day, host and the visitor's country, so the SEO map can show
-- where search brings money and where it brings only clicks. Payments are
-- counted once, as in organic_conversions. The sessions come from a
-- separate read and stay empty when the analytics tool can't say a
-- session's country. Rows whose host belongs to no brand are not kept.
CREATE TABLE organic_countries (
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    seo_site_id      uuid REFERENCES seo_sites (id) ON DELETE CASCADE,
    integration_id   uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    site             text NOT NULL,
    day              date NOT NULL,
    host             text NOT NULL,
    -- ISO 3166-1 alpha-2, uppercase; '' when the tool did not know.
    country          text NOT NULL,
    organic_sessions integer NOT NULL DEFAULT 0,
    session_seconds  double precision NOT NULL DEFAULT 0,
    signups          integer NOT NULL DEFAULT 0,
    payments         integer NOT NULL DEFAULT 0,
    revenue_usd      numeric(18, 2) NOT NULL DEFAULT 0,
    PRIMARY KEY (integration_id, site, day, host, country)
);
CREATE INDEX organic_countries_brand_idx ON organic_countries (brand_id, day);

CREATE VIEW query.seo_organic_countries WITH (security_barrier) AS
SELECT c.brand_id, c.seo_site_id AS site_id, c.day, c.host, c.country,
    c.organic_sessions, c.session_seconds, c.signups, c.payments, c.revenue_usd
FROM organic_countries c JOIN query_scopes s ON s.pid = pg_backend_pid() AND c.workspace_id = s.workspace_id
WHERE 'seo.read' = ANY (s.scopes) AND (s.brands IS NULL OR c.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR c.brand_id = s.brand);
