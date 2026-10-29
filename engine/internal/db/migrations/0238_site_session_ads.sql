-- First-party ad tracking: the ad a paid visit came from, as the tracking
-- template's of_ parameters named it with the network's own ids
-- (engine/internal/adtrack): campaign, adset, ad and placement. Empty for
-- every other visit.
ALTER TABLE site_sessions ADD COLUMN ad jsonb NOT NULL DEFAULT '{}';
