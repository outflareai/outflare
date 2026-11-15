-- Journeys about companies: a journey is about one kind of record, a
-- person (as every journey so far) or an account. A journey about an
-- account starts on events on the company and on events its people do,
-- and its steps act on the company's record.
ALTER TABLE journeys ADD COLUMN record_type text NOT NULL DEFAULT 'person';
