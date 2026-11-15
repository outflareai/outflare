-- A brand's own playbooks: a name and a purpose around rules, journeys,
-- lists and signal types the brand already has, so the things that work
-- together are found, switched on and measured together. A playbook
-- owns none of its parts: removing it leaves them as they are.
CREATE TABLE own_playbooks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    title        text NOT NULL CHECK (length(title) BETWEEN 1 AND 80),
    summary      text NOT NULL DEFAULT '' CHECK (length(summary) <= 300),
    -- [{"kind": "rule", "id": "<uuid>"}, {"kind": "signal_type", "id": "<key>"}]
    parts        jsonb NOT NULL DEFAULT '[]',
    created_by   uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX own_playbooks_brand ON own_playbooks (brand_id, created_at);
CREATE INDEX own_playbooks_workspace ON own_playbooks (workspace_id);
