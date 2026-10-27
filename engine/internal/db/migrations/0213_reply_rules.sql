-- Reply rules: what Outflare does when a reply gets a label, such as
-- snoozing a "not now" until the date it gives or adding the person a
-- referral names. One set for the workspace, and optional overrides for a
-- project or a sequence; a more specific set wins per label.
CREATE TABLE reply_rules (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    project_id   uuid REFERENCES projects (id) ON DELETE CASCADE,
    sequence_id  uuid REFERENCES sequences (id) ON DELETE CASCADE,
    -- Per label: {"not_now": {"action": "snooze", "days": 90}, ...}.
    -- Checked by the engine; labels left out follow the next set up.
    rules        jsonb NOT NULL DEFAULT '{}',
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    CHECK (project_id IS NULL OR sequence_id IS NULL)
);

CREATE UNIQUE INDEX reply_rules_scope_idx ON reply_rules (workspace_id,
    COALESCE(project_id, '00000000-0000-0000-0000-000000000000'),
    COALESCE(sequence_id, '00000000-0000-0000-0000-000000000000'));
