-- The team directory: everyone who works at the company, whether or not
-- they sign in to Outflare, the departments and teams they belong to, and
-- the work accounts the company owns and hands to them (a Telegram
-- account, a work email, a phone number). Accounts belong to the company,
-- not the person: when someone leaves, an account passes to the next
-- holder, and staff_account_holders keeps who held it when.
--
-- All of it is workspace-level, like members and teams: one person can
-- work across brands.

CREATE TABLE departments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX departments_name_idx ON departments (workspace_id, lower(name));

CREATE TABLE staff (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name          text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- Work email, lower case. Null for someone without one yet.
    email         text CHECK (email = lower(email) AND length(email) BETWEEN 3 AND 320),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 200),
    department_id uuid REFERENCES departments (id) ON DELETE SET NULL,
    team_id       uuid REFERENCES teams (id) ON DELETE SET NULL,
    manager_id    uuid REFERENCES staff (id) ON DELETE SET NULL CHECK (manager_id <> id),
    -- Their Outflare sign-in, when they have one.
    user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    -- leaving: their handover has started; left: gone, kept for history.
    status        text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'leaving', 'left')),
    started_on    date,
    left_on       date,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX staff_email_idx ON staff (workspace_id, email) WHERE email IS NOT NULL;
CREATE UNIQUE INDEX staff_user_idx ON staff (workspace_id, user_id) WHERE user_id IS NOT NULL;
CREATE INDEX staff_department_idx ON staff (department_id) WHERE department_id IS NOT NULL;
CREATE INDEX staff_team_idx ON staff (team_id) WHERE team_id IS NOT NULL;
CREATE INDEX staff_manager_idx ON staff (manager_id) WHERE manager_id IS NOT NULL;

-- Who runs a department, and who leads a team.
ALTER TABLE departments ADD COLUMN head_id uuid REFERENCES staff (id) ON DELETE SET NULL;
ALTER TABLE teams ADD COLUMN department_id uuid REFERENCES departments (id) ON DELETE SET NULL;
ALTER TABLE teams ADD COLUMN lead_id uuid REFERENCES staff (id) ON DELETE SET NULL;

-- A work account the company owns. address is normalised: an email in
-- lower case, a phone number in E.164, a Telegram username without the @
-- in lower case (or its phone number until it has one).
CREATE TABLE staff_accounts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('telegram', 'email', 'phone')),
    address      text NOT NULL CHECK (length(address) BETWEEN 1 AND 320),
    -- What people call it, e.g. "Sales 3" or "Support line".
    label        text NOT NULL DEFAULT '' CHECK (length(label) <= 100),
    holder_id    uuid REFERENCES staff (id) ON DELETE SET NULL,
    -- retired: no longer used, kept for history.
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    -- When it must next be renewed or topped up (a phone number's plan),
    -- so nobody loses it by letting it lapse.
    renews_on    date,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX staff_accounts_address_idx ON staff_accounts (workspace_id, kind, address);
CREATE INDEX staff_accounts_holder_idx ON staff_accounts (holder_id) WHERE holder_id IS NOT NULL;

-- Who held an account when. One open row (ended_at null) per held account.
CREATE TABLE staff_account_holders (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    account_id   uuid NOT NULL REFERENCES staff_accounts (id) ON DELETE CASCADE,
    staff_id     uuid NOT NULL REFERENCES staff (id) ON DELETE CASCADE,
    started_at   timestamptz NOT NULL DEFAULT now(),
    ended_at     timestamptz,
    assigned_by  uuid REFERENCES actors (id)
);

CREATE UNIQUE INDEX staff_account_holders_open_idx ON staff_account_holders (account_id) WHERE ended_at IS NULL;
CREATE INDEX staff_account_holders_account_idx ON staff_account_holders (account_id, started_at DESC);
CREATE INDEX staff_account_holders_staff_idx ON staff_account_holders (staff_id);
