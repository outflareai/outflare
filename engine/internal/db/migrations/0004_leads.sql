-- Leads, lists of leads, and the suppression list every send is checked against.

CREATE TABLE leads (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email        text NOT NULL,
    domain       text NOT NULL GENERATED ALWAYS AS (lower(split_part(email, '@', 2))) STORED,
    first_name   text NOT NULL DEFAULT '',
    last_name    text NOT NULL DEFAULT '',
    company      text NOT NULL DEFAULT '',
    title        text NOT NULL DEFAULT '',
    phone        text NOT NULL DEFAULT '',
    linkedin_url text NOT NULL DEFAULT '',
    website      text NOT NULL DEFAULT '',
    -- Any other imported columns, usable as template variables.
    custom       jsonb NOT NULL DEFAULT '{}',
    source       text NOT NULL DEFAULT 'csv',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX leads_email_idx ON leads (workspace_id, lower(email));
CREATE INDEX leads_domain_idx ON leads (workspace_id, domain);

CREATE TABLE lead_lists (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX lead_lists_name_idx ON lead_lists (workspace_id, lower(name));

CREATE TABLE lead_list_members (
    list_id  uuid NOT NULL REFERENCES lead_lists (id) ON DELETE CASCADE,
    lead_id  uuid NOT NULL REFERENCES leads (id) ON DELETE CASCADE,
    added_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, lead_id)
);

CREATE INDEX lead_list_members_lead_idx ON lead_list_members (lead_id);

-- An address or a whole domain that must never be emailed. Values are stored
-- lower case. Entries outlive the lead: deleting a lead keeps its suppression.
CREATE TABLE suppressions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('email', 'domain')),
    value        text NOT NULL CHECK (value = lower(value)),
    reason       text NOT NULL CHECK (reason IN ('unsubscribed', 'bounced', 'complained', 'manual', 'do_not_contact')),
    note         text NOT NULL DEFAULT '',
    source       text NOT NULL DEFAULT 'manual',
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, kind, value)
);
