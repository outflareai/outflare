-- Paid ads management (the brand agent plan, step 7): Outflare pauses and
-- resumes a campaign and sets its daily budget on the workspace's own ad
-- accounts, when a person asks, or an agent within the grants a person
-- set. Each change is a row: what it asked for, what the campaign was
-- before, how the job that carried it out ended, and whether the platform
-- showed the change afterwards.
--
-- Expand only (docs/migrations.md): one new table. The previous version
-- ignores it.

CREATE TABLE ad_changes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The campaign's brand (its own, else its account's); null when the
    -- account is shared by every brand.
    brand_id     uuid REFERENCES brands(id) ON DELETE SET NULL,
    ad_entity_id uuid NOT NULL REFERENCES ad_entities(id) ON DELETE CASCADE,
    -- pause, resume or budget.
    kind         text NOT NULL CHECK (kind IN ('pause', 'resume', 'budget')),
    -- The campaign's status and daily budget before, and what the change
    -- asked for.
    before       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(before) = 'object'),
    after        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(after) = 'object'),
    -- queued (the job has not run), applied (the platform took it),
    -- failed (it refused, or stayed unreachable).
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'applied', 'failed')),
    error        text NOT NULL DEFAULT '',
    -- When the campaign was read back from the platform and showed the
    -- change; note says what it showed instead when it did not.
    verified_at  timestamptz,
    verify_note  text NOT NULL DEFAULT '',
    -- Who asked: the actor, and the agent's action when one did.
    actor_id     uuid REFERENCES actors(id) ON DELETE SET NULL,
    action_id    uuid REFERENCES actions(id) ON DELETE SET NULL,
    -- A brand-wide pause groups every campaign it paused.
    batch_id     uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    applied_at   timestamptz
);
CREATE INDEX ad_changes_workspace_idx ON ad_changes (workspace_id, created_at DESC);
CREATE INDEX ad_changes_entity_idx ON ad_changes (ad_entity_id, created_at DESC);
