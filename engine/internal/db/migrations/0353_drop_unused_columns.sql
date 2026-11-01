-- Columns nothing uses: missions.brief_version was the project brief a
-- mission was approved against (projects are gone, and the API stopped
-- reading it in v0.50.0), and allocation_declines.declined_at was never
-- read or written by name.
-- migration-safety: the release before this one (v0.50.0 on) reads neither column

ALTER TABLE missions DROP COLUMN brief_version;
ALTER TABLE allocation_declines DROP COLUMN declined_at;
