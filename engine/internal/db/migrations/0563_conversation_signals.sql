-- Conversation signals: a brand can let its own AI read conversations
-- with customers (support, company Telegram chats, email the team shares,
-- and call transcripts its own recorder sends) and raise a signal when a
-- customer says something sales should act on. Off until the brand
-- switches it on, because every read is a call on the workspace's own AI.
CREATE TABLE conversation_signal_settings (
    brand_id     uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT false,
    -- Which kinds of conversation are read.
    channels     text[] NOT NULL DEFAULT '{support,telegram,email,call}',
    updated_by   uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversation_signal_settings_workspace ON conversation_signal_settings (workspace_id);

-- How far each conversation has been read, so it is read again only when
-- the customer has said something new. ref is the conversation's id in
-- its channel: a support conversation, a Telegram chat, a transcript, or
-- for email the person or company the mail is with.
CREATE TABLE conversation_reads (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    channel      text NOT NULL CHECK (channel IN ('support', 'telegram', 'email', 'call')),
    ref          text NOT NULL CHECK (length(ref) BETWEEN 1 AND 200),
    read_through timestamptz NOT NULL,
    read_at      timestamptz NOT NULL DEFAULT now(),
    reads        integer NOT NULL DEFAULT 1,
    -- Signals raised from it so far.
    raised       integer NOT NULL DEFAULT 0,
    PRIMARY KEY (brand_id, channel, ref)
);
CREATE INDEX conversation_reads_workspace ON conversation_reads (workspace_id, read_at DESC);

-- A transcript sent in for reading: a call or meeting from the workspace's
-- own recorder. The text is sealed, and removed once it has been read; the
-- row stays to say that it happened.
CREATE TABLE conversation_transcripts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    record_id      uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    title          text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    content_sealed bytea,
    occurred_at    timestamptz NOT NULL,
    read_at        timestamptz,
    created_by     uuid REFERENCES users(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversation_transcripts_record ON conversation_transcripts (record_id, occurred_at DESC);
CREATE INDEX conversation_transcripts_brand ON conversation_transcripts (workspace_id, brand_id, created_at DESC);
