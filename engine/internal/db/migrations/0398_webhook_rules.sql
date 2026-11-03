-- A webhook per rule (plans/customer-data-platform, phase 3, "Actions: a
-- signed webhook per rule"): an endpoint can listen to some rules only.
-- With rule_ids set, events about a rule (rule.fired and the rest, whose
-- target is the rule) reach it only for those rules; other events are
-- unaffected, like categories for reply labels (migration 0065).
--
-- Expand only (docs/migrations.md): a column with a default. Old code
-- never sets it and delivers as before.

ALTER TABLE webhook_endpoints ADD COLUMN rule_ids uuid[] NOT NULL DEFAULT '{}';
