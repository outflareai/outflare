-- Queries in plain English (docs/query.md, "Describe it in English"): a
-- person describes the list or answer they want, the workspace's own AI
-- model writes it as a visual builder (a table, its columns, filters and
-- order) or, when the builder can't say it, as SQL, and the person adjusts
-- and saves it.
--
-- saved_query_versions keeps the builder a version was made with (null for
-- SQL written by hand; the SQL is always the builder's SQL when it is set)
-- and the English description it came from.
--
-- query_questions holds each description asked, answered by a job, as
-- insight_questions does for charts. A refinement ("also show their
-- country") carries what it changes (base_sql, base_builder).
--
-- Rollback-safe: two nullable or defaulted columns and a new table, which
-- the previous version never reads.

ALTER TABLE saved_query_versions
    ADD COLUMN builder  jsonb CHECK (builder IS NULL OR jsonb_typeof(builder) = 'object'),
    ADD COLUMN question text NOT NULL DEFAULT '' CHECK (length(question) <= 2000);

CREATE TABLE query_questions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    question     text NOT NULL,
    -- What the question changes, when it refines a query already open.
    base_sql     text NOT NULL DEFAULT '',
    base_builder jsonb,
    -- The read scopes of the person who asked, so the answer is checked
    -- against what they may read and nothing more.
    scopes       text[] NOT NULL DEFAULT '{}',
    -- queued, running, ready or failed. Only Outflare writes it, so it
    -- carries no check.
    status       text NOT NULL DEFAULT 'queued',
    -- Why it failed, in words a person can act on.
    error        text NOT NULL DEFAULT '',
    -- The answer: builder (builder and its sql), sql (sql only) or ''
    -- (it can't be answered from this data: explanation says why).
    kind         text NOT NULL DEFAULT '',
    name         text NOT NULL DEFAULT '',
    sql          text NOT NULL DEFAULT '',
    builder      jsonb,
    explanation  text NOT NULL DEFAULT '',
    model        text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz
);
CREATE INDEX query_questions_brand_idx ON query_questions (brand_id, created_at DESC);
CREATE INDEX query_questions_workspace_idx ON query_questions (workspace_id);
