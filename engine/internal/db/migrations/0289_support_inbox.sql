-- The support inbox: conversations with customers who write in, answered
-- by the team from a shared inbox. A connected mailbox marked for support
-- feeds it instead of the sales inbox and never sends cold email; a
-- forwarding address can feed it too. Replies go out through the mailbox
-- the conversation came in on.

-- outreach: sequences and the sales inbox, as before. support: its mail
-- goes to the support inbox and sequences never send from it.
ALTER TABLE mailboxes ADD COLUMN purpose text NOT NULL DEFAULT 'outreach'
    CHECK (purpose IN ('outreach', 'support'));

CREATE TABLE support_conversations (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- The customer. Null only until their record is made, in the same
    -- transaction as the first message.
    record_id        uuid REFERENCES records (id) ON DELETE SET NULL,
    -- Where it came in and where replies go out. Null when that mailbox
    -- was removed: the conversation stays, but can't be answered by email.
    mailbox_id       uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    channel          text NOT NULL DEFAULT 'email' CHECK (channel IN ('email')),
    subject          text NOT NULL DEFAULT '' CHECK (length(subject) <= 1000),
    -- open: waits on the team. pending: waits on the customer, and opens
    -- again when they write. closed: done, and opens again when they write.
    status           text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'pending', 'closed')),
    assignee_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    team_id          uuid REFERENCES teams (id) ON DELETE SET NULL,
    tags             text[] NOT NULL DEFAULT '{}',
    -- The customer's email address as they wrote from it, for replies.
    contact_email    text NOT NULL DEFAULT '',
    -- Times the team is measured on: when the customer first wrote, when
    -- the team first answered, and when it was last closed.
    first_message_at timestamptz NOT NULL DEFAULT now(),
    first_reply_at   timestamptz,
    closed_at        timestamptz,
    last_message_at  timestamptz NOT NULL DEFAULT now(),
    -- Who wrote last: customer or team, for "waiting on" views.
    last_from        text NOT NULL DEFAULT 'customer' CHECK (last_from IN ('customer', 'team')),
    unread           boolean NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX support_conversations_inbox ON support_conversations (workspace_id, status, last_message_at DESC);
CREATE INDEX support_conversations_assignee ON support_conversations (assignee_id) WHERE assignee_id IS NOT NULL;
CREATE INDEX support_conversations_record ON support_conversations (record_id) WHERE record_id IS NOT NULL;

-- What was said: the customer's emails, the team's replies, and notes the
-- customer never sees.
CREATE TABLE support_messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    conversation_id uuid NOT NULL REFERENCES support_conversations (id) ON DELETE CASCADE,
    -- in: from the customer. out: a reply to them. note: for the team only.
    kind            text NOT NULL CHECK (kind IN ('in', 'out', 'note')),
    -- The customer, on messages they sent; the conversation's otherwise.
    record_id       uuid REFERENCES records (id) ON DELETE SET NULL,
    author_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    from_email      text NOT NULL DEFAULT '',
    from_name       text NOT NULL DEFAULT '',
    to_email        text NOT NULL DEFAULT '',
    subject         text NOT NULL DEFAULT '',
    body            text NOT NULL DEFAULT '' CHECK (length(body) <= 200000),
    -- Email threading: this message's Message-ID, and what it answered.
    message_id      text NOT NULL DEFAULT '',
    in_reply_to     text NOT NULL DEFAULT '',
    references_ids  text NOT NULL DEFAULT '',
    -- An out-of-office or other automatic answer: shown, but it doesn't
    -- open a closed conversation.
    auto_reply      boolean NOT NULL DEFAULT false,
    -- Replies: sending, then sent or failed (error says why).
    status          text NOT NULL DEFAULT 'received' CHECK (status IN ('received', 'sending', 'sent', 'failed', 'saved')),
    error           text,
    -- Where an email came from, so a sync never stores it twice.
    mailbox_id      uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    uidvalidity     bigint,
    uid             bigint,
    created_at      timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz
);
CREATE UNIQUE INDEX support_messages_uid ON support_messages (mailbox_id, uidvalidity, uid) WHERE uid IS NOT NULL;
CREATE INDEX support_messages_conversation ON support_messages (conversation_id, created_at);
CREATE INDEX support_messages_thread ON support_messages (workspace_id, message_id) WHERE message_id <> '';
CREATE INDEX support_messages_record ON support_messages (record_id) WHERE record_id IS NOT NULL;
