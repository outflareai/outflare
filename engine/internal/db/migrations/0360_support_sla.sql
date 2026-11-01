-- Support SLAs: how fast the team promises to answer and to resolve, for
-- the whole workspace or one brand, counted in business hours or around
-- the clock. Each conversation carries when its next reply and its
-- resolution are due, and when they were missed.

CREATE TABLE support_sla_policies (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Null: every conversation without a brand policy of its own.
    brand_id            uuid REFERENCES brands (id) ON DELETE CASCADE,
    enabled             boolean NOT NULL DEFAULT true,
    -- Targets in minutes; null means no target. First reply counts from
    -- the customer's first message, next reply from each later message
    -- the team hasn't answered, resolution from when it (re)opened.
    first_reply_minutes integer CHECK (first_reply_minutes BETWEEN 1 AND 525600),
    next_reply_minutes  integer CHECK (next_reply_minutes BETWEEN 1 AND 525600),
    resolution_minutes  integer CHECK (resolution_minutes BETWEEN 1 AND 525600),
    -- The team's working week, in its time zone: per weekday (0 Sunday to
    -- 6 Saturday) a list of [start, end] minutes after midnight. Null
    -- counts around the clock.
    timezone            text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) <= 64),
    hours               jsonb,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX support_sla_policies_scope ON support_sla_policies (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid));

-- When the conversation last opened (for resolution), when the team's
-- next reply and the resolution are due, and when each was missed.
-- sla_breaches counts every miss, for reports.
ALTER TABLE support_conversations
    ADD COLUMN opened_at           timestamptz,
    ADD COLUMN reply_due_at        timestamptz,
    ADD COLUMN reply_breached_at   timestamptz,
    ADD COLUMN resolve_due_at      timestamptz,
    ADD COLUMN resolve_breached_at timestamptz,
    ADD COLUMN sla_breaches        integer NOT NULL DEFAULT 0;
CREATE INDEX support_conversations_reply_due ON support_conversations (reply_due_at) WHERE reply_due_at IS NOT NULL AND reply_breached_at IS NULL;
CREATE INDEX support_conversations_resolve_due ON support_conversations (resolve_due_at) WHERE resolve_due_at IS NOT NULL AND resolve_breached_at IS NULL;
