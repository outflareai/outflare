-- Projects: one motion, an offer sold to one ideal customer in one market.
-- A project holds the brief people and agents work from, and owns the
-- domains, mailboxes, lead lists and sequences that run the motion.
-- Anything without a project is shared by the whole workspace.

CREATE TABLE projects (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name             text NOT NULL,
    -- paused: none of its sequences send. archived: paused and hidden from
    -- lists, kept for reporting.
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'archived')),
    -- The brief. offer: what is sold. audience: who buys it (the ideal
    -- customer profile). market: where, and in which language. brief: the
    -- rest, in Markdown: messaging, proof, objections, words to avoid.
    offer            text NOT NULL DEFAULT '',
    audience         text NOT NULL DEFAULT '',
    market           text NOT NULL DEFAULT '',
    brief            text NOT NULL DEFAULT '',
    -- The audience as filters list builders and data providers can run:
    -- {titles, seniorities, industries, employee_ranges, locations,
    --  keywords}, each a list of strings.
    icp              jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(icp) = 'object'),
    -- Bumped on every brief change, so agents can tell theirs is stale and
    -- an edit made against an old brief fails instead of overwriting.
    brief_version    integer NOT NULL DEFAULT 1,
    brief_updated_at timestamptz NOT NULL DEFAULT now(),
    brief_updated_by uuid REFERENCES actors (id),
    -- Most cold sends from all of the project's sequences together in 24
    -- hours. Null leaves only the mailbox, domain and token limits.
    daily_send_limit integer CHECK (daily_send_limit BETWEEN 1 AND 1000000),
    created_by       uuid REFERENCES actors (id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX projects_name_idx ON projects (workspace_id, lower(name));

ALTER TABLE sending_domains ADD COLUMN project_id uuid REFERENCES projects (id) ON DELETE SET NULL;
ALTER TABLE mailboxes ADD COLUMN project_id uuid REFERENCES projects (id) ON DELETE SET NULL;
ALTER TABLE lead_lists ADD COLUMN project_id uuid REFERENCES projects (id) ON DELETE SET NULL;
ALTER TABLE sequences ADD COLUMN project_id uuid REFERENCES projects (id) ON DELETE SET NULL;

CREATE INDEX sending_domains_project_idx ON sending_domains (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX mailboxes_project_idx ON mailboxes (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX lead_lists_project_idx ON lead_lists (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX sequences_project_idx ON sequences (project_id) WHERE project_id IS NOT NULL;

-- A token bound to a project sees and changes only that project's
-- records, and creates new ones inside it. Deleting the project deletes
-- its tokens, so a project token can never widen to the whole workspace.
ALTER TABLE api_tokens ADD COLUMN project_id uuid REFERENCES projects (id) ON DELETE CASCADE;
