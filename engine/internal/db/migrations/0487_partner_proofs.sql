-- The proof behind a partner's money, kept when it is credited.
--
-- session_id is the website visit that credited a referral (via visit). It
-- has no foreign key: site_sessions rows are pruned, and the key would
-- need an index here for every session deleted. promo_code is the code the
-- conversion reported when via is promo_code.
ALTER TABLE partner_referrals ADD COLUMN session_id uuid;
ALTER TABLE partner_referrals ADD COLUMN promo_code text NOT NULL DEFAULT '' CHECK (length(promo_code) <= 64);

-- The rule that priced a reward, as it was then, with the rate it paid:
-- saving a program's rules replaces them, which clears rule_id.
ALTER TABLE partner_rewards ADD COLUMN rule jsonb;

-- A partner's history (GET .../partners/{id}/activity): events about the
-- membership itself, and its referrals, rewards and payouts, which name it
-- in data.partner_id. The query repeats this expression and predicate.
CREATE INDEX audit_events_partner_idx ON audit_events
    (workspace_id, (CASE WHEN target_type = 'partner' THEN target_id ELSE data->>'partner_id' END), id DESC)
    WHERE target_type IN ('partner', 'partner_referral', 'partner_reward', 'partner_payout');
