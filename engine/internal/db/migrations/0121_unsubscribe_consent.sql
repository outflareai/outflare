-- An unsubscribe from any email (a cold sequence's link, a reply asking to
-- stop, an import from another sequencer) is also a withdrawn consent to all
-- marketing email on the person's record, so every module sees one answer.
-- The suppression stays as the hard block. The person opting in again
-- themselves (signing up and agreeing to email, the preference page) clears
-- it; an import or a CRM sync never does.
--
-- record_id is the person an email suppression belongs to. The
-- customers.link job fills it in for older unsubscribes and records their
-- withdrawn consent, dated when they unsubscribed.

ALTER TABLE suppressions ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;
CREATE INDEX suppressions_record_idx ON suppressions (record_id) WHERE record_id IS NOT NULL;
CREATE INDEX suppressions_unsubscribed_unlinked_idx ON suppressions (workspace_id, id)
    WHERE record_id IS NULL AND kind = 'email' AND reason = 'unsubscribed';

INSERT INTO jobs (workspace_id, kind, payload, max_attempts, idempotency_key)
SELECT DISTINCT workspace_id, 'customers.link', '{}'::jsonb, 10, 'customers.link:0121:' || workspace_id
FROM suppressions WHERE kind = 'email' AND reason = 'unsubscribed';
