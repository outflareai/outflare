-- A link's value to the brand, not just its site's domain rating: how it's
-- got (can't buy, earned, paid, free to get, unknown), a 0 to 100 value from
-- the page's authority, readers, trust, relevance to the brand's topic and
-- how many competitors it features, and the reasons as short phrases.
-- coverage.Value computes them when a placement is written; rows without
-- one are valued the next time their list is read.
--
-- Expand only: the previous version ignores the new columns.
ALTER TABLE coverage_competitor_placements
    ADD COLUMN value double precision,
    ADD COLUMN access text NOT NULL DEFAULT '' CHECK (access IN ('', 'cant_buy', 'earned', 'paid', 'free', 'unknown')),
    ADD COLUMN value_reasons text[] NOT NULL DEFAULT '{}';
CREATE INDEX coverage_competitor_placements_value_idx ON coverage_competitor_placements (workspace_id, brand_id, value DESC NULLS LAST);
