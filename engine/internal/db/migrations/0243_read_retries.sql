-- Failed daily reads are tried again the same day (engine/internal/retry):
-- review pages, social profiles and rank checks keep a reading per day
-- that can't be filled in later. failures counts the reads in a row that
-- failed and sets how long the next try waits (15 minutes, then 1, 2 and
-- every 4 hours); last_error_code says why in a word; last_failed_at when.

-- render_js remembers that a review page only showed its reviews once
-- the page reader ran it in a browser, so later reads start that way.
ALTER TABLE review_pages
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text,
    ADD COLUMN last_failed_at  timestamptz,
    ADD COLUMN render_js       boolean NOT NULL DEFAULT false;

ALTER TABLE social_accounts
    ADD COLUMN failures        int NOT NULL DEFAULT 0,
    ADD COLUMN last_error_code text,
    ADD COLUMN last_failed_at  timestamptz;

ALTER TABLE tracked_keywords
    ADD COLUMN failures       int NOT NULL DEFAULT 0,
    ADD COLUMN last_failed_at timestamptz;
