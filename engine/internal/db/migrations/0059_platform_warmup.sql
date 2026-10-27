-- Warm-up can run on a connected sequencer instead of in Outflare. When
-- set, Outflare's own warm-up leaves the mailbox alone and keeps the
-- platform's warm-up switched to match warmup_enabled.
ALTER TABLE mailboxes ADD COLUMN warmup_integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL;
