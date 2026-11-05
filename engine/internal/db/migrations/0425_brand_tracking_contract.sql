-- migration-safety: the version before this one (the "look up keys and settings per brand" change) never upserts against the per-workspace keys dropped here: it upserts flag groups on (brand_id, name) and settings on brand_id, inserts conversion definitions with a bare ON CONFLICT DO NOTHING, finds keys within a brand, and reads settings preferring the first brand's row, so it runs the same on this schema.
-- Brands own their data, step 2 of making brands fully separate
-- (plans/brands/README.md), the contract: two brands can use the same
-- flag key, group name, conversion key and model key, and each brand has
-- its own visitor, attribution and ad settings. The per-brand unique
-- indexes (0423, 0424) are now the only ones for them. Campaign slugs keep
-- their per-workspace key one release longer: the version before this one
-- has a test that a slug is refused in a second brand, so a later
-- contract step drops it.

ALTER TABLE flag_groups DROP CONSTRAINT flag_groups_pkey;
DROP INDEX flags_key_idx;
ALTER TABLE conversion_definitions DROP CONSTRAINT conversion_definitions_workspace_id_key_key;
ALTER TABLE attribution_models DROP CONSTRAINT attribution_models_workspace_id_key_version_key;
ALTER TABLE visitor_settings DROP CONSTRAINT visitor_settings_pkey;
ALTER TABLE attribution_settings DROP CONSTRAINT attribution_settings_pkey;
ALTER TABLE ad_settings DROP CONSTRAINT ad_settings_pkey;

-- Lookups by workspace the dropped keys served.
CREATE INDEX flag_groups_workspace_idx ON flag_groups (workspace_id, name);
CREATE INDEX flags_workspace_key_idx ON flags (workspace_id, key);
CREATE INDEX conversion_definitions_workspace_key_idx ON conversion_definitions (workspace_id, key);
CREATE INDEX attribution_models_workspace_key_idx ON attribution_models (workspace_id, key, version);
CREATE INDEX visitor_settings_workspace_idx ON visitor_settings (workspace_id);
CREATE INDEX attribution_settings_workspace_idx ON attribution_settings (workspace_id);
CREATE INDEX ad_settings_workspace_idx ON ad_settings (workspace_id);
-- A workspace with no brand keeps one row of each.
CREATE UNIQUE INDEX visitor_settings_unbranded_idx ON visitor_settings (workspace_id) WHERE brand_id IS NULL;
CREATE UNIQUE INDEX attribution_settings_unbranded_idx ON attribution_settings (workspace_id) WHERE brand_id IS NULL;
CREATE UNIQUE INDEX ad_settings_unbranded_idx ON ad_settings (workspace_id) WHERE brand_id IS NULL;

-- The settings were the workspace's, and the first brand holds them. Every
-- other brand starts from a copy, so nothing a brand counts changes.
INSERT INTO visitor_settings (workspace_id, brand_id, key_pages, hot_score, email_clicks, exclude_domains, exclude_customers, updated_at,
    reveal_companies, reveal_integration_id, reveal_daily_limit, not_companies, reveal_people, people_notice_accepted_at, people_notice_accepted_by)
SELECT b.workspace_id, b.id, v.key_pages, v.hot_score, v.email_clicks, v.exclude_domains, v.exclude_customers, v.updated_at,
    v.reveal_companies, v.reveal_integration_id, v.reveal_daily_limit, v.not_companies, v.reveal_people, v.people_notice_accepted_at, v.people_notice_accepted_by
FROM brands b JOIN visitor_settings v ON v.workspace_id = b.workspace_id AND v.brand_id = outflare_first_brand(b.workspace_id)
WHERE b.id <> v.brand_id
ON CONFLICT DO NOTHING;

INSERT INTO attribution_settings (workspace_id, brand_id, model, lookback_days, conversion_events, backfilled_at, updated_by, updated_at)
SELECT b.workspace_id, b.id, v.model, v.lookback_days, v.conversion_events, v.backfilled_at, v.updated_by, v.updated_at
FROM brands b JOIN attribution_settings v ON v.workspace_id = b.workspace_id AND v.brand_id = outflare_first_brand(b.workspace_id)
WHERE b.id <> v.brand_id
ON CONFLICT DO NOTHING;

INSERT INTO ad_settings (workspace_id, brand_id, consent_mode, updated_at)
SELECT b.workspace_id, b.id, v.consent_mode, v.updated_at
FROM brands b JOIN ad_settings v ON v.workspace_id = b.workspace_id AND v.brand_id = outflare_first_brand(b.workspace_id)
WHERE b.id <> v.brand_id
ON CONFLICT DO NOTHING;
