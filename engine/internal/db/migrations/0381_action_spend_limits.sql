-- Spend limits on grants (ADR 13): a grant that runs changes can cap what
-- those changes spend on the workspace's own accounts a day and a month.
-- Spend booked while an action runs is linked to it, so the gate can add
-- it up. Expand only: nullable columns, a wider CHECK, and an index.

ALTER TABLE action_grants ADD COLUMN daily_spend_cents bigint CHECK (daily_spend_cents > 0);
ALTER TABLE action_grants ADD COLUMN monthly_spend_cents bigint CHECK (monthly_spend_cents > 0);

-- The action a charge was booked under, when an agent's change caused it.
ALTER TABLE spend_entries ADD COLUMN action_id uuid REFERENCES actions (id) ON DELETE SET NULL;
CREATE INDEX spend_entries_action_idx ON spend_entries (action_id) WHERE action_id IS NOT NULL;

-- A change held because its grant's spend limit would be passed.
ALTER TABLE actions DROP CONSTRAINT actions_decided_by_rule_check;
ALTER TABLE actions ADD CONSTRAINT actions_decided_by_rule_check
    CHECK (decided_by_rule IN ('', 'grant', 'no_grant', 'daily_limit', 'spend_limit', 'always_person'));
