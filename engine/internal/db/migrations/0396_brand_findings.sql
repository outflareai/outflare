-- Findings: what an agent (or a person) learned about a brand's marketing,
-- kept apart from facts and from operational state (plans/brand-agent,
-- "Brand state"). Each carries its evidence, how sure it is, the scope it
-- holds for (a channel, an audience, an objective) and a date to review
-- it, so the next decision can use it without over-generalising, and a
-- stale one is looked at again rather than trusted forever. Findings are
-- data agents read, never instructions that change what runs.
CREATE TABLE brand_findings (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- What was learned, in a sentence or two.
    text           text NOT NULL CHECK (length(text) BETWEEN 1 AND 1000),
    -- Why it is believed: a list of {kind, ref, note}, at least one. kind
    -- is metric, action, experiment or observation; ref names the metric,
    -- action, experiment or page it rests on.
    evidence       jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(evidence) = 'array'),
    confidence     text NOT NULL CHECK (confidence IN ('low', 'medium', 'high')),
    -- Where it holds: {channel, audience, objective_id}, each optional.
    scope          jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(scope) = 'object'),
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'retired')),
    -- When to look at it again; a finding past its review date is listed
    -- as due.
    review_at      timestamptz NOT NULL,
    retired_reason text NOT NULL DEFAULT '' CHECK (length(retired_reason) <= 500),
    retired_at     timestamptz,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX brand_findings_brand_idx ON brand_findings (brand_id, status, review_at);
CREATE INDEX brand_findings_workspace_idx ON brand_findings (workspace_id, created_at DESC);
