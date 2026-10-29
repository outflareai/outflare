-- A review page read in a browser runs as a job at the page reader
-- (Geonode's async extract): sent once, then polled. When a round stops
-- waiting before the job is done, the job's id, the address it reads and
-- when it started are kept on the page, so the next round picks the same
-- job up instead of sending the page again. All three are cleared once the
-- job ends.
--
-- backfill_tries counts first reads that stopped partway (a later page of
-- the history failed): the history is read again soon, and after a few
-- tries what was read is kept as the page's history.
ALTER TABLE review_pages
    ADD COLUMN pending_job_id  text,
    ADD COLUMN pending_job_url text,
    ADD COLUMN pending_job_at  timestamptz,
    ADD COLUMN backfill_tries  int NOT NULL DEFAULT 0;
