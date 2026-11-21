-- Removing a campaign, an ad or a keyword can't be undone on the ad
-- platform, so the action layer lets an agent token do it only under a
-- grant that names the removal itself (never a broad one such as "**").
-- Such a grant can also be kept to some removals: conditions holds them
-- ({"paused_campaign_only": true, "disapproved_only": true,
-- "max_spend_micros": 0}), and a removal outside them is refused.
--
-- Expand only (docs/migrations.md): a new nullable column. The previous
-- version ignores it and refuses every removal by an agent, as before.
ALTER TABLE action_grants ADD COLUMN conditions jsonb;
