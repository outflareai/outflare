-- Insights, phase 4 (docs/insights.md): paths join the saved insight
-- kinds, and annotations mark a day on every chart of a brand ("launched
-- the new pricing page"), so a jump or a dip has its reason beside it.

-- Widening the list of kinds only lets more rows in: the version before
-- keeps reading the kinds it knows.
ALTER TABLE insights DROP CONSTRAINT insights_kind_check;
ALTER TABLE insights ADD CONSTRAINT insights_kind_check CHECK (kind IN ('funnel', 'trend', 'retention', 'paths'));

CREATE TABLE insight_annotations (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The day it marks, in UTC like every chart.
    day          date NOT NULL,
    text         text NOT NULL CHECK (length(text) BETWEEN 1 AND 300),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX insight_annotations_brand_day_idx ON insight_annotations (brand_id, day, created_at);
