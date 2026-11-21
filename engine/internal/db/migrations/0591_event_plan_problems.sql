-- Tracking plans: an event in the catalog lists the properties it should
-- carry, and marks the ones every event must have. As events arrive, the
-- ones that don't follow it are counted here, per day, so a person can
-- see what their code sends wrong. Nothing is dropped: the events are
-- stored as usual.

CREATE TABLE event_plan_problems (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    event_name   text NOT NULL,
    -- missing_property: a required property was absent or null.
    -- unplanned_property: a property the event doesn't list.
    kind         text NOT NULL CHECK (kind IN ('missing_property', 'unplanned_property')),
    property     text NOT NULL CHECK (length(property) BETWEEN 1 AND 100),
    day          date NOT NULL,
    -- How many events had the problem that day.
    events       bigint NOT NULL DEFAULT 0,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, event_name, kind, property, day)
);
CREATE INDEX event_plan_problems_workspace_idx ON event_plan_problems (workspace_id);
CREATE INDEX event_plan_problems_day_idx ON event_plan_problems (brand_id, day);
