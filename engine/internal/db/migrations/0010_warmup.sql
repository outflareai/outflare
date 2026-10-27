-- Warm-up: the workspace's own mailboxes email each other at a slowly
-- rising volume, open the mail, rescue it from spam and reply, so
-- providers learn that mail from them is wanted. Where each message
-- landed is the inbox placement score.

ALTER TABLE mailboxes
    ADD COLUMN warmup_enabled      boolean NOT NULL DEFAULT false,
    -- When warm-up was last turned on; the daily volume ramps up from here.
    ADD COLUMN warmup_started_at   timestamptz,
    -- Warm-up emails a day once fully ramped up.
    ADD COLUMN warmup_daily_target integer NOT NULL DEFAULT 20 CHECK (warmup_daily_target BETWEEN 1 AND 100),
    -- Percent of received warm-up emails that are answered.
    ADD COLUMN warmup_reply_rate   integer NOT NULL DEFAULT 30 CHECK (warmup_reply_rate BETWEEN 0 AND 100),
    ADD COLUMN warmup_checked_at   timestamptz;

CREATE TABLE warmup_messages (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    from_mailbox_id uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    to_mailbox_id   uuid NOT NULL REFERENCES mailboxes (id) ON DELETE CASCADE,
    message_id      text NOT NULL,
    subject         text NOT NULL,
    -- The warm-up message this answers, and how deep in the thread it is.
    reply_to_id     uuid REFERENCES warmup_messages (id) ON DELETE SET NULL,
    depth           integer NOT NULL DEFAULT 0,
    status          text NOT NULL DEFAULT 'sending' CHECK (status IN ('sending', 'sent', 'failed')),
    error           text,
    -- Where the recipient found it: inbox, spam (and moved to the inbox),
    -- or missing when it never arrived.
    landed          text CHECK (landed IN ('inbox', 'spam', 'missing')),
    landed_at       timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX warmup_from_idx ON warmup_messages (from_mailbox_id, created_at DESC);
CREATE INDEX warmup_to_pending_idx ON warmup_messages (to_mailbox_id, created_at) WHERE landed IS NULL AND status = 'sent';
CREATE INDEX warmup_message_id_idx ON warmup_messages (workspace_id, message_id);
