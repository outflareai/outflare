-- Paid ads suggestions (step 2 of the paid ads plan): Outflare reads each
-- campaign's recent numbers and suggests one change at a time (raise or
-- lower its daily budget, pause it, resume it), with why. A person approves
-- or dismisses each; an approved one becomes an ad_changes row as if they
-- had made it, and is then measured over a window long enough to judge
-- (the platform's learning period, at least a week, enough conversions and
-- their lag) and given a verdict: worked, didn't work or not sure.
--
-- Expand only (docs/migrations.md): one new table. The previous version
-- ignores it.

CREATE TABLE ad_suggestions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The campaign's brand (its own, else its account's); null when the
    -- account is shared by every brand.
    brand_id      uuid REFERENCES brands(id) ON DELETE SET NULL,
    ad_entity_id  uuid NOT NULL REFERENCES ad_entities(id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('budget_up', 'budget_down', 'pause', 'resume')),
    -- open (waits for a person), dismissed, expired (no longer true when
    -- the numbers were read again), measuring (approved and made), judged.
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'dismissed', 'expired', 'measuring', 'judged')),
    -- Why, in a sentence, and the numbers behind it.
    reason        text NOT NULL DEFAULT '',
    evidence      jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(evidence) = 'object'),
    -- The campaign's state the change asks for (status, daily budget).
    proposed      jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(proposed) = 'object'),
    change_id     uuid REFERENCES ad_changes(id) ON DELETE SET NULL,
    decided_by    uuid REFERENCES actors(id) ON DELETE SET NULL,
    decided_at    timestamptz,
    -- The days the change is measured over, after its learning period,
    -- and the days before it compared with them.
    measure_from  date,
    measure_until date,
    window_note   text NOT NULL DEFAULT '',
    verdict       text NOT NULL DEFAULT '' CHECK (verdict IN ('', 'worked', 'did_not_work', 'not_sure')),
    verdict_note  text NOT NULL DEFAULT '',
    result        jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(result) = 'object'),
    judged_at     timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ad_suggestions_workspace_idx ON ad_suggestions (workspace_id, status, created_at DESC);
CREATE INDEX ad_suggestions_entity_idx ON ad_suggestions (ad_entity_id, created_at DESC);
-- One open suggestion of a kind per campaign.
CREATE UNIQUE INDEX ad_suggestions_open_idx ON ad_suggestions (ad_entity_id, kind) WHERE status = 'open';
