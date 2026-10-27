-- Inbox: mail received by connected mailboxes, matched to the leads and
-- sequences it answers.

ALTER TABLE mailboxes
    ADD COLUMN imap_uidvalidity bigint,
    ADD COLUMN imap_last_uid    bigint NOT NULL DEFAULT 0,
    ADD COLUMN last_synced_at   timestamptz,
    ADD COLUMN sync_error       text;

CREATE TABLE inbound_messages (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    mailbox_id    uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    uidvalidity   bigint NOT NULL,
    uid           bigint NOT NULL,
    message_id    text NOT NULL DEFAULT '',
    in_reply_to   text NOT NULL DEFAULT '',
    references_ids text NOT NULL DEFAULT '',
    from_email    text NOT NULL,
    from_name     text NOT NULL DEFAULT '',
    to_email      text NOT NULL DEFAULT '',
    subject       text NOT NULL DEFAULT '',
    body          text NOT NULL DEFAULT '',
    -- reply: a person answered. auto_reply: out of office and the like.
    -- bounce: a delivery failure report. other: not about a sequence.
    kind          text NOT NULL CHECK (kind IN ('reply', 'auto_reply', 'bounce', 'other')),
    -- The reply asked not to be contacted again.
    opt_out       boolean NOT NULL DEFAULT false,
    lead_id       uuid REFERENCES leads (id) ON DELETE SET NULL,
    enrollment_id uuid REFERENCES enrollments (id) ON DELETE SET NULL,
    sequence_id   uuid REFERENCES sequences (id) ON DELETE SET NULL,
    -- The sent message this answers, when it could be matched.
    reply_to_message_id uuid REFERENCES messages (id) ON DELETE SET NULL,
    read          boolean NOT NULL DEFAULT false,
    received_at   timestamptz NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (mailbox_id, uidvalidity, uid)
);

CREATE INDEX inbound_ws_idx ON inbound_messages (workspace_id, received_at DESC);
CREATE INDEX inbound_lead_idx ON inbound_messages (lead_id);

-- Manual replies sent from the inbox point at what they answer.
ALTER TABLE messages ADD COLUMN reply_to_inbound_id uuid REFERENCES inbound_messages (id) ON DELETE SET NULL;

CREATE INDEX messages_message_id_idx ON messages (workspace_id, message_id);
CREATE INDEX messages_lead_idx ON messages (lead_id);
