-- Outflare no longer sends warm-up mail itself: warm-up runs only in a
-- connected sequencer (EmailBison, Instantly, Smartlead). Turn off the
-- warm-up Outflare was running, and drop its queued sends and checks.
-- warmup_messages keeps its history; nothing new is written to it.
UPDATE mailboxes SET warmup_enabled = false, updated_at = now()
WHERE warmup_enabled AND warmup_integration_id IS NULL;

DELETE FROM jobs WHERE kind IN ('warmup.send', 'warmup.check') AND status = 'queued';
