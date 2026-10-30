-- Brands own their data (plans/brands/README.md): every workspace has at
-- least one brand. Sign-up makes the first; this gives one to every
-- workspace that has none, named after the workspace, with its slug as the
-- handle when that is a valid handle. Expand only: nothing is dropped.
INSERT INTO brands (workspace_id, name, handle)
SELECT w.id, left(w.name, 100),
	CASE WHEN left(w.slug, 40) ~ '^[a-z0-9][a-z0-9-]{0,38}[a-z0-9]$' AND left(w.slug, 40) NOT IN ('none', 'all')
		THEN left(w.slug, 40) ELSE 'brand' END
FROM workspaces w
WHERE NOT EXISTS (SELECT 1 FROM brands b WHERE b.workspace_id = w.id)
ON CONFLICT DO NOTHING;
