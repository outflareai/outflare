-- Tests inside the ad networks (step 4 of the paid ads plan): a person
-- starts a test on one campaign, and the network itself splits the
-- campaign's traffic (Google Ads experiments) or audience (Meta split
-- tests) between the campaign as it is and a trial that does one thing
-- differently. Both arms run on the same days, so they compare fairly.
-- Outflare reads both arms each day, gives a verdict once the test has run
-- long enough, and a person keeps the trial's change or rolls it back.
-- A suggestion can be tested instead of made straight away.
--
-- Expand only (docs/migrations.md): one new table, one new column, and the
-- suggestion status CHECK takes one more value. The previous version
-- ignores the table and column and never writes the new status.

CREATE TABLE ad_tests (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- The campaign's brand (its own, else its account's); null when the
    -- account is shared by every brand.
    brand_id         uuid REFERENCES brands(id) ON DELETE SET NULL,
    ad_entity_id     uuid NOT NULL REFERENCES ad_entities(id) ON DELETE CASCADE,
    suggestion_id    uuid REFERENCES ad_suggestions(id) ON DELETE SET NULL,
    name             text NOT NULL,
    -- What the trial does differently: its daily budget, or the cost per
    -- conversion it bids to.
    kind             text NOT NULL CHECK (kind IN ('budget', 'bidding')),
    -- starting (the job is creating it), running, judged (a verdict waits
    -- for a person), ending (keeping or rolling back), kept, rolled_back,
    -- failed (the network refused it).
    status           text NOT NULL DEFAULT 'starting'
        CHECK (status IN ('starting', 'running', 'judged', 'ending', 'kept', 'rolled_back', 'failed')),
    split_percent    integer NOT NULL DEFAULT 50 CHECK (split_percent BETWEEN 10 AND 90),
    -- The campaign's settings when the test started, and the trial's.
    control          jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(control) = 'object'),
    trial            jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(trial) = 'object'),
    -- The network's id for the test and the campaign serving the trial.
    platform_test_id text,
    trial_campaign_id text,
    starts_on        date NOT NULL,
    ends_on          date NOT NULL,
    -- The arms are compared from this day, after the trial has settled.
    measure_from     date NOT NULL,
    window_note      text NOT NULL DEFAULT '',
    -- Both arms' numbers so far, and the comparison.
    result           jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(result) = 'object'),
    verdict          text NOT NULL DEFAULT '' CHECK (verdict IN ('', 'worked', 'did_not_work', 'not_sure')),
    verdict_note     text NOT NULL DEFAULT '',
    decision         text NOT NULL DEFAULT '' CHECK (decision IN ('', 'keep', 'roll_back')),
    error            text,
    created_by       uuid REFERENCES actors(id) ON DELETE SET NULL,
    decided_by       uuid REFERENCES actors(id) ON DELETE SET NULL,
    decided_at       timestamptz,
    checked_at       timestamptz,
    judged_at        timestamptz,
    ended_at         timestamptz,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ad_tests_workspace_idx ON ad_tests (workspace_id, status, created_at DESC);
CREATE INDEX ad_tests_entity_idx ON ad_tests (ad_entity_id, created_at DESC);
-- One live test per campaign.
CREATE UNIQUE INDEX ad_tests_live_idx ON ad_tests (ad_entity_id) WHERE status IN ('starting', 'running', 'judged', 'ending');

-- A suggestion being tested names its test.
ALTER TABLE ad_suggestions ADD COLUMN test_id uuid REFERENCES ad_tests(id) ON DELETE SET NULL;
ALTER TABLE ad_suggestions DROP CONSTRAINT ad_suggestions_status_check;
ALTER TABLE ad_suggestions ADD CONSTRAINT ad_suggestions_status_check
    CHECK (status IN ('open', 'dismissed', 'expired', 'measuring', 'testing', 'judged'));
