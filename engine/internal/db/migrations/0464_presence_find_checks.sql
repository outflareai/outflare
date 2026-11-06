-- Presence discovery reads each find once to see whether the page is about
-- the brand: whether it links to one of the brand's domains, names its
-- website, or only uses its name (which another product can share). The
-- Discovery list shows the pages about the brand first and keeps the rest
-- apart, so a find is never confirmed by hand just to learn what it is.
ALTER TABLE coverage_candidates
    ADD COLUMN verdict text NOT NULL DEFAULT 'unchecked'
        CHECK (verdict IN ('unchecked', 'links', 'mentions', 'name_only', 'absent', 'unreachable')),
    ADD COLUMN checked_at timestamptz;

CREATE INDEX coverage_candidates_unchecked_idx ON coverage_candidates (workspace_id, brand_id, created_at)
    WHERE verdict = 'unchecked' AND status = 'new';
