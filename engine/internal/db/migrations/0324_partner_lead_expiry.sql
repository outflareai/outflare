-- A program's lead expiration: a referred customer who hasn't bought within
-- this many days of being referred no longer credits the partner. Null:
-- leads never expire.
ALTER TABLE partner_programs
    ADD COLUMN lead_expiry_days integer CHECK (lead_expiry_days BETWEEN 1 AND 3650);
