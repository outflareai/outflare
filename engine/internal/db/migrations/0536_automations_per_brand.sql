-- Playbook automations belong to a brand (brands own their data): the
-- workspace routes for them are retired, so one about no brand could no
-- longer be seen or changed. Each goes to its workspace's first brand.
-- Expand-only: no column changes, and the previous version reads these
-- rows as before.
UPDATE playbook_automations
SET brand_id = outflare_first_brand(workspace_id)
WHERE brand_id IS NULL;
