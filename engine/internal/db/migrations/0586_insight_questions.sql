-- Questions asked in plain English on the analytics pages (docs/insights.md,
-- "Ask"): the question, and the chart the workspace's own AI model built
-- from it (an insight kind and spec, checked like any saved insight). A
-- question is answered by a job, so the row carries its status; the chart
-- is saved as an insight only when a person saves it.
CREATE TABLE insight_questions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    question     text NOT NULL,
    -- queued, running, ready or failed. Only Outflare writes it, so it
    -- carries no check.
    status       text NOT NULL DEFAULT 'queued',
    -- Why it failed, in words a person can act on.
    error        text NOT NULL DEFAULT '',
    -- The answer: an insight kind ('' when the question can't be answered
    -- with a chart), a name for it, its spec and one or two sentences on
    -- how to read it.
    kind         text NOT NULL DEFAULT '',
    name         text NOT NULL DEFAULT '',
    spec         jsonb,
    explanation  text NOT NULL DEFAULT '',
    model        text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX insight_questions_brand_idx ON insight_questions (brand_id, created_at DESC);
CREATE INDEX insight_questions_workspace_idx ON insight_questions (workspace_id);
