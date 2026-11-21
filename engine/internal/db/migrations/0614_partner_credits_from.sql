-- An optional start time for earning partner credit. Existing programs keep
-- crediting conversions as before; existing referrals and rewards are unchanged.
ALTER TABLE partner_programs ADD COLUMN credits_from timestamptz;
