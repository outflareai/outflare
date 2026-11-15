-- Scores, round two: what kind of number a score is, a headline score made
-- of other scores, scores on deals, and each record's score over time.

-- What kind of number it is, so nobody reads a guess as a fact: measured
-- (worked out from what happened: events, payments, fields), ai (read from
-- conversations by the brand's own AI) or predicted (an estimate of what
-- will happen, such as churn risk or the chance a deal is won).
ALTER TABLE score_definitions ADD COLUMN nature text NOT NULL DEFAULT 'measured'
    CHECK (nature IN ('measured', 'ai', 'predicted'));

-- A headline score has parts in place of factors: [{key, weight}], the
-- brand's other scores it is the weighted average of, such as health made
-- of usage, billing, engagement and sentiment.
ALTER TABLE score_definitions ADD COLUMN parts jsonb NOT NULL DEFAULT '[]';

-- Whether a high value is good (health) or bad (churn risk): it decides
-- the colour, never the number.
ALTER TABLE score_definitions ADD COLUMN higher_is_better boolean NOT NULL DEFAULT true;

-- A field that must be set for a record to get the score, such as
-- sentiment.value: a customer nobody has talked to has no sentiment score,
-- which is not a low one.
ALTER TABLE score_definitions ADD COLUMN needs text NOT NULL DEFAULT '' CHECK (length(needs) <= 80);

-- Deals can be scored too: the chance a deal is won.
ALTER TABLE score_definitions DROP CONSTRAINT score_definitions_record_type_check;
ALTER TABLE score_definitions ADD CONSTRAINT score_definitions_record_type_check
    CHECK (record_type IN ('person', 'account', 'deal'));

-- A record's score on each day it changed, so a page can show where it is
-- heading and a prediction can be checked against what happened. One row
-- per record, score and day: the last value of that day.
CREATE TABLE score_history (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    score_id     uuid NOT NULL REFERENCES score_definitions(id) ON DELETE CASCADE,
    day          date NOT NULL,
    value        smallint NOT NULL CHECK (value BETWEEN 0 AND 100),
    band         text NOT NULL CHECK (band IN ('high', 'medium', 'low')),
    PRIMARY KEY (record_id, score_id, day)
);
CREATE INDEX score_history_score ON score_history (score_id, day);
CREATE INDEX score_history_brand ON score_history (workspace_id, brand_id, day);

-- How a customer sounded in a conversation the brand's own AI read (the
-- same read that looks for signals): one row per read, with the
-- customer's own words behind the judgement. The record carries the
-- roll-up under attributes.sentiment (label, value, trend), which scores
-- read. A conversation that says nothing either way adds no row: unknown
-- is not unhappy.
CREATE TABLE conversation_sentiments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    channel      text NOT NULL CHECK (channel IN ('support', 'telegram', 'email', 'call')),
    ref          text NOT NULL CHECK (length(ref) BETWEEN 1 AND 200),
    label        text NOT NULL CHECK (label IN ('positive', 'mixed', 'negative')),
    quote        text NOT NULL DEFAULT '' CHECK (length(quote) <= 500),
    read_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX conversation_sentiments_record ON conversation_sentiments (record_id, read_at DESC);
CREATE INDEX conversation_sentiments_brand ON conversation_sentiments (workspace_id, brand_id, read_at DESC);
