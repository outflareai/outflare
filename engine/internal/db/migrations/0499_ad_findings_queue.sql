-- Paid ads intelligence layer (plans/paid-ads/plan.md): ad_suggestions
-- becomes the findings queue. A finding is a proposed change, an alert, or
-- an automatic change that was made, each with an autonomy class (auto:
-- applied without asking, a saving or a protective change; approve: waits
-- for a person; alert: nothing to apply, a person acknowledges), the unit
-- its evidence is in, its expected impact, the rule that made it and
-- whether it was only recorded (shadow mode).
--
-- Expand only (docs/migrations.md): the kind CHECK widens to any kind of
-- the lever catalogue in code (a lever, or alert.<what>), columns are added
-- with defaults the previous version never writes, and the status CHECK
-- takes two more values. The previous version keeps writing the old kinds
-- and statuses.

-- The old kinds stay listed first: the previous version's brand leak test
-- seeds a row with the first value a CHECK lists, and its API answers only
-- those kinds.
ALTER TABLE ad_suggestions DROP CONSTRAINT ad_suggestions_kind_check;
ALTER TABLE ad_suggestions ADD CONSTRAINT ad_suggestions_kind_check
    CHECK (kind IN ('budget_up', 'budget_down', 'pause', 'resume', 'keyword_add', 'negative_add', 'keyword_split', 'bidding_target')
        OR kind ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)?$');

-- auto: applied without asking (a saving or a protective change); approve:
-- waits for a person; alert: nothing to apply, a person acknowledges.
ALTER TABLE ad_suggestions ADD COLUMN autonomy text NOT NULL DEFAULT 'approve' CHECK (autonomy IN ('auto', 'approve', 'alert'));
-- Expected change in cost per new paying customer, micros of the brand's
-- currency per payer (negative is better), and the unit the evidence is in.
ALTER TABLE ad_suggestions ADD COLUMN impact_micros bigint;
ALTER TABLE ad_suggestions ADD COLUMN unit text NOT NULL DEFAULT 'platform_conversions' CHECK (unit IN ('payers', 'signups', 'platform_conversions'));
-- Shadow: recorded, never applied or offered; what the rule would have done.
ALTER TABLE ad_suggestions ADD COLUMN shadow boolean NOT NULL DEFAULT false;
ALTER TABLE ad_suggestions ADD COLUMN applied_auto boolean NOT NULL DEFAULT false;
ALTER TABLE ad_suggestions ADD COLUMN reverted_at timestamptz;
ALTER TABLE ad_suggestions ADD COLUMN revert_change_id uuid REFERENCES ad_changes (id) ON DELETE SET NULL;
-- The rule that made it and its version, so a re-fit never changes old rows.
ALTER TABLE ad_suggestions ADD COLUMN rule text NOT NULL DEFAULT '';
ALTER TABLE ad_suggestions ADD COLUMN rule_version integer NOT NULL DEFAULT 1;
-- New statuses: acknowledged (an alert a person has seen), shadow (what a
-- rule would have done, never offered). testing stays: a suggestion
-- approved as a test inside the network (ad_tests) is still one.
ALTER TABLE ad_suggestions DROP CONSTRAINT ad_suggestions_status_check;
ALTER TABLE ad_suggestions ADD CONSTRAINT ad_suggestions_status_check
    CHECK (status IN ('open', 'dismissed', 'expired', 'measuring', 'testing', 'judged', 'acknowledged', 'shadow'));
CREATE INDEX ad_suggestions_queue_idx ON ad_suggestions (brand_id, status, autonomy, created_at DESC);
