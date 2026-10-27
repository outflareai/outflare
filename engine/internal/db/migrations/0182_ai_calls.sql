-- Every call Outflare makes to a workspace's own AI model: which tool made
-- it, the provider and model, the tokens, and what it cost on the
-- workspace's own bill with the provider. It is how a workspace sees where
-- its AI spend goes inside Outflare. Prompts and answers are never stored
-- here: only counts, ids and the tool.
--
-- spend_entries stays the ledger the monthly AI limit is counted from; this
-- table adds the detail the ledger never had (tokens, model, tool) and also
-- keeps calls too cheap to round to a cent.
CREATE TABLE ai_calls (
    id                 bigserial PRIMARY KEY,
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    occurred_at        timestamptz NOT NULL DEFAULT now(),
    -- The tool or feature that made the call, as a model task id: plan,
    -- turn, check_in, reply_labelling, classify_domain, ai_visibility,
    -- ai_visibility_sampling, social_replies, email_verification, or one an
    -- extension names.
    task               text NOT NULL CHECK (task ~ '^[a-z][a-z0-9_]{0,63}$'),
    provider           text NOT NULL CHECK (length(provider) <= 64),  -- connector id: claude, openai...
    model              text NOT NULL DEFAULT '' CHECK (length(model) <= 200),
    integration_id     uuid REFERENCES integrations (id) ON DELETE SET NULL,
    mission_id         uuid REFERENCES missions (id) ON DELETE SET NULL,
    project_id         uuid REFERENCES projects (id) ON DELETE SET NULL,
    actor_id           uuid REFERENCES actors (id) ON DELETE SET NULL,
    input_tokens       bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens      bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_read_tokens  bigint NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens bigint NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    web_searches       bigint NOT NULL DEFAULT 0 CHECK (web_searches >= 0),
    -- US cents, rounded up. reported: the provider said what the call cost
    -- (OpenRouter); otherwise it is the model's price times the tokens.
    cost_cents         bigint NOT NULL DEFAULT 0 CHECK (cost_cents >= 0),
    cost_reported      boolean NOT NULL DEFAULT false,
    -- The same ref as the call's spend entry, so a retried job books a call
    -- once.
    ref                text CHECK (length(ref) <= 300)
);

CREATE INDEX ai_calls_ws_time ON ai_calls (workspace_id, occurred_at DESC);
CREATE INDEX ai_calls_ws_task_time ON ai_calls (workspace_id, task, occurred_at);
CREATE UNIQUE INDEX ai_calls_ref ON ai_calls (workspace_id, ref) WHERE ref IS NOT NULL;
CREATE INDEX ai_calls_mission ON ai_calls (mission_id) WHERE mission_id IS NOT NULL;
CREATE INDEX ai_calls_integration ON ai_calls (integration_id) WHERE integration_id IS NOT NULL;
CREATE INDEX ai_calls_project ON ai_calls (project_id) WHERE project_id IS NOT NULL;
CREATE INDEX ai_calls_actor ON ai_calls (actor_id) WHERE actor_id IS NOT NULL;
-- The daily purge of calls older than the retention window.
CREATE INDEX ai_calls_time ON ai_calls (occurred_at);

-- A monthly limit per tool, in US cents, on top of the workspace's
-- monthly_limit_cents: {"reply_labelling": 1000}. A tool left out has no
-- limit of its own.
ALTER TABLE workspace_ai_settings ADD COLUMN task_limits jsonb NOT NULL DEFAULT '{}';

-- Which spend warnings a workspace was sent, so each goes out once a month:
-- level 80 when AI calls reach 80% of a limit, 100 when they reach it.
-- task is '' for the workspace's own limit, else the tool's.
CREATE TABLE ai_budget_alerts (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    month        date NOT NULL,
    task         text NOT NULL DEFAULT '',
    level        smallint NOT NULL CHECK (level IN (80, 100)),
    limit_cents  bigint NOT NULL,
    spent_cents  bigint NOT NULL,
    sent_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, month, task, level)
);
