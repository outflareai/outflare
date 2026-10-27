-- What each AI answer named and cited: the brand, its competitors, and
-- the web pages it used. Share of voice, mention rates and citation rates
-- are counted from these.

-- One brand or competitor an answer names.
CREATE TABLE ai_mentions (
    sample_id    uuid NOT NULL REFERENCES ai_samples (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    -- brand (the cohort's brand) or competitor.
    entity       text NOT NULL CHECK (entity IN ('brand', 'competitor')),
    -- The competitor's name as the cohort lists it; the brand's name.
    name         text NOT NULL,
    -- 1 for the first name in the answer, 2 for the second, and so on.
    position     integer NOT NULL,
    -- How many times the answer names it.
    count        integer NOT NULL,
    sampled_at   timestamptz NOT NULL,
    PRIMARY KEY (sample_id, entity, name)
);
CREATE INDEX ai_mentions_cohort_idx ON ai_mentions (workspace_id, cohort_id, sampled_at);

-- One web page an answer cites.
CREATE TABLE ai_citations (
    sample_id    uuid NOT NULL REFERENCES ai_samples (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    url          text NOT NULL,
    -- Lowercase, without www.
    host         text NOT NULL,
    title        text NOT NULL DEFAULT '',
    -- 1 for the first page cited.
    position     integer NOT NULL,
    -- Whose page it is: brand (one of the brand's domains), competitor, or
    -- other (a review site, a forum, a news story...).
    owner        text NOT NULL CHECK (owner IN ('brand', 'competitor', 'other')),
    competitor   text NOT NULL DEFAULT '',
    sampled_at   timestamptz NOT NULL,
    PRIMARY KEY (sample_id, url)
);
CREATE INDEX ai_citations_cohort_idx ON ai_citations (workspace_id, cohort_id, sampled_at);
CREATE INDEX ai_citations_host_idx ON ai_citations (workspace_id, host);
