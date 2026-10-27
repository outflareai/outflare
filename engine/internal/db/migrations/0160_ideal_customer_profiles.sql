-- Ideal customer profiles: who a workspace sells to, kept once and used
-- everywhere. A profile is a plain-words description plus filters in the
-- same vocabulary as lead searches (titles, seniorities, industries,
-- employee ranges, locations, keywords). Projects, lead lists, fit scores,
-- the lead finder and website visitor scoring all read the same profile,
-- so the ideal customer is never typed twice.
--
-- A project links its profile with profile_id, and projects.icp stays in
-- step with the linked profile's filters, so everything that reads
-- projects.icp today keeps working. Every project that already has an ICP
-- gets a profile of its own, named after it.

CREATE TABLE ideal_customer_profiles (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand it describes customers for; null for every brand.
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    -- Who they are and why they buy, in words. AI steps read it.
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 4000),
    -- {titles, seniorities, industries, employee_ranges, locations,
    --  keywords}, each a list of strings, as projects.icp.
    filters      jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(filters) = 'object'),
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    -- Bumped on every change to description or filters, so a run can say
    -- which version it used.
    version      integer NOT NULL DEFAULT 1,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX ideal_customer_profiles_name_idx ON ideal_customer_profiles (workspace_id, lower(name));

ALTER TABLE projects ADD COLUMN profile_id uuid REFERENCES ideal_customer_profiles (id) ON DELETE SET NULL;
CREATE INDEX projects_profile_idx ON projects (profile_id) WHERE profile_id IS NOT NULL;

-- Projects are few per workspace, so the backfill is quick.
WITH made AS (
    INSERT INTO ideal_customer_profiles (workspace_id, brand_id, name, description, filters, created_by, updated_by)
    SELECT workspace_id, brand_id, name, left(audience, 4000), icp, created_by, created_by
    FROM projects
    WHERE icp <> '{}'::jsonb
    RETURNING id, workspace_id, name
)
UPDATE projects p SET profile_id = m.id
FROM made m
WHERE p.workspace_id = m.workspace_id AND p.name = m.name;
