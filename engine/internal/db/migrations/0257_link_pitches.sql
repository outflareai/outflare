-- Link building P3: pitching a link prospect through an Outreach sequence.
-- A pitch is one contact at the prospect's website, the sequence that
-- writes to them and the opener the workspace's AI wrote for them (the
-- step's {{custom.opener}}). It waits for a person's approval unless the
-- sequence's link pitches skip it; approving enrolls the contact's lead.
-- Replies move the prospect along, and a daily check of agreed pages marks
-- the link live.

CREATE TABLE link_pitches (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    prospect_id  uuid NOT NULL REFERENCES link_prospects (id) ON DELETE CASCADE,
    sequence_id  uuid NOT NULL REFERENCES sequences (id) ON DELETE CASCADE,
    -- Who it is to: the person record, and their lead once one is made.
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    lead_id      uuid REFERENCES leads (id) ON DELETE SET NULL,
    -- writing (the AI opener is being written), draft (waits for approval),
    -- enrolled, rejected, or failed (the opener couldn't be written).
    status       text NOT NULL DEFAULT 'writing' CHECK (status IN ('writing', 'draft', 'enrolled', 'rejected', 'failed')),
    opener       text NOT NULL DEFAULT '' CHECK (length(opener) <= 2000),
    -- Why it failed or was rejected.
    reason       text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    -- What the AI read the latest reply as: agreed, declined,
    -- wants_payment, wants_content or other.
    reply_kind   text CHECK (reply_kind IN ('agreed', 'declined', 'wants_payment', 'wants_content', 'other')),
    replied_at   timestamptz,
    approved_by  uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at  timestamptz,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX link_pitches_ws_idx ON link_pitches (workspace_id, status, created_at DESC);
CREATE INDEX link_pitches_prospect_idx ON link_pitches (prospect_id);
CREATE INDEX link_pitches_lead_idx ON link_pitches (workspace_id, lead_id) WHERE lead_id IS NOT NULL;
CREATE INDEX link_pitches_record_idx ON link_pitches (workspace_id, record_id);

-- Whether a sequence's link pitches send without a person approving each
-- one. No row: they wait for approval (the default).
CREATE TABLE link_sequence_settings (
    sequence_id     uuid PRIMARY KEY REFERENCES sequences (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    approve_pitches boolean NOT NULL DEFAULT true,
    updated_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at      timestamptz NOT NULL DEFAULT now()
);

-- An agreed link not live after the overdue wait is announced once.
ALTER TABLE link_prospects ADD COLUMN overdue_at timestamptz;
CREATE INDEX link_prospects_live_check_idx ON link_prospects (stage, stage_changed_at) WHERE stage IN ('agreed', 'live');
