-- Experts approve their own quotes: a person (or an agent) asks the
-- expert, by an email from one of the brand's outreach sequences (sent by
-- the sequence engine from the mailboxes allocated to it) or by a link to
-- paste anywhere, and the expert approves the exact words, suggests
-- other words or declines on a public page. A request is bound to the
-- words it asked about and to the expert, works once and expires after
-- 14 days; changing the words, withdrawing the quote or removing the
-- expert voids it.
CREATE TABLE story_quote_approvals (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    story_id       uuid NOT NULL REFERENCES stories (id) ON DELETE CASCADE,
    quote_id       uuid NOT NULL REFERENCES story_quotes (id) ON DELETE CASCADE,
    expert_id      uuid NOT NULL REFERENCES pr_experts (id) ON DELETE CASCADE,
    -- The exact words the expert is asked to approve.
    body           text NOT NULL CHECK (length(body) BETWEEN 1 AND 2000),
    -- email: the expert is enrolled in an outreach sequence whose step
    -- carries the link. link: the person copies the link and sends it.
    delivery       text NOT NULL CHECK (delivery IN ('email', 'link')),
    -- SHA-256 of the link's secret; this table never keeps the secret.
    token_hash     bytea UNIQUE,
    -- sending: the email waits in the sequence. open: waiting for the expert.
    -- failed: the email couldn't be sent. approved, changes_suggested,
    -- declined: the expert decided. void: the words, the quote or the
    -- expert changed, or the request was replaced by a new one.
    status         text NOT NULL DEFAULT 'open'
                   CHECK (status IN ('sending', 'open', 'failed', 'approved', 'changes_suggested', 'declined', 'void')),
    -- Why it is void or failed, as a code (words_changed, withdrawn,
    -- approved_by_person, replaced; the sequence's blocked, failed or
    -- bounced reason, such as suppressed_email).
    reason         text NOT NULL DEFAULT '' CHECK (length(reason) <= 100),
    -- An emailed request: the sequence, the expert's lead and enrollment,
    -- and the mailbox and message that sent it.
    sequence_id    uuid REFERENCES sequences (id) ON DELETE SET NULL,
    lead_id        uuid REFERENCES leads (id) ON DELETE SET NULL,
    enrollment_id  uuid REFERENCES enrollments (id) ON DELETE SET NULL,
    mailbox_id     uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    message_id     uuid REFERENCES messages (id) ON DELETE SET NULL,
    -- The words the expert suggested instead, and what they wrote with a
    -- suggestion or a decline.
    suggested_body text NOT NULL DEFAULT '' CHECK (length(suggested_body) <= 2000),
    expert_note    text NOT NULL DEFAULT '' CHECK (length(expert_note) <= 1000),
    expires_at     timestamptz NOT NULL,
    sent_at        timestamptz,
    opened_at      timestamptz,
    decided_at     timestamptz,
    requested_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX story_quote_approvals_quote_idx ON story_quote_approvals (quote_id, created_at DESC);
CREATE INDEX story_quote_approvals_open_idx ON story_quote_approvals (workspace_id, status) WHERE status IN ('sending', 'open');
CREATE INDEX story_quote_approvals_enrollment_idx ON story_quote_approvals (enrollment_id) WHERE enrollment_id IS NOT NULL;

-- How a quote's approval was recorded: by a person in Outflare, or by
-- the expert through an approval link. And when the expert last
-- suggested the quote's words, until a person edits them or asks again.
ALTER TABLE story_quotes ADD COLUMN approved_via text NOT NULL DEFAULT '' CHECK (approved_via IN ('', 'person', 'expert_link'));
ALTER TABLE story_quotes ADD COLUMN suggested_at timestamptz;
