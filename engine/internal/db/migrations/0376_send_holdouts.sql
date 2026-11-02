-- Holdouts for broadcasts and sequences: a share of the audience, picked
-- at random and the same way every time for a person, gets nothing, so
-- comparing what they did afterwards with what the people who got it did
-- says what the email added (GET .../lift). 0 is off, at most 50.
ALTER TABLE email_broadcasts
    ADD COLUMN holdout_percent smallint NOT NULL DEFAULT 0 CHECK (holdout_percent BETWEEN 0 AND 50),
    -- How many of the audience were held out; they are recipients rows
    -- with skipped = 'holdout' and are not counted in skipped.
    ADD COLUMN held_out integer NOT NULL DEFAULT 0;

-- A sequence's held-out enrollments stop before their first email, with
-- status stopped and status_reason holdout.
ALTER TABLE sequences
    ADD COLUMN holdout_percent smallint NOT NULL DEFAULT 0 CHECK (holdout_percent BETWEEN 0 AND 50);
