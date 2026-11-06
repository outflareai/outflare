-- AI visibility reads answers from the real ChatGPT and Gemini screens, as
-- a signed-out person in a country sees them, through an answer reader the
-- workspace connects with its own account (DataForSEO). Those answers say
-- more than an API answer: the searches the assistant ran before it
-- answered, every page it read, the brands it named, and a link that asks
-- the same question again. They also cost a fraction of a cent, so their
-- cost is kept in millionths of a dollar.

ALTER TABLE ai_samples
    -- api: the model's API on an AI connection; screen: the assistant's
    -- own screen, read by an answer reader.
    ADD COLUMN surface      text NOT NULL DEFAULT 'api' CHECK (surface IN ('api', 'screen')),
    -- What a screen answer cost, in millionths of a US dollar (cost_cents
    -- stays 0 for those).
    ADD COLUMN cost_micros  bigint NOT NULL DEFAULT 0,
    -- The model the screen reported answering with (gpt-6).
    ADD COLUMN answer_model text NOT NULL DEFAULT '',
    -- The web searches the assistant ran before answering.
    ADD COLUMN searches     text[] NOT NULL DEFAULT '{}',
    -- Every brand the assistant named, as the reader recognised them, in
    -- order: the brand and its competitors, and others not tracked yet.
    ADD COLUMN brands_named text[] NOT NULL DEFAULT '{}',
    -- A link that asks the assistant the same question, to check by hand.
    ADD COLUMN check_url    text NOT NULL DEFAULT '';

ALTER TABLE ai_runs ADD COLUMN cost_micros bigint NOT NULL DEFAULT 0;

-- Pages the assistant read before answering, cited or not. A page that is
-- read but not cited, or that names competitors and not the brand, is a
-- place to be.
CREATE TABLE ai_pages_read (
    sample_id    uuid NOT NULL REFERENCES ai_samples (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    -- Copied from the cohort, for brand filters.
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    url          text NOT NULL,
    -- Lowercase, without www.
    host         text NOT NULL,
    title        text NOT NULL DEFAULT '',
    -- 1 for the first page in the assistant's search results.
    position     integer NOT NULL,
    -- Whose page it is, as for ai_citations.
    owner        text NOT NULL CHECK (owner IN ('brand', 'competitor', 'other')),
    competitor   text NOT NULL DEFAULT '',
    -- Whether the answer cited it too.
    cited        boolean NOT NULL DEFAULT false,
    sampled_at   timestamptz NOT NULL,
    PRIMARY KEY (sample_id, url)
);
CREATE INDEX ai_pages_read_cohort_idx ON ai_pages_read (workspace_id, cohort_id, sampled_at);
CREATE INDEX ai_pages_read_brand_idx ON ai_pages_read (workspace_id, brand_id, host);
