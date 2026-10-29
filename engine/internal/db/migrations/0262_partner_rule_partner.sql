-- A reward rule for one partner: their own rate, which beats their group's
-- and the program's. Null is every partner (or the rule's group).
ALTER TABLE partner_reward_rules
    ADD COLUMN membership_id uuid REFERENCES partner_memberships (id) ON DELETE CASCADE;

CREATE INDEX partner_reward_rules_membership ON partner_reward_rules (membership_id) WHERE membership_id IS NOT NULL;
