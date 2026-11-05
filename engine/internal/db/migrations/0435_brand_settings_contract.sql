-- migration-safety: the version before this one (migration 0434) never upserts against the keys dropped here: opt-outs widen only while suppressions_workspace_id_kind_value_key exists and then insert with a bare ON CONFLICT DO NOTHING, and AI visibility and link building settings insert with a bare ON CONFLICT DO NOTHING and then update by workspace.
-- Brands own their data (plans/brands/README.md), the contract for
-- opt-outs and settings: one address can have an opt-out per brand, and
-- each brand has its own AI visibility and link building settings.
-- Extensions that record an opt-out upsert on the per-brand key from
-- migration 0431 (suppressions_brand_kind_value_idx), never on the one
-- dropped here.
-- partner_programs (workspace_id, slug) goes in the next contract step:
-- the version before this one tests that a second brand's program can't
-- reuse a slug.

ALTER TABLE suppressions DROP CONSTRAINT suppressions_workspace_id_kind_value_key;

ALTER TABLE ai_visibility_settings DROP CONSTRAINT ai_visibility_settings_pkey;
ALTER TABLE link_building_settings DROP CONSTRAINT link_building_settings_pkey;
CREATE INDEX ai_visibility_settings_workspace_idx ON ai_visibility_settings (workspace_id);
CREATE INDEX link_building_settings_workspace_idx ON link_building_settings (workspace_id);

-- Every other brand starts from a copy of the first brand's settings and
-- keeps its own from then on.
INSERT INTO ai_visibility_settings (workspace_id, brand_id, models, samples_per_prompt, monthly_limit_cents, updated_by, updated_at)
SELECT s.workspace_id, b.id, s.models, s.samples_per_prompt, s.monthly_limit_cents, s.updated_by, s.updated_at
FROM ai_visibility_settings s JOIN brands b ON b.workspace_id = s.workspace_id
WHERE s.brand_id = outflare_first_brand(s.workspace_id) AND b.id <> s.brand_id
ON CONFLICT DO NOTHING;
INSERT INTO link_building_settings (workspace_id, brand_id, pitch_cooldown_days, weekly_reads, weekly_budget_percent, recheck_days, updated_by, updated_at)
SELECT s.workspace_id, b.id, s.pitch_cooldown_days, s.weekly_reads, s.weekly_budget_percent, s.recheck_days, s.updated_by, s.updated_at
FROM link_building_settings s JOIN brands b ON b.workspace_id = s.workspace_id
WHERE s.brand_id = outflare_first_brand(s.workspace_id) AND b.id <> s.brand_id
ON CONFLICT DO NOTHING;
