-- migration-safety: the version before this one only reads the dropped per-workspace name index through isUnique on create and rename; the per-brand index and the per-workspace index on profiles with no brand, created here, answer it the same way.
-- Brands own their data (plans/brands/README.md): an ideal customer
-- profile is one brand's. A profile saved with no brand described
-- customers for every brand, so every brand keeps it: the first brand
-- keeps the row (and its id), and every other brand gets its own copy.
-- Two brands can then use the same profile name.

CREATE UNIQUE INDEX ideal_customer_profiles_brand_name_idx ON ideal_customer_profiles (brand_id, lower(name));
DROP INDEX ideal_customer_profiles_name_idx;
CREATE INDEX ideal_customer_profiles_workspace_name_idx ON ideal_customer_profiles (workspace_id, lower(name));

INSERT INTO ideal_customer_profiles (workspace_id, brand_id, name, description, filters, status, version,
    created_by, updated_by, created_at, updated_at)
SELECT p.workspace_id, b.id, p.name, p.description, p.filters, p.status, p.version,
    p.created_by, p.updated_by, p.created_at, p.updated_at
FROM ideal_customer_profiles p
JOIN brands b ON b.workspace_id = p.workspace_id AND b.id <> outflare_first_brand(p.workspace_id)
WHERE p.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM ideal_customer_profiles o WHERE o.brand_id = b.id AND lower(o.name) = lower(p.name));

-- A lead list of another brand that scored against a shared profile scores
-- against its own brand's copy.
UPDATE lead_lists l SET profile_id = c.id
FROM ideal_customer_profiles p
JOIN ideal_customer_profiles c ON c.workspace_id = p.workspace_id AND c.brand_id IS NOT NULL AND lower(c.name) = lower(p.name)
WHERE l.profile_id = p.id AND p.brand_id IS NULL
  AND l.brand_id IS NOT NULL AND l.brand_id <> outflare_first_brand(l.workspace_id)
  AND c.brand_id = l.brand_id;

UPDATE ideal_customer_profiles SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

-- The version before this one still saves profiles with no brand during a
-- deploy or after a rollback: their names stay unique per workspace.
CREATE UNIQUE INDEX ideal_customer_profiles_shared_name_idx ON ideal_customer_profiles (workspace_id, lower(name)) WHERE brand_id IS NULL;
