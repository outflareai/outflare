-- What Outflare remembers about a business, so people never have to tell
-- each agent, chat or teammate again. Company context is what is true for
-- every project; offer, audience, market and ICP stay on projects.
CREATE TABLE workspace_context (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    company      text NOT NULL DEFAULT '',
    website      text NOT NULL DEFAULT '',
    -- What the company does, in a few sentences.
    about        text NOT NULL DEFAULT '',
    -- What the person wants from Outflare, such as 20 demos a month.
    goals        text NOT NULL DEFAULT '',
    -- How emails should sound: tone, length, sign-off.
    voice        text NOT NULL DEFAULT '',
    -- Things never to do.
    rules        text NOT NULL DEFAULT '',
    -- Everything else, in Markdown: proof points, competitors, past results.
    notes        text NOT NULL DEFAULT '',
    -- Bumped on every real change, so agents can tell theirs is stale and an
    -- edit made against an old version fails instead of overwriting.
    version      integer NOT NULL DEFAULT 1,
    -- Set when a person says not to ask for the missing fields again.
    onboarding_dismissed_at timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL
);

-- Memories: short lasting facts an agent or a person saved, such as a
-- preference or a decision. Never transcripts. Agents read them as data;
-- they never change limits or policy.
CREATE TABLE memories (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null: true for the whole workspace.
    project_id   uuid REFERENCES projects (id) ON DELETE CASCADE,
    kind         text NOT NULL DEFAULT 'fact' CHECK (kind IN ('preference', 'decision', 'fact', 'feedback')),
    text         text NOT NULL CHECK (length(text) BETWEEN 1 AND 500),
    -- Pinned memories come back with every context read.
    pinned       boolean NOT NULL DEFAULT false,
    -- Who saved it, and the mission it was saved in, if any.
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    mission_id   uuid REFERENCES missions (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX memories_workspace_idx ON memories (workspace_id, pinned DESC, updated_at DESC);
