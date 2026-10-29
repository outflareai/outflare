-- Journey actions (customer data platform step 2): static lists of people
-- beside smart lists, and exit criteria on journeys.

-- A static list holds the people someone, the API or a journey step puts
-- in it; nothing refreshes it. It is a smart_lists row of kind static, so
-- the Lists page, broadcasts and rules treat both kinds alike, with its
-- members in a table of their own: a version from before static lists
-- reads such a row as a smart list, and a refresh by it must never fill
-- the list with whoever its rules match. New static lists also carry a
-- rule that matches nobody and a refreshed_at far in the future.
ALTER TABLE smart_lists ADD COLUMN kind text NOT NULL DEFAULT 'smart' CHECK (kind IN ('smart', 'static'));

CREATE TABLE static_list_members (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    list_id      uuid NOT NULL REFERENCES smart_lists (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    entered_at   timestamptz NOT NULL DEFAULT now(),
    -- Who put them in: manual (a member), api, journey (added_ref is the
    -- run) or import.
    added_via    text NOT NULL DEFAULT 'manual',
    added_ref    text,
    PRIMARY KEY (list_id, record_id)
);
CREATE INDEX static_list_members_record_idx ON static_list_members (workspace_id, record_id);
CREATE INDEX static_list_members_entered_idx ON static_list_members (list_id, entered_at DESC, record_id);

-- Exit criteria: a run ends at once when the person does one of these
-- events, even mid-wait, or before its next step when these conditions
-- hold. Separate from the goal, which is for reporting.
ALTER TABLE journeys ADD COLUMN exit_events text[] NOT NULL DEFAULT '{}';
ALTER TABLE journeys ADD COLUMN exit_conditions jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(exit_conditions) = 'array');
CREATE INDEX journeys_exit_events ON journeys USING gin (exit_events) WHERE status IN ('active', 'paused');
