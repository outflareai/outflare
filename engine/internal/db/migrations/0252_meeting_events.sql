-- Meetings on people's timelines: each meeting tied to a person records
-- meeting.booked, then meeting.held, meeting.no_show or meeting.canceled,
-- as customer events (engine/internal/outcomes, MeetingEvents), so a booked
-- or held call can count as a conversion like any other event.
--
-- event_status is the last status recorded as an event: '' for none yet.
-- Meetings from before this migration start NULL and are recorded too,
-- with the times they happened.
ALTER TABLE meetings ADD COLUMN event_status text;
ALTER TABLE meetings ALTER COLUMN event_status SET DEFAULT '';

CREATE INDEX meetings_event_pending_idx ON meetings (id)
    WHERE record_id IS NOT NULL AND event_status IS DISTINCT FROM status;
