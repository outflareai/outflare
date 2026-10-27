-- Sequences: steps sent to enrolled leads from a pool of mailboxes, every
-- send through the policy check.

-- Minimum time between two sends from one mailbox, so a mailbox never
-- bursts. A random extra of up to half this is added per send.
ALTER TABLE mailboxes ADD COLUMN min_gap_seconds integer NOT NULL DEFAULT 120
    CHECK (min_gap_seconds BETWEEN 0 AND 3600);

CREATE TABLE sequences (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name            text NOT NULL,
    status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'archived')),
    -- Mailboxes to send from. Null means every mailbox in the workspace.
    pool_id         uuid REFERENCES mailbox_pools (id) ON DELETE SET NULL,
    timezone        text NOT NULL DEFAULT 'UTC',
    send_days       smallint[] NOT NULL DEFAULT '{1,2,3,4,5}', -- ISO weekdays, 1 = Monday
    send_start      time NOT NULL DEFAULT '09:00',
    send_end        time NOT NULL DEFAULT '17:00',
    -- Cap on leads starting the sequence per day; null is no cap.
    daily_new_limit integer CHECK (daily_new_limit > 0),
    stop_on_reply   boolean NOT NULL DEFAULT true,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (send_start < send_end)
);

CREATE INDEX sequences_ws_idx ON sequences (workspace_id);

CREATE TABLE sequence_steps (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    sequence_id uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    position    integer NOT NULL CHECK (position >= 0),
    -- Days to wait after the previous step. The first step's delay is
    -- counted from enrolment.
    delay_days  integer NOT NULL DEFAULT 0 CHECK (delay_days BETWEEN 0 AND 365),
    -- Empty subject on a later step means a reply in the same thread.
    subject     text NOT NULL DEFAULT '',
    body        text NOT NULL,
    UNIQUE (sequence_id, position)
);

CREATE TABLE enrollments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    sequence_id     uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    lead_id         uuid NOT NULL REFERENCES leads (id) ON DELETE CASCADE,
    -- Assigned at the first send and kept, so a lead hears from one sender.
    mailbox_id      uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN
                    ('active', 'completed', 'stopped', 'unsubscribed', 'bounced', 'replied', 'blocked', 'failed')),
    status_reason   text,
    next_step       integer NOT NULL DEFAULT 0,  -- position of the next step to send
    next_send_at    timestamptz,                 -- null while a send is queued or when finished
    thread_subject  text,                        -- subject of the first email, for replies
    last_message_id text,                        -- Message-ID of the last email, for threading
    references_ids  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (sequence_id, lead_id)
);

CREATE INDEX enrollments_due_idx ON enrollments (next_send_at) WHERE status = 'active' AND next_send_at IS NOT NULL;
CREATE INDEX enrollments_lead_idx ON enrollments (lead_id);

-- Every email Outflare tried to send, including ones the policy blocked.
CREATE TABLE messages (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enrollment_id uuid REFERENCES enrollments (id) ON DELETE SET NULL,
    sequence_id   uuid REFERENCES sequences (id) ON DELETE SET NULL,
    step_position integer,
    lead_id       uuid REFERENCES leads (id) ON DELETE SET NULL,
    mailbox_id    uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    from_email    text NOT NULL,
    to_email      text NOT NULL,
    subject       text NOT NULL,
    body          text NOT NULL,
    message_id    text NOT NULL,
    -- sending: handed to SMTP, outcome not yet recorded. unknown: the
    -- process stopped mid-send, so it is not retried (never send twice).
    status        text NOT NULL CHECK (status IN ('sending', 'sent', 'failed', 'blocked', 'unknown')),
    error         text,
    attempts      integer NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    sent_at       timestamptz
);

CREATE INDEX messages_mailbox_day_idx ON messages (mailbox_id, created_at) WHERE status IN ('sending', 'sent', 'unknown');
CREATE INDEX messages_ws_idx ON messages (workspace_id, created_at DESC);
CREATE INDEX messages_enrollment_idx ON messages (enrollment_id);
CREATE UNIQUE INDEX messages_step_idx ON messages (enrollment_id, step_position) WHERE status <> 'blocked';
