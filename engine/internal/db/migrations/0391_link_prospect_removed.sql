-- When a live link was found removed. A removed link on a page that is
-- still up goes back on the prospect list as one to win back, so its stage
-- moves on; this keeps that it was removed, for the results.
ALTER TABLE link_prospects ADD COLUMN removed_at timestamptz;
