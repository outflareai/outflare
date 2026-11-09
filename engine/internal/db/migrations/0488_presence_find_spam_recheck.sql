-- Presence's Found list gets two more answers besides "about us" and "not
-- us". Spam marks a site as spam: its pages leave the review list and any
-- page found on it later arrives already marked spam. Check again in a week
-- hides a page (one that is offline, say) until recheck_at, when it is read
-- again and comes back with what that read found. Only expands the schema:
-- the old version never writes spam and ignores recheck_at.
ALTER TABLE coverage_candidates DROP CONSTRAINT IF EXISTS coverage_candidates_status_check;
ALTER TABLE coverage_candidates ADD CONSTRAINT coverage_candidates_status_check
    CHECK (status IN ('new', 'confirmed', 'dismissed', 'spam'));
ALTER TABLE coverage_candidates ADD COLUMN recheck_at timestamptz;
