-- Link pitches that send without approval go out at most auto_send_per_day
-- a day per sequence (10 unless changed). Pitches past the cap wait as
-- drafts (reason daily_cap) and go out on a later day; pitches the AI was
-- unsure about, or to a site with a high spam score, wait for a person.
ALTER TABLE link_sequence_settings ADD COLUMN auto_send_per_day integer NOT NULL DEFAULT 10
    CHECK (auto_send_per_day BETWEEN 1 AND 200);
CREATE INDEX link_pitches_held_idx ON link_pitches (sequence_id, created_at) WHERE status = 'draft' AND reason = 'daily_cap';
