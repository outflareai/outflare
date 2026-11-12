-- migration-safety: the version before this one finds a kit's parts and checks smart list names with its own queries (and a conflict on insert answers 409 through isUnique); the per-brand indexes created here answer the same way within a brand, and that version only sets kits up for the workspace's first brand.
-- Brands own their data (plans/brands/README.md): a kit's smart lists,
-- rules and journeys, and smart list names, are unique within a brand,
-- so the same kit can set up every brand and two brands can each have a
-- list of the same name. Rows with no brand keep the zero uuid, unique
-- among themselves as before.
CREATE UNIQUE INDEX smart_lists_brand_name_idx ON smart_lists (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));
DROP INDEX smart_lists_name_idx;

CREATE UNIQUE INDEX smart_lists_brand_kit_idx ON smart_lists (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), kit_id, kit_key)
    WHERE kit_key IS NOT NULL;
DROP INDEX smart_lists_kit_idx;

CREATE UNIQUE INDEX rules_brand_kit_idx ON rules (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), kit_id, kit_key)
    WHERE kit_key IS NOT NULL AND status <> 'archived';
DROP INDEX rules_kit_idx;

CREATE UNIQUE INDEX journeys_brand_kit_idx ON journeys (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), kit_id, kit_key)
    WHERE kit_key IS NOT NULL AND status <> 'archived';
DROP INDEX journeys_kit_idx;
