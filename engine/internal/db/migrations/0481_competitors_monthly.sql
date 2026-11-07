-- Every competitor is scanned at most once a month by default: the brand's
-- top competitors too, not just the rest, so a long competitor list doesn't
-- spend the workspace's SEO and search budgets every week. The owner asked
-- for every brand to move to the new default, so saved cadences (the
-- brand's and any competitor's own) are reset to it; they can be changed
-- again on the Presence competitors page. New brands get 30 from the code
-- that creates them, so the column's own default stays as it was for the
-- previous version.
--
-- Expand only: a data change; the previous version reads the same columns.
UPDATE brands SET presence_top_every_days = 30, presence_rest_every_days = 30
    WHERE presence_top_every_days <> 30 OR presence_rest_every_days <> 30;
UPDATE seo_competitors SET presence_every_days = NULL WHERE presence_every_days IS NOT NULL;
-- No SEO connection has a saved competitor read limit yet; drop any so all
-- of them use the monthly default.
UPDATE integrations SET config = config - 'competitor_read_days' WHERE config ? 'competitor_read_days';
