-- Presence competitor scans: the rest of a brand's tracked competitors
-- default to every 90 days, not every 30. The top 10 stay monthly
-- (presence_top_every_days is already 30 by default). New brands get 90
-- from the code that creates them, so the column's own default stays 30
-- for the previous version after a rollback.
--
-- Only rows still on the old default (30) move to 90. A value someone set
-- to anything else (7, 14, 60, 90, …) is left alone. After 0481 every
-- brand was reset to 30/30, so 30 is the previous default rather than a
-- later customisation that happened to pick 30. Per-competitor
-- presence_every_days overrides are not touched: those are always set by
-- hand.
--
-- Expand only: a data change; the previous version reads the same columns.
UPDATE brands SET presence_rest_every_days = 90
    WHERE presence_rest_every_days = 30;
