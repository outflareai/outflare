-- Reward rules become flows, as in Tolt: a trigger, an action (cash, an
-- action for the workspace's system, or moving the partner to a group),
-- conditions, its own approval, whether it adds to other cash flows or
-- is exclusive, and overrides per partner or product. Existing rules keep
-- working unchanged: the defaults are what they did before.
ALTER TABLE partner_reward_rules
    ADD COLUMN name text NOT NULL DEFAULT '' CHECK (length(name) <= 120),
    -- {"match": "all"|"any", "groups": [{"match": "all"|"any", "conditions": [{"field", "op", "value"}]}]};
    -- {} always matches.
    ADD COLUMN conditions jsonb NOT NULL DEFAULT '{}',
    -- program: the program's hold and auto-approve. auto: approved at once.
    -- after_days: approved approval_days after the conversion. manual: a
    -- person approves it.
    ADD COLUMN approval text NOT NULL DEFAULT 'program' CHECK (approval IN ('program', 'auto', 'after_days', 'manual')),
    ADD COLUMN approval_days integer CHECK (approval_days BETWEEN 0 AND 365),
    -- exclusive: only one exclusive cash flow pays a conversion. adds: it
    -- pays alongside it.
    ADD COLUMN stacking text NOT NULL DEFAULT 'exclusive' CHECK (stacking IN ('exclusive', 'adds')),
    -- [{"partner_id"|"product_id", "percent_bp"|"amount_cents"}]: another
    -- rate inside the flow for one partner or product.
    ADD COLUMN overrides jsonb NOT NULL DEFAULT '[]',
    -- kind group: the partner moves to this group.
    ADD COLUMN target_group_id uuid REFERENCES partner_groups (id) ON DELETE CASCADE;

ALTER TABLE partner_reward_rules DROP CONSTRAINT partner_reward_rules_kind_check;
ALTER TABLE partner_reward_rules ADD CONSTRAINT partner_reward_rules_kind_check
    CHECK (kind IN ('percent', 'fixed', 'action', 'group'));
ALTER TABLE partner_reward_rules ADD CONSTRAINT partner_reward_rules_group_target_check
    CHECK (kind <> 'group' OR target_group_id IS NOT NULL);

-- A reward's own approval from its flow; null follows the program.
ALTER TABLE partner_rewards ADD COLUMN auto_approve boolean;
