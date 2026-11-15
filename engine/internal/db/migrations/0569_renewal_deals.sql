-- Renewal deals: a deal opened for a renewal that is coming up says which
-- record renews (a company, or an agreement of it) and on which day, so
-- one renewal gets one deal, and the forecast counts it once.
-- renewal_of is a record's id without a foreign key: the version before
-- this one doesn't move it in a merge, and its check of every link to a
-- record must still pass on this schema. Merges move it (customers.Follows)
-- and erasing the record clears it (customers.Erase).
ALTER TABLE deals ADD COLUMN renewal_of uuid;
ALTER TABLE deals ADD COLUMN renewal_on date;
CREATE INDEX deals_renewal_of ON deals (renewal_of, renewal_on) WHERE renewal_of IS NOT NULL;
