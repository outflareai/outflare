-- The other ways a payment was also sent, each kept as this one
-- (transaction.merged), as source:source_id. A guess (the same person,
-- amount and currency within 15 minutes) takes only a payment with no copy
-- from that source yet, so two payments of the same amount seconds apart
-- each keep their own copies.
ALTER TABLE transactions ADD COLUMN copies text[] NOT NULL DEFAULT '{}';
