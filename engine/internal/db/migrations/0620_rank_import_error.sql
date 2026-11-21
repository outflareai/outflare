-- The words of a failed import of tracked keywords (which step of reading
-- the rank tracker failed, and how), so the Rankings page can say more
-- than "unavailable". Only a connector's own explained error is kept,
-- never an internal one.
ALTER TABLE rank_imports ADD COLUMN error text NOT NULL DEFAULT '';
