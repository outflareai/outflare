-- Page speed from real visits: one row per page load, as the site's tag
-- measured it in the visitor's browser (Core Web Vitals plus first paint
-- and server response). No visitor id, cookie or person: only the page,
-- the kind of device and the country. Kept 90 days.
CREATE TABLE page_vitals (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    key_id       uuid REFERENCES site_keys (id) ON DELETE SET NULL,
    host         text NOT NULL DEFAULT '' CHECK (length(host) <= 253),
    path         text NOT NULL DEFAULT '' CHECK (length(path) <= 1000),
    device       text NOT NULL DEFAULT '' CHECK (length(device) <= 20),
    country      text NOT NULL DEFAULT '' CHECK (length(country) <= 2),
    -- Milliseconds, except cls (a score). Null when the browser didn't
    -- measure it (Safari has no INP; a page left before any click has none).
    lcp_ms       integer CHECK (lcp_ms BETWEEN 0 AND 120000),
    inp_ms       integer CHECK (inp_ms BETWEEN 0 AND 60000),
    cls          real CHECK (cls BETWEEN 0 AND 100),
    fcp_ms       integer CHECK (fcp_ms BETWEEN 0 AND 120000),
    ttfb_ms      integer CHECK (ttfb_ms BETWEEN 0 AND 120000),
    occurred_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX page_vitals_brand_idx ON page_vitals (workspace_id, brand_id, occurred_at);
CREATE INDEX page_vitals_age_idx ON page_vitals (occurred_at);
