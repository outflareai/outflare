-- The support assistant's next steps: Auto email replies held for a few
-- minutes so the team can stop them, hand-off topics that route a
-- conversation to a person, learning answers from past replies, and the
-- website kept current by a daily re-check.

-- How long an Auto email reply waits before it goes, so a teammate can stop
-- or edit it. 0 sends at once. Chat always answers at once.
ALTER TABLE support_assistant_settings
    ADD COLUMN email_hold_minutes integer NOT NULL DEFAULT 5 CHECK (email_hold_minutes BETWEEN 0 AND 60);

-- When a held Auto reply will be sent; null when it isn't waiting to go.
-- Stopping it clears this and leaves it as a draft.
ALTER TABLE support_assistant_runs ADD COLUMN send_at timestamptz;
CREATE INDEX support_assistant_runs_held ON support_assistant_runs (send_at) WHERE send_at IS NOT NULL;

-- Hand-off topics: what a brand's conversations are about, for the
-- assistant to tag them, and who takes each when it hands one over. A topic
-- with always_handoff is never answered by the assistant.
CREATE TABLE support_handoff_topics (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid REFERENCES brands (id) ON DELETE CASCADE,
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 60),
    description    text NOT NULL DEFAULT '' CHECK (length(description) <= 300),
    -- Who takes it: a person, a team, or both (the conversation is
    -- assigned to the person, in the team's queue).
    assignee_id    uuid REFERENCES users (id) ON DELETE SET NULL,
    team_id        uuid REFERENCES teams (id) ON DELETE SET NULL,
    always_handoff boolean NOT NULL DEFAULT false,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX support_handoff_topics_name ON support_handoff_topics (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));

-- The topic the assistant gave a conversation.
ALTER TABLE support_conversations ADD COLUMN topic_id uuid REFERENCES support_handoff_topics (id) ON DELETE SET NULL;
CREATE INDEX support_conversations_topic ON support_conversations (topic_id) WHERE topic_id IS NOT NULL;

-- Learning from past replies: one run reads the closed conversations of the
-- last days, and files the general answers in them for review.
CREATE TABLE knowledge_reply_imports (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id           uuid REFERENCES brands (id) ON DELETE CASCADE,
    days               integer NOT NULL DEFAULT 90 CHECK (days BETWEEN 1 AND 365),
    status             text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    conversations_read integer NOT NULL DEFAULT 0,
    suggestions_filed  integer NOT NULL DEFAULT 0,
    error              text NOT NULL DEFAULT '' CHECK (length(error) <= 500),
    created_by         uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    finished_at        timestamptz
);
CREATE INDEX knowledge_reply_imports_workspace ON knowledge_reply_imports (workspace_id, created_at DESC);

-- Website imports: whether the daily re-check started it, and how many
-- articles it unpublished because their page left the site.
ALTER TABLE knowledge_website_imports ADD COLUMN scheduled boolean NOT NULL DEFAULT false;
ALTER TABLE knowledge_website_imports ADD COLUMN pages_removed integer NOT NULL DEFAULT 0;

-- An article made from a website page that is no longer on the site: when
-- the re-check stopped finding it. It is unpublished, and published again
-- if the page comes back.
ALTER TABLE knowledge_items ADD COLUMN source_missing_at timestamptz;
