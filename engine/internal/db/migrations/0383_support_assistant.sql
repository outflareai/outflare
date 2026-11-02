-- The support assistant: drafts answers to customers, by chat and email,
-- from approved knowledge only, or hands the conversation to a person.
-- Here it drafts for a person to send; sending on its own is a separate
-- mode an extension turns on.

-- How the assistant works, for the workspace (brand_id null) or one brand:
-- per channel, off or draft (auto is accepted only when an extension
-- enables it), the name it signs with, and where it looks for answers.
CREATE TABLE support_assistant_settings (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    chat_mode    text NOT NULL DEFAULT 'off' CHECK (chat_mode IN ('off', 'draft', 'auto')),
    email_mode   text NOT NULL DEFAULT 'off' CHECK (email_mode IN ('off', 'draft', 'auto')),
    name         text NOT NULL DEFAULT '' CHECK (length(name) <= 60),
    -- Where it looks when the knowledge has no answer, to suggest one for
    -- a person to approve: the brand's own website, and the Slack
    -- channels picked here (through a Slack knowledge connection).
    find_on_website      boolean NOT NULL DEFAULT true,
    slack_integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    slack_channels       jsonb NOT NULL DEFAULT '[]',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX support_assistant_settings_scope ON support_assistant_settings (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- Each time the assistant looked at a customer's message: what it decided,
-- the reply it drafted, what it cited, what its checks found, and what a
-- person then did with it. The report and the Auto decision are built
-- from these.
CREATE TABLE support_assistant_runs (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid REFERENCES brands (id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES support_conversations (id) ON DELETE CASCADE,
    -- The customer message it answered.
    message_id      uuid REFERENCES support_messages (id) ON DELETE SET NULL,
    channel         text NOT NULL CHECK (channel IN ('email', 'chat')),
    -- answer, clarify (one question back), handoff (to a person), or
    -- skipped (a check before the AI stopped it; nothing was asked).
    action          text NOT NULL CHECK (action IN ('answer', 'clarify', 'handoff', 'skipped')),
    -- Why it handed off or skipped: a short code.
    reason          text NOT NULL DEFAULT '' CHECK (length(reason) <= 60),
    reply           text NOT NULL DEFAULT '' CHECK (length(reply) <= 8000),
    cited           uuid[] NOT NULL DEFAULT '{}',
    -- What the checks found before a reply was offered.
    checks          jsonb NOT NULL DEFAULT '{}',
    -- drafted (waiting for a person), sent (as drafted), edited (sent after
    -- changes), discarded, or failed.
    outcome         text NOT NULL DEFAULT 'drafted' CHECK (outcome IN ('drafted', 'sent', 'edited', 'discarded', 'failed')),
    -- Why it ran: a new message, or a person asked.
    trigger         text NOT NULL DEFAULT 'message' CHECK (trigger IN ('message', 'person')),
    provider        text NOT NULL DEFAULT '',
    model           text NOT NULL DEFAULT '',
    duration_ms     integer NOT NULL DEFAULT 0,
    decided_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    decided_at      timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_assistant_runs_conversation ON support_assistant_runs (conversation_id, created_at DESC);
CREATE INDEX support_assistant_runs_report ON support_assistant_runs (workspace_id, created_at DESC);
