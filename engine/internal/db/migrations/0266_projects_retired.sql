-- Retiring projects (ADR 12): the model becomes workspace, brand,
-- sequences. This first step moves everything a project did onto the
-- brand, its sequences and its lists, so nothing changes when the web app
-- stops showing projects. Projects stay in place for the release before;
-- a later migration drops them.

-- A sequence's own cap on sends in 24 hours, all steps counted. Null
-- leaves only the new-leads, mailbox, domain and token limits.
ALTER TABLE sequences ADD COLUMN daily_send_limit integer CHECK (daily_send_limit BETWEEN 1 AND 1000000);

-- The ideal customer profile a list scores people against, under its own
-- filters. It replaces the project's ICP.
ALTER TABLE lead_lists ADD COLUMN profile_id uuid REFERENCES ideal_customer_profiles (id) ON DELETE SET NULL;
CREATE INDEX lead_lists_profile_idx ON lead_lists (profile_id) WHERE profile_id IS NOT NULL;

-- The brief version copied to the brand or company context, so the step
-- that removes projects copies only briefs changed since.
ALTER TABLE projects ADD COLUMN brief_copied_version integer;

-- 1. Customer filters: a project with an ICP and no profile gets one, and
-- its lists score against the project's profile.
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

-- 2. Briefs: offer, audience, market and the written brief are added to
-- the notes of the project's brand, or of the company when it has no
-- brand, under the project's name. Nothing is overwritten.
CREATE TEMPORARY TABLE retired_briefs ON COMMIT DROP AS
SELECT p.id, p.workspace_id, p.brand_id, p.brief_version, p.name,
       '## ' || p.name || ' (was a project)' || E'\n\n' || concat_ws(E'\n\n',
           NULLIF('**Offer:** ' || btrim(p.offer), '**Offer:** '),
           NULLIF('**Audience:** ' || btrim(p.audience), '**Audience:** '),
           NULLIF('**Market:** ' || btrim(p.market), '**Market:** '),
           NULLIF(btrim(p.brief), '')) AS text
FROM projects p
WHERE btrim(p.offer) <> '' OR btrim(p.audience) <> '' OR btrim(p.market) <> '' OR btrim(p.brief) <> '';

INSERT INTO brand_context (brand_id, workspace_id)
SELECT DISTINCT brand_id, workspace_id FROM retired_briefs WHERE brand_id IS NOT NULL
ON CONFLICT (brand_id) DO NOTHING;

UPDATE brand_context c
SET notes = concat_ws(E'\n\n', NULLIF(c.notes, ''), b.text), version = c.version + 1, updated_at = now()
FROM (SELECT brand_id, string_agg(text, E'\n\n' ORDER BY name) AS text FROM retired_briefs WHERE brand_id IS NOT NULL GROUP BY brand_id) b
WHERE c.brand_id = b.brand_id;

INSERT INTO workspace_context (workspace_id)
SELECT DISTINCT workspace_id FROM retired_briefs WHERE brand_id IS NULL
ON CONFLICT (workspace_id) DO NOTHING;

UPDATE workspace_context c
SET notes = concat_ws(E'\n\n', NULLIF(c.notes, ''), b.text), version = c.version + 1, updated_at = now()
FROM (SELECT workspace_id, string_agg(text, E'\n\n' ORDER BY name) AS text FROM retired_briefs WHERE brand_id IS NULL GROUP BY workspace_id) b
WHERE c.workspace_id = b.workspace_id;

UPDATE projects p SET brief_copied_version = p.brief_version;

-- 3. The project's switch: sequences of a paused or archived project are
-- paused, and the project itself goes back to active so the sequence is
-- the only switch left.
UPDATE sequences s SET status = 'paused', updated_at = now()
FROM projects p
WHERE p.id = s.project_id AND p.status IN ('paused', 'archived') AND s.status = 'active';

-- 4. The project's daily cap is split evenly over its sequences, rounded
-- down, so together they never send more than before. The project's own
-- cap stays until projects are removed, so the release before (and a
-- rollback to it) still keeps to it.
UPDATE sequences s SET daily_send_limit = GREATEST(1, p.daily_send_limit / n.count)
FROM projects p
JOIN (SELECT project_id, count(*)::int AS count FROM sequences WHERE project_id IS NOT NULL GROUP BY project_id) n ON n.project_id = p.id
WHERE p.id = s.project_id AND p.daily_send_limit IS NOT NULL;

UPDATE projects SET status = 'active', updated_at = now() WHERE status <> 'active';
