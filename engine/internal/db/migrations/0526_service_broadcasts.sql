-- Service broadcasts: one transactional email to many people at once, such
-- as a notice about their account or a sign-up that failed. A service
-- broadcast sends a transactional template and skips what people chose
-- about marketing email (topics, consent, guardrails); suppressions still
-- stop it at send time. Broadcasts to a saved list or segment name it in
-- audience_ref (a smart list id), which 0094 kept for this.
ALTER TABLE email_broadcasts ADD COLUMN service boolean NOT NULL DEFAULT false;
