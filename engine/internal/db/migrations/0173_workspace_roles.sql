-- Workspace roles: a workspace can change what Admin, Member (operator) and
-- Viewer allow, and add roles of its own. Owner is fixed and never stored.
--
-- A row with builtin set holds the workspace's version of that starting
-- role; without one, the starting role's defaults apply. A row without
-- builtin is a role the workspace made.
CREATE TABLE workspace_roles (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    builtin      text CHECK (builtin IN ('admin', 'operator', 'viewer')),
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    scopes       text[] NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, id)
);
CREATE UNIQUE INDEX workspace_roles_builtin_idx ON workspace_roles (workspace_id, builtin) WHERE builtin IS NOT NULL;
CREATE UNIQUE INDEX workspace_roles_name_idx ON workspace_roles (workspace_id, lower(name));

-- A member or invite on a workspace's own role. role still holds the
-- starting role nearest below it (Viewer when none fits), which is what a
-- version without custom roles reads after a rollback. Edits to the
-- starting roles are ignored by such a version, so it uses their defaults.
-- A role in use can't be deleted: its people move first.
ALTER TABLE memberships ADD COLUMN role_id uuid;
ALTER TABLE memberships ADD CONSTRAINT memberships_role_fk FOREIGN KEY (workspace_id, role_id) REFERENCES workspace_roles (workspace_id, id);
ALTER TABLE invites ADD COLUMN role_id uuid;
ALTER TABLE invites ADD CONSTRAINT invites_role_fk FOREIGN KEY (workspace_id, role_id) REFERENCES workspace_roles (workspace_id, id);
CREATE INDEX memberships_role_idx ON memberships (role_id) WHERE role_id IS NOT NULL;
CREATE INDEX invites_role_idx ON invites (role_id) WHERE role_id IS NOT NULL;

-- Owners can invite owners.
ALTER TABLE invites DROP CONSTRAINT invites_role_check;
ALTER TABLE invites ADD CONSTRAINT invites_role_check CHECK (role IN ('owner', 'admin', 'operator', 'viewer'));
