-- Removing projects, step 2 (ADR 12): nothing reads projects any more.
-- This catches up on what changed through the API since step 1 (0266),
-- then unlinks every record from its project. The tables and columns stay
-- until a later release drops them, so the release before still runs.

-- 1. Customer filters added since step 1.
WITH made AS (
    INSERT INTO ideal_customer_profiles (workspace_id, brand_id, name, description, filters, created_by, updated_by)
    SELECT p.workspace_id, p.brand_id, left(p.name, 100), left(p.audience, 4000), p.icp, p.created_by, p.created_by
    FROM projects p
    WHERE p.icp <> '{}'::jsonb AND p.profile_id IS NULL
      AND NOT EXISTS (SELECT 1 FROM ideal_customer_profiles x WHERE x.workspace_id = p.workspace_id AND lower(x.name) = lower(left(p.name, 100)))
    RETURNING id, workspace_id, name
)
UPDATE projects p SET profile_id = m.id
FROM made m
WHERE p.workspace_id = m.workspace_id AND left(p.name, 100) = m.name;

UPDATE lead_lists l SET profile_id = p.profile_id
FROM projects p
WHERE p.id = l.project_id AND p.profile_id IS NOT NULL AND l.profile_id IS NULL;

-- 2. Briefs written or changed since step 1 copied it, added to the brand's
-- notes (the company's when there is no brand), as step 1 did.
CREATE TEMPORARY TABLE changed_briefs ON COMMIT DROP AS
SELECT p.id, p.workspace_id, p.brand_id, p.name,
       '## ' || p.name || ' (was a project' || CASE WHEN p.brief_copied_version IS NULL THEN '' ELSE ', brief updated' END || ')' || E'\n\n' || concat_ws(E'\n\n',
           NULLIF('**Offer:** ' || btrim(p.offer), '**Offer:** '),
           NULLIF('**Audience:** ' || btrim(p.audience), '**Audience:** '),
           NULLIF('**Market:** ' || btrim(p.market), '**Market:** '),
           NULLIF(btrim(p.brief), '')) AS text
FROM projects p
WHERE (p.brief_copied_version IS NULL OR p.brief_version > p.brief_copied_version)
  AND (btrim(p.offer) <> '' OR btrim(p.audience) <> '' OR btrim(p.market) <> '' OR btrim(p.brief) <> '');

INSERT INTO brand_context (brand_id, workspace_id)
SELECT DISTINCT brand_id, workspace_id FROM changed_briefs WHERE brand_id IS NOT NULL
ON CONFLICT (brand_id) DO NOTHING;

UPDATE brand_context c
SET notes = concat_ws(E'\n\n', NULLIF(c.notes, ''), b.text), version = c.version + 1, updated_at = now()
FROM (SELECT brand_id, string_agg(text, E'\n\n' ORDER BY name) AS text FROM changed_briefs WHERE brand_id IS NOT NULL GROUP BY brand_id) b
WHERE c.brand_id = b.brand_id;

INSERT INTO workspace_context (workspace_id)
SELECT DISTINCT workspace_id FROM changed_briefs WHERE brand_id IS NULL
ON CONFLICT (workspace_id) DO NOTHING;

UPDATE workspace_context c
SET notes = concat_ws(E'\n\n', NULLIF(c.notes, ''), b.text), version = c.version + 1, updated_at = now()
FROM (SELECT workspace_id, string_agg(text, E'\n\n' ORDER BY name) AS text FROM changed_briefs WHERE brand_id IS NULL GROUP BY workspace_id) b
WHERE c.workspace_id = b.workspace_id;

UPDATE projects SET brief_copied_version = brief_version;

-- 3. The switch and the cap, for projects paused or capped since step 1.
UPDATE sequences s SET status = 'paused', updated_at = now()
FROM projects p
WHERE p.id = s.project_id AND p.status IN ('paused', 'archived') AND s.status = 'active';

UPDATE sequences s SET daily_send_limit = GREATEST(1, p.daily_send_limit / n.count)
FROM projects p
JOIN (SELECT project_id, count(*)::int AS count FROM sequences WHERE project_id IS NOT NULL GROUP BY project_id) n ON n.project_id = p.id
WHERE p.id = s.project_id AND p.daily_send_limit IS NOT NULL
  AND (s.daily_send_limit IS NULL OR s.daily_send_limit > GREATEST(1, p.daily_send_limit / n.count));

-- 4. Reply rules set for a project become each of its sequences' rules.
-- A sequence's own rule for a label still wins.
UPDATE reply_rules sr SET rules = pr.rules || sr.rules, updated_at = now()
FROM reply_rules pr
JOIN sequences s ON s.project_id = pr.project_id
WHERE pr.project_id IS NOT NULL AND sr.sequence_id = s.id;

INSERT INTO reply_rules (workspace_id, sequence_id, rules, updated_by)
SELECT pr.workspace_id, s.id, pr.rules, pr.updated_by
FROM reply_rules pr
JOIN sequences s ON s.project_id = pr.project_id
WHERE pr.project_id IS NOT NULL
  AND NOT EXISTS (SELECT 1 FROM reply_rules x WHERE x.sequence_id = s.id);

DELETE FROM reply_rules WHERE project_id IS NOT NULL;

-- 5. API tokens limited to one project become workspace tokens that keep
-- their scopes (the owner's choice). Each change is in the audit log.
INSERT INTO audit_events (workspace_id, actor_type, action, target_type, target_id, data)
SELECT t.workspace_id, 'system', 'token.updated', 'api_token', t.id::text,
       jsonb_build_object('renamed', false, 'scopes', to_jsonb(t.scopes), 'scopes_added', '[]'::jsonb,
                          'scopes_removed', '[]'::jsonb, 'reason', 'projects_removed', 'project_id', t.project_id)
FROM api_tokens t
WHERE t.project_id IS NOT NULL;

UPDATE api_tokens SET project_id = NULL WHERE project_id IS NOT NULL;

-- 6. Infrastructure plans and costs tied to a project keep its brand. A
-- plan now has a brand of its own, which its new domains and mailboxes
-- join.
ALTER TABLE infra_plans ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE infra_plans i SET brand_id = p.brand_id
FROM projects p WHERE p.id = i.project_id AND p.brand_id IS NOT NULL;

UPDATE spend_entries e SET brand_id = p.brand_id
FROM projects p WHERE p.id = e.project_id AND e.brand_id IS NULL AND p.brand_id IS NOT NULL;
UPDATE spend_recurring r SET brand_id = p.brand_id
FROM projects p WHERE p.id = r.project_id AND r.brand_id IS NULL AND p.brand_id IS NOT NULL;

-- 7. Unlink everything. Records keep their brand.
UPDATE sending_domains SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE mailboxes SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE lead_lists SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE sequences SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE missions SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE memories SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE list_runs SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE infra_plans SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE spend_recurring SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE spend_entries SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE meetings SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE deals SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE ai_calls SET project_id = NULL WHERE project_id IS NOT NULL;
UPDATE projects SET daily_send_limit = NULL, status = 'archived', updated_at = now();
