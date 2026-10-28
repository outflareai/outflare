-- Person reveal: people a visitor identification tool (RB2B, Vector,
-- Warmly) names from the workspace's own account, pushed to a webhook.
-- They become person records like everyone else, tagged with the tool
-- (records.acquired_via) and held for review: hidden from the people list
-- and never emailed until someone approves them.

-- A record someone has to look at before it is used: hidden from lists
-- and blocked by the policy check until approved. Any feature that adds
-- people it is unsure of may set it.
ALTER TABLE records ADD COLUMN needs_review boolean NOT NULL DEFAULT false;

CREATE INDEX records_needs_review_idx ON records (workspace_id, created_at DESC) WHERE needs_review;

ALTER TABLE visitor_settings
    -- Whether people pushed by a connected tool are taken in. Off until
    -- the workspace turns it on and accepts the notice.
    ADD COLUMN reveal_people boolean NOT NULL DEFAULT false,
    -- When and by whom the notice was accepted: the tools name people in
    -- the United States only, and the workspace is responsible for telling
    -- its visitors (its privacy policy) and honouring opt-outs.
    ADD COLUMN people_notice_accepted_at timestamptz,
    ADD COLUMN people_notice_accepted_by uuid REFERENCES actors (id) ON DELETE SET NULL;
