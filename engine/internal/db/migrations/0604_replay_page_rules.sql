-- Finer control of what a brand's website records: which pages, how
-- short a visit is still worth keeping, and how many visits a day.

-- Pages recorded and counted for heatmaps. A path such as /pricing, or
-- with * for any run of characters (/blog/*). With include_paths empty
-- every page counts; a page in exclude_paths never does.
ALTER TABLE replay_settings ADD COLUMN include_paths text[] NOT NULL DEFAULT '{}';
ALTER TABLE replay_settings ADD COLUMN exclude_paths text[] NOT NULL DEFAULT '{}';
-- Recordings of visits shorter than this many seconds are not kept.
ALTER TABLE replay_settings ADD COLUMN min_seconds integer NOT NULL DEFAULT 0 CHECK (min_seconds BETWEEN 0 AND 600);
-- The most visits recorded in a day (UTC); 0 is no limit of the brand's own.
ALTER TABLE replay_settings ADD COLUMN daily_limit integer NOT NULL DEFAULT 0 CHECK (daily_limit BETWEEN 0 AND 100000);
