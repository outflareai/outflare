-- Saved views of the drill-down report (plans/ad-tracker.md, B5b): its
-- grouping, filters, sort, dates, time zone, model and custom columns,
-- and how the app shows it. A view is its maker's until shared with the
-- brand's team.
CREATE TABLE report_views (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    spec         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(spec) = 'object'),
    shared       boolean NOT NULL DEFAULT false,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX report_views_brand_idx ON report_views (workspace_id, brand_id, lower(name));
