-- Working a reply as a team (docs/support-inbox.md, "Working together"):
-- a reply can sit with a team, carry private notes, and have teammates
-- looped in who then see it under Watching. Support conversations already
-- have a team and notes, and get the same loop-in.
ALTER TABLE inbound_messages ADD COLUMN team_id uuid REFERENCES teams (id) ON DELETE SET NULL;
CREATE INDEX inbound_messages_team ON inbound_messages (team_id) WHERE team_id IS NOT NULL;

-- A note on a reply, for the team only. It is never sent.
CREATE TABLE inbox_notes (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    inbound_id   uuid NOT NULL REFERENCES inbound_messages (id) ON DELETE CASCADE,
    -- Null when an API token or agent wrote it, or its author has left.
    author_id    uuid REFERENCES users (id) ON DELETE SET NULL,
    body         text NOT NULL CHECK (length(body) BETWEEN 1 AND 20000),
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX inbox_notes_inbound ON inbox_notes (inbound_id, created_at);
CREATE INDEX inbox_notes_workspace ON inbox_notes (workspace_id);
CREATE INDEX inbox_notes_brand ON inbox_notes (brand_id);

-- A teammate looped in on a reply: it shows under their Watching view.
CREATE TABLE inbox_followers (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    inbound_id   uuid NOT NULL REFERENCES inbound_messages (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    -- Who looped them in; null for an API token or agent.
    added_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- When they first opened it after being looped in; null while it is
    -- still new to them. Set at once for someone who looped themselves in.
    seen_at      timestamptz,
    PRIMARY KEY (inbound_id, user_id)
);
CREATE INDEX inbox_followers_user ON inbox_followers (user_id);
CREATE INDEX inbox_followers_workspace ON inbox_followers (workspace_id);
CREATE INDEX inbox_followers_brand ON inbox_followers (brand_id);

-- The same for a support conversation.
CREATE TABLE support_followers (
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES support_conversations (id) ON DELETE CASCADE,
    user_id         uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    added_by        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    seen_at         timestamptz,
    PRIMARY KEY (conversation_id, user_id)
);
CREATE INDEX support_followers_user ON support_followers (user_id);
CREATE INDEX support_followers_workspace ON support_followers (workspace_id);
CREATE INDEX support_followers_brand ON support_followers (brand_id);

-- Who has a reply or support conversation open right now, and whether they
-- are writing in it, so two teammates don't answer the same customer. The
-- page says so again every few seconds; a row older than that counts for
-- nothing and is swept away.
CREATE TABLE inbox_viewing (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('reply', 'support')),
    -- The inbound message or the support conversation, by kind.
    item_id      uuid NOT NULL,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    seen_at      timestamptz NOT NULL DEFAULT now(),
    typing_at    timestamptz,
    PRIMARY KEY (item_id, user_id)
);
CREATE INDEX inbox_viewing_workspace ON inbox_viewing (workspace_id, seen_at);
CREATE INDEX inbox_viewing_brand ON inbox_viewing (brand_id);
CREATE INDEX inbox_viewing_user ON inbox_viewing (user_id);
