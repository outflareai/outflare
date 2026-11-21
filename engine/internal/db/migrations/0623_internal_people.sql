-- Internal and test people: a brand's own team and its test accounts, by
-- email address or email domain, left out of its analytics reports
-- (funnels, trends, retention, stickiness, lifecycle and paths) while the
-- setting is on. One row per brand; no row means nobody is left out.
CREATE TABLE internal_people (
    brand_id     uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT false,
    -- Lowercase email domains, such as acme.example.
    domains      text[] NOT NULL DEFAULT '{}',
    -- Lowercase email addresses of single accounts.
    emails       text[] NOT NULL DEFAULT '{}',
    updated_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX internal_people_workspace_idx ON internal_people (workspace_id);
