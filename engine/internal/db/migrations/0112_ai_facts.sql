-- How accurate AI answers are about a brand: the facts the workspace says
-- are true (its prices, what it offers, where it sells), and, for each AI
-- answer that names the brand, which facts it agrees with or contradicts.

-- A fact the workspace states about one of its brands, or about the
-- company when brand_id is null.
CREATE TABLE ai_brand_facts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    topic        text NOT NULL DEFAULT '' CHECK (topic IN ('', 'pricing', 'product', 'company', 'availability', 'integrations', 'other')),
    -- One plain sentence, such as "Plans start at $49 a month".
    statement    text NOT NULL,
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    created_by   uuid,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX ai_brand_facts_statement_idx
    ON ai_brand_facts (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'), lower(statement));
CREATE INDEX ai_brand_facts_brand_idx ON ai_brand_facts (workspace_id, brand_id);

-- What one answer says about one fact. Facts an answer doesn't touch have
-- no row.
CREATE TABLE ai_fact_checks (
    sample_id    uuid NOT NULL REFERENCES ai_samples (id) ON DELETE CASCADE,
    fact_id      uuid NOT NULL REFERENCES ai_brand_facts (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cohort_id    uuid NOT NULL REFERENCES ai_cohorts (id) ON DELETE CASCADE,
    run_id       uuid NOT NULL REFERENCES ai_runs (id) ON DELETE CASCADE,
    -- supported: the answer agrees; contradicted: it says something else.
    verdict      text NOT NULL CHECK (verdict IN ('supported', 'contradicted')),
    -- The answer's own words, at most 300 characters.
    quote        text NOT NULL DEFAULT '',
    sampled_at   timestamptz NOT NULL,
    checked_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (sample_id, fact_id)
);
CREATE INDEX ai_fact_checks_fact_idx ON ai_fact_checks (workspace_id, fact_id, sampled_at);
CREATE INDEX ai_fact_checks_run_idx ON ai_fact_checks (run_id);

-- Whether an answer has been checked against the facts (checked, skipped
-- when the check could not run, failed), and what the check cost; the cost
-- counts towards AI visibility's monthly limit with the answer's own.
ALTER TABLE ai_samples
    ADD COLUMN facts_status text NOT NULL DEFAULT '' CHECK (facts_status IN ('', 'checked', 'skipped', 'failed')),
    ADD COLUMN facts_cost_cents bigint NOT NULL DEFAULT 0;
