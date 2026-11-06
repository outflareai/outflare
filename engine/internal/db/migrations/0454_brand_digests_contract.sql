-- migration-safety: the version before this one (migration 0435) never upserts against the keys dropped here: digest settings insert with a bare ON CONFLICT DO NOTHING and then update by workspace, scheduled digests insert with a bare ON CONFLICT DO NOTHING, and partner programs insert with a bare ON CONFLICT DO NOTHING.
-- Brands own their data (plans/brands/README.md), the contract for
-- digests and partner programs: each brand has its own digest settings and
-- its own digests, and two brands can use the same program slug.

ALTER TABLE digest_settings DROP CONSTRAINT digest_settings_pkey;
CREATE INDEX digest_settings_workspace_idx ON digest_settings (workspace_id);
DROP INDEX digests_period_idx;

ALTER TABLE partner_programs DROP CONSTRAINT partner_programs_workspace_id_slug_key;
CREATE INDEX partner_programs_workspace_slug_idx ON partner_programs (workspace_id, slug);

-- Every other brand starts from a copy of the first brand's digest
-- settings and keeps its own from then on.
INSERT INTO digest_settings (workspace_id, brand_id, frequency, weekday, hour, timezone, recipients, provider_id, from_address, updated_by, updated_at)
SELECT s.workspace_id, b.id, s.frequency, s.weekday, s.hour, s.timezone, s.recipients, s.provider_id, s.from_address, s.updated_by, s.updated_at
FROM digest_settings s JOIN brands b ON b.workspace_id = s.workspace_id
WHERE s.brand_id = outflare_first_brand(s.workspace_id) AND b.id <> s.brand_id
ON CONFLICT DO NOTHING;
