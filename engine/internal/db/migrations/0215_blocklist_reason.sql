-- An entry read from a sequencer's blocklist (EmailBison, Instantly,
-- Smartlead) says only "don't cold email them": it is not an unsubscribe.
-- The sync filed blocked addresses as unsubscribed and domains as
-- do_not_contact, which also withdrew those people's consent to marketing
-- email. They get their own reason, which stops cold email only.
ALTER TABLE suppressions DROP CONSTRAINT IF EXISTS suppressions_reason_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_reason_check CHECK (reason IN (
    'unsubscribed', 'bounced', 'complained', 'manual', 'do_not_contact',
    'competitor', 'customer', 'partner', 'legal', 'blocklist'));

-- Only the blocklist sync writes these: a connection, source
-- <connector>_sync, added automatically.
UPDATE suppressions SET reason = 'blocklist'
WHERE integration_id IS NOT NULL AND method = 'automated'
    AND source IN ('emailbison_sync', 'instantly_sync', 'smartlead_sync')
    AND reason IN ('unsubscribed', 'do_not_contact');

-- migration-safety: these withdrawals were written by the blocklist sync
-- (source <connector>_sync, via unsubscribe), never by the person, so
-- removing them gives people back the consent the sync took; no version
-- reads them as anything but the latest decision.
DELETE FROM consents
WHERE source IN ('emailbison_sync', 'instantly_sync', 'smartlead_sync')
    AND status = 'withdrawn' AND channel = 'email' AND purpose = 'marketing'
    AND evidence ->> 'via' = 'unsubscribe';
