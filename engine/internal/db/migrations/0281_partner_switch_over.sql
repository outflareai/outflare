-- A program imported from another platform is switched over when that
-- platform stops paying it. Until then, commissions the import brought in
-- keep the platform's status: Outflare never approves them after its hold
-- or puts them in a payout, so nobody is paid twice.
ALTER TABLE partner_programs ADD COLUMN switched_over_at timestamptz;
