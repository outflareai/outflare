-- When a verified tracking domain's lookups started failing. It stays
-- verified until they have failed for a while, so one stale "no record"
-- from a caching resolver doesn't take a working domain offline.
ALTER TABLE tracking_domains ADD COLUMN IF NOT EXISTS failing_since timestamptz;
