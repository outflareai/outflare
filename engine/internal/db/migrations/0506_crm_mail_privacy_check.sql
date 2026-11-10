-- Team email sharing (crm_mail_policy.team_sharing) only shows a colleague
-- an email the workspace's own AI model checked and found to be about work:
-- one that looks private (pay, a job offer, HR, a legal dispute, health or
-- family) stays its owner's. Every email starts unchecked, which is
-- private to its owner too. Only expands the schema: the old version
-- never reads these columns, and its inserts take the default.
ALTER TABLE crm_mail_messages
    ADD COLUMN privacy text NOT NULL DEFAULT 'unchecked' CHECK (privacy IN ('unchecked', 'work', 'private')),
    ADD COLUMN privacy_checked_at timestamptz;
CREATE INDEX crm_mail_messages_unchecked_idx ON crm_mail_messages (workspace_id, brand_id, created_at) WHERE privacy = 'unchecked';
