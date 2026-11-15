-- Change rules whose conditions name a date relative to today (renews_on
-- lt +60d) can come true when a day passes with no change to the record,
-- so they are also tested against every record once a day. checked_at is
-- when a rule's last daily check was queued; the first check after a rule
-- goes live only notes where records stand. The version before ignores it.
ALTER TABLE rules ADD COLUMN checked_at timestamptz;
