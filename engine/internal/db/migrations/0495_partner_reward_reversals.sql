-- The entries that take a reward back (reverses_id), which the Rewards list
-- and a reward's proof read for every reward they show.
CREATE INDEX partner_rewards_reverses_idx ON partner_rewards (reverses_id) WHERE reverses_id IS NOT NULL;
