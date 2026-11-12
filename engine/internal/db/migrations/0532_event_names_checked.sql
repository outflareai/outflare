-- New event names no longer wait for a person to approve them: a name a
-- brand's product sends is usable straight away, in segments, lists,
-- journeys and reports. Outflare checks each new name instead, with the
-- workspace's own AI model when it has one and a spelling comparison when
-- it has none, and asks only when a name looks like a typo or a second
-- spelling of one the brand already has ("Is sign_up a typo of
-- signed_up?").
--
-- name_check: pending (not checked yet), ok (nothing to ask), asked (a
-- person is asked whether it is looks_like). check_reason is what the
-- check said, for the question. same_as is the name a person said it is:
-- its events were moved there and new ones are stored under it.
--
-- Names already in the catalog count as checked; a name added from now on
-- starts pending.
ALTER TABLE event_definitions
    ADD COLUMN name_check   text NOT NULL DEFAULT 'ok' CHECK (name_check IN ('pending', 'ok', 'asked')),
    ADD COLUMN looks_like   text,
    ADD COLUMN check_reason text NOT NULL DEFAULT '',
    ADD COLUMN same_as      text,
    ADD COLUMN checked_at   timestamptz;
ALTER TABLE event_definitions ALTER COLUMN name_check SET DEFAULT 'pending';

-- The names ingest held for approval are approved, and the events held for
-- them go back to be processed: the inbox's sweep queues the work within a
-- minute.
UPDATE event_definitions SET status = 'approved', updated_at = now() WHERE status = 'quarantined';
UPDATE event_inbox SET status = 'pending', code = NULL, reason = NULL, attempts = 0, processed_at = NULL, purge_after = NULL
WHERE status = 'held' AND code = 'name_not_approved';
