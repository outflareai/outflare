-- Leads partners submit on the portal: a customer's email they say they
-- referred. With the program's portal setting lead_submissions on review,
-- each waits for the team; on auto, a lead nobody else has is taken at
-- once. Taking one binds the referral (via manual) like any conversion.
-- The person is a record; their email lives there, never here.
CREATE TABLE partner_lead_submissions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE CASCADE,
    program_id    uuid NOT NULL REFERENCES partner_programs (id) ON DELETE CASCADE,
    membership_id uuid NOT NULL REFERENCES partner_memberships (id) ON DELETE CASCADE,
    record_id     uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    note          text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    status        text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'approved', 'rejected')),
    -- Why it was rejected: the team's words, or why it couldn't be taken
    -- (already_referred, self_referral and the like).
    reason        text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    referral_id   uuid REFERENCES partner_referrals (id) ON DELETE SET NULL,
    decided_at    timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- One open or taken submission per person and program.
CREATE UNIQUE INDEX partner_lead_submissions_once ON partner_lead_submissions (program_id, record_id) WHERE status <> 'rejected';
CREATE INDEX partner_lead_submissions_recent ON partner_lead_submissions (workspace_id, created_at DESC);
CREATE INDEX partner_lead_submissions_partner ON partner_lead_submissions (membership_id, created_at DESC);
