-- A program's payout term (Net N: a reward joins a payout N days after the
-- month it was earned in ends; null pays once it is approved) and the payout
-- methods and crypto tokens its partners choose from (empty is any). A
-- partner's chosen method and token sit beside their sealed payout details.
ALTER TABLE partner_programs
    ADD COLUMN payout_term_days integer CHECK (payout_term_days BETWEEN 0 AND 90),
    ADD COLUMN payout_methods   text[] NOT NULL DEFAULT '{}',
    ADD COLUMN payout_tokens    text[] NOT NULL DEFAULT '{}';

ALTER TABLE partner_memberships
    ADD COLUMN payout_type  text,
    ADD COLUMN payout_token text;
