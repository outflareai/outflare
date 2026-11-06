-- AI visibility benchmark: how many of the AI answers DataForSEO keeps
-- (ChatGPT in the US in English, and Google's AI Overviews) mention a
-- cohort's brand and each competitor, read on the workspace's own
-- connection after a run, one row per cohort, day, brand and platform.
-- It reaches far more questions than the cohort asks itself.
CREATE TABLE ai_benchmarks (
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE CASCADE,
    cohort_id        uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    day              date NOT NULL,
    -- brand or competitor, and the competitor's name ('' for the brand).
    entity           text NOT NULL CHECK (entity IN ('brand', 'competitor')),
    name             text NOT NULL DEFAULT '',
    -- '' for all platforms, chatgpt or ai_overview.
    platform         text NOT NULL DEFAULT '' CHECK (platform IN ('', 'chatgpt', 'ai_overview')),
    mentions         integer NOT NULL CHECK (mentions >= 0),
    -- How many times a month people ask the questions those answers are to.
    ai_search_volume bigint NOT NULL DEFAULT 0 CHECK (ai_search_volume >= 0),
    created_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (cohort_id, day, entity, name, platform)
);
CREATE INDEX ai_benchmarks_workspace_idx ON ai_benchmarks (workspace_id, brand_id, cohort_id, day);
