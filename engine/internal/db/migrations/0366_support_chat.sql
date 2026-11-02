-- Live chat: a widget on the business's website opens a conversation in
-- the support inbox, and the team's replies show in the chat. The
-- visitor is known by a random token their browser keeps (only its hash
-- is stored), and by their customer record once they give an email or
-- the site vouches for who they are.

CREATE TABLE support_chat_widgets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand its conversations belong to; null for every brand.
    brand_id        uuid REFERENCES brands (id) ON DELETE CASCADE,
    -- The public key in the embed code: names the widget, grants nothing.
    key             text NOT NULL UNIQUE CHECK (length(key) BETWEEN 4 AND 100),
    enabled         boolean NOT NULL DEFAULT true,
    -- The sites that may show it (https://example.com); the browser refuses
    -- it anywhere else.
    allowed_origins text[] NOT NULL DEFAULT '{}',
    -- Look and words: colour, title, greeting, whether an email is needed
    -- before the first message.
    settings        jsonb NOT NULL DEFAULT '{}',
    -- Sealed secret the site signs a signed-in user's email with, so the
    -- chat knows it is really them. Null until made.
    identity_secret text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_chat_widgets_workspace ON support_chat_widgets (workspace_id);

ALTER TABLE support_conversations DROP CONSTRAINT support_conversations_channel_check;
ALTER TABLE support_conversations ADD CONSTRAINT support_conversations_channel_check CHECK (channel IN ('email', 'chat'));

-- A chat conversation: the widget it came from, and the hash of the
-- visitor's token. identity_verified says the site vouched for the email.
ALTER TABLE support_conversations
    ADD COLUMN chat_widget_id    uuid REFERENCES support_chat_widgets (id) ON DELETE SET NULL,
    ADD COLUMN chat_token_hash   bytea,
    ADD COLUMN identity_verified boolean NOT NULL DEFAULT false,
    ADD COLUMN visitor_seen_at   timestamptz;
CREATE INDEX support_conversations_chat_token ON support_conversations (chat_token_hash) WHERE chat_token_hash IS NOT NULL;
