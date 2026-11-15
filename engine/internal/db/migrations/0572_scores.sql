-- Scores: a number from 0 to 100 a brand keeps on every company or person,
-- such as a health score or a lead score. A score starts from a base and
-- adds or takes points for each factor whose conditions hold on the
-- record ("usage fell by a third: -20"), so anyone can read why a record
-- scores what it does. The value lives on the record under
-- attributes.scores.<key>, with <key>_band (high, medium or low), where
-- rules, journeys, smart lists and Query read it like any field.
CREATE TABLE score_definitions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    key          text NOT NULL CHECK (length(key) BETWEEN 1 AND 40),
    label        text NOT NULL CHECK (length(label) BETWEEN 1 AND 80),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
    record_type  text NOT NULL CHECK (record_type IN ('person', 'account')),
    -- Where every record starts, before its factors.
    base         integer NOT NULL DEFAULT 50 CHECK (base BETWEEN 0 AND 100),
    -- [{label, conditions, match, points}]: points are added when the
    -- conditions hold.
    factors      jsonb NOT NULL DEFAULT '[]',
    -- high from high_from up, low below low_below, medium between.
    high_from    integer NOT NULL DEFAULT 70 CHECK (high_from BETWEEN 1 AND 100),
    low_below    integer NOT NULL DEFAULT 40 CHECK (low_below BETWEEN 0 AND 99),
    -- The record fields the factors test: a change to one re-scores the
    -- record.
    watch_fields text[] NOT NULL DEFAULT '{}',
    status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    -- A change to how it scores makes a new version, and every record is
    -- scored again, quietly.
    version      integer NOT NULL DEFAULT 1,
    -- Since when a pass over every record has been waiting or running.
    scoring_since timestamptz,
    -- When every record was last scored in one pass.
    scored_at    timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, key),
    CHECK (low_below < high_from)
);
CREATE INDEX score_definitions_active ON score_definitions (workspace_id) WHERE status = 'active';
