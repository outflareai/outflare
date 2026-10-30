-- A program can let partners refer themselves. On by default, as before:
-- a partner's own sign-ups and purchases earn them nothing.
ALTER TABLE partner_programs ADD COLUMN block_self_referrals boolean NOT NULL DEFAULT true;
