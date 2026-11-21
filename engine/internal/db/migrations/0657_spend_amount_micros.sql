-- The exact amount of a spend entry, in millionths of its currency, when the
-- provider reported what a call cost to less than a cent (Treg answers with
-- each call's cost in millionths of a dollar). amount_cents still holds the
-- amount rounded up to a cent, so reports that read only cents see it;
-- whoever needs the exact cost of many small calls sums amount_micros.
-- A nullable column; the version before runs on unchanged.
ALTER TABLE spend_entries ADD COLUMN amount_micros bigint CHECK (amount_micros >= 0);
