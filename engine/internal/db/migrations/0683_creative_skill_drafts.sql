-- An ad described in a person's own words ("an ad for developers whose
-- requests keep getting blocked, lead with our success rate"), and the
-- skill the workspace's own AI model drafted from it: a name, the prompt
-- for the picture and how to write the words over it. A job writes it, so
-- the row carries its status. Nothing is made and no skill is saved until
-- a person has read the draft and saves it.
CREATE TABLE creative_skill_drafts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    description  text NOT NULL,
    -- queued, running, ready or failed. Only Outflare writes it, so it
    -- carries no check.
    status       text NOT NULL DEFAULT 'queued',
    -- Why it failed, in words a person can act on.
    error        text NOT NULL DEFAULT '',
    -- The answer: a name and one line for the skill, the picture prompt,
    -- and how to write the headline, second line and button.
    name         text NOT NULL DEFAULT '',
    summary      text NOT NULL DEFAULT '',
    picture      text NOT NULL DEFAULT '',
    copy         text NOT NULL DEFAULT '',
    -- The chat model that wrote it.
    model        text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX creative_skill_drafts_brand_idx ON creative_skill_drafts (workspace_id, brand_id, created_at DESC);

CREATE VIEW query.creative_skill_drafts WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.description, x.status, x.error, x.name, x.summary, x.picture, x.copy, x.model,
    x.created_by, x.created_at, x.updated_at, x.finished_at
FROM creative_skill_drafts x
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'content.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
