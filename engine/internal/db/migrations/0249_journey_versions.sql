-- Journey versions: a live journey is read-only. Editing it makes a draft
-- version beside it, and publishing the draft swaps its trigger, entry,
-- goal and steps into the journey as the next version. Every version that
-- went live is kept, so it can be looked at and compared, and so people
-- who entered on it can finish on it.
CREATE TABLE journey_versions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    journey_id     uuid NOT NULL REFERENCES journeys (id) ON DELETE CASCADE,
    -- draft: being edited, at most one per journey, with no number yet.
    -- published: a version that went live, numbered as journeys.version
    -- was then; it never changes.
    status         text NOT NULL CHECK (status IN ('draft', 'published')),
    version        integer CHECK ((status = 'draft') = (version IS NULL)),
    trigger_kind   text NOT NULL CHECK (trigger_kind IN ('event', 'manual')),
    trigger_event  text,
    trigger_filter jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(trigger_filter) = 'array'),
    entry          text NOT NULL CHECK (entry IN ('once', 'again')),
    goal_event     text,
    steps          jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(steps) = 'array'),
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    published_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    published_at   timestamptz
);
CREATE UNIQUE INDEX journey_versions_number ON journey_versions (journey_id, version) WHERE status = 'published';
CREATE UNIQUE INDEX journey_versions_one_draft ON journey_versions (journey_id) WHERE status = 'draft';
CREATE INDEX journey_versions_workspace ON journey_versions (workspace_id, journey_id);

-- The version a run goes through. Null (runs from before versions) follows
-- the journey as it is now, as all runs did.
ALTER TABLE journey_runs ADD COLUMN journey_version integer;

-- Journeys already live keep their current steps as their first kept
-- version, so the runs in them can be pinned to it.
INSERT INTO journey_versions (workspace_id, journey_id, status, version, trigger_kind, trigger_event, trigger_filter, entry, goal_event,
    steps, created_by, created_at, updated_at, published_at)
SELECT workspace_id, id, 'published', version, trigger_kind, trigger_event, trigger_filter, entry, goal_event, steps, created_by,
    COALESCE(activated_at, updated_at), COALESCE(activated_at, updated_at), COALESCE(activated_at, updated_at)
FROM journeys WHERE status IN ('active', 'paused', 'archived');
