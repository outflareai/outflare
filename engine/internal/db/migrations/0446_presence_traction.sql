-- Presence traction: whether each live placement has taken off. Once a
-- day each live placement gets a verdict from what Outflare already
-- records: how often AI answers to the brand's questions cite the page
-- (last 90 days) and how many visits the brand's tracked site got from
-- the page's site (last 28 days). too_early in its first two weeks, then
-- quiet (no sign), steady (some) or taking_off (cited often or sending
-- real traffic). unknown while the page isn't confirmed live.

ALTER TABLE coverage_placements
    ADD COLUMN traction text NOT NULL DEFAULT 'unknown' CHECK (traction IN ('unknown', 'too_early', 'quiet', 'steady', 'taking_off')),
    ADD COLUMN traction_at timestamptz,
    ADD COLUMN ai_citations_90d integer NOT NULL DEFAULT 0;
