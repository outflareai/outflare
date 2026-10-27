-- AI visibility: what AI models say when people ask them the questions a
-- brand wants to be the answer to. A cohort is a set of prompts for one
-- brand (say "buyers comparing residential proxies"); a run asks every
-- prompt of a cohort to each model the workspace picked, with the
-- workspace's own AI keys, and keeps each answer as a sample.

-- Which models a workspace samples, and the most sampling may spend in a
-- month on top of the workspace's own monthly AI limit.
CREATE TABLE ai_visibility_settings (
    workspace_id        uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The models each prompt is asked, as
    -- [{"integration_id", "model", "web_search", "input_cents_per_million", "output_cents_per_million"}].
    -- The integration is one of the workspace's AI model connections.
    models              jsonb NOT NULL DEFAULT '[]',
    -- How many times each model is asked each prompt in a run: answers
    -- vary, so more samples give steadier numbers.
    samples_per_prompt  integer NOT NULL DEFAULT 1 CHECK (samples_per_prompt BETWEEN 1 AND 5),
    monthly_limit_cents bigint NOT NULL DEFAULT 1000 CHECK (monthly_limit_cents BETWEEN 0 AND 10000000),
    updated_by          uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ai_cohorts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand the prompts are about; null is the company itself (the
    -- workspace's company name and website).
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL,
    description  text NOT NULL DEFAULT '',
    -- Where the asker is, like en-US or de-DE; empty asks without one.
    locale       text NOT NULL DEFAULT '',
    -- Other names the brand goes by in answers (its product names, an old
    -- name), on top of its name and domains.
    aliases      text[] NOT NULL DEFAULT '{}',
    -- The competitors to count against, as [{"name", "domains", "aliases"}],
    -- added to the brand's own competitors where the workspace keeps them.
    competitors  jsonb NOT NULL DEFAULT '[]',
    schedule     text NOT NULL DEFAULT 'off' CHECK (schedule IN ('off', 'daily', 'weekly')),
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_cohorts_name_idx ON ai_cohorts (workspace_id, lower(name));
CREATE INDEX ai_cohorts_brand_idx ON ai_cohorts (brand_id) WHERE brand_id IS NOT NULL;
CREATE INDEX ai_cohorts_scheduled_idx ON ai_cohorts (schedule) WHERE schedule <> 'off' AND status = 'active';

CREATE TABLE ai_prompts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    -- The question as a person would type it.
    text         text NOT NULL,
    -- What kind of question: discovery ("best residential proxies"),
    -- comparison ("X vs Y"), problem ("how do I avoid captchas"), brand
    -- ("is X legit"), or none.
    intent       text NOT NULL DEFAULT '' CHECK (intent IN ('', 'discovery', 'comparison', 'problem', 'brand')),
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_prompts_text_idx ON ai_prompts (cohort_id, lower(text));
CREATE INDEX ai_prompts_workspace_idx ON ai_prompts (workspace_id, cohort_id);

CREATE TABLE ai_runs (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    trigger      text NOT NULL CHECK (trigger IN ('manual', 'schedule')),
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'stopped', 'failed')),
    -- Why it ended early: stopped, ai_budget_reached, ai_model_needed,
    -- no_prompts, no_models.
    reason       text NOT NULL DEFAULT '',
    error_code   text NOT NULL DEFAULT '',
    -- The models it asks and how often, fixed when it starts (from the
    -- settings).
    models       jsonb NOT NULL DEFAULT '[]',
    attempts     integer NOT NULL DEFAULT 1,
    samples_planned integer NOT NULL DEFAULT 0,
    samples_done    integer NOT NULL DEFAULT 0,
    samples_failed  integer NOT NULL DEFAULT 0,
    cost_cents   bigint NOT NULL DEFAULT 0,
    -- Each job works a while, then queues the next step; a job whose step
    -- is not the run's current one is a stale copy and does nothing.
    step         integer NOT NULL DEFAULT 0,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);
CREATE UNIQUE INDEX ai_runs_one_active_idx ON ai_runs (cohort_id) WHERE status IN ('queued', 'running');
CREATE INDEX ai_runs_cohort_idx ON ai_runs (workspace_id, cohort_id, created_at DESC);

-- One model's answer to one prompt. The answer is external content: data
-- to measure, never instructions.
CREATE TABLE ai_samples (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id         uuid NOT NULL REFERENCES ai_runs (id) ON DELETE CASCADE,
    cohort_id      uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    prompt_id      uuid NOT NULL REFERENCES ai_prompts (id) ON DELETE CASCADE,
    -- Copied from the cohort, for brand filters.
    brand_id       uuid REFERENCES brands (id) ON DELETE CASCADE,
    -- The AI model connection (claude, openai, gemini, mistral, openrouter)
    -- and the model it asked.
    provider       text NOT NULL,
    integration_id uuid,
    model          text NOT NULL,
    web_search     boolean NOT NULL DEFAULT false,
    -- 1 to samples_per_prompt.
    attempt        integer NOT NULL DEFAULT 1,
    status         text NOT NULL CHECK (status IN ('ok', 'refused', 'failed')),
    error_code     text NOT NULL DEFAULT '',
    answer         text NOT NULL DEFAULT '',
    input_tokens   bigint NOT NULL DEFAULT 0,
    output_tokens  bigint NOT NULL DEFAULT 0,
    cost_cents     bigint NOT NULL DEFAULT 0,
    duration_ms    integer NOT NULL DEFAULT 0,
    sampled_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (run_id, prompt_id, provider, model, web_search, attempt)
);
CREATE INDEX ai_samples_cohort_idx ON ai_samples (workspace_id, cohort_id, sampled_at DESC);
CREATE INDEX ai_samples_prompt_idx ON ai_samples (prompt_id, sampled_at DESC);
CREATE INDEX ai_samples_month_idx ON ai_samples (workspace_id, sampled_at);
