-- Transactions in the 7-day recovery window (plans/customer-data-platform,
-- the owner's decision of 2026-10-28: "The 7-day recovery window covers
-- everything: ... product events and transactions").
--
-- Deleting a transaction moves its row into deleted_rows, as events are
-- (migration 0328), so lists, Query and the record card can't read it by
-- mistake. While it waits there, a billing sync or the API sending the
-- same source id again doesn't add it back: the index below finds it.
--
-- Expand only (docs/migrations.md): a widened CHECK and an index. Old code
-- never makes a transaction deletion.

ALTER TABLE deletions DROP CONSTRAINT deletions_kind_check;
ALTER TABLE deletions ADD CONSTRAINT deletions_kind_check
    CHECK (kind IN ('record', 'field_value', 'identity', 'relationship', 'event', 'batch', 'transaction'));

-- A deleted transaction by its brand, source and source id.
CREATE INDEX deleted_rows_transactions_idx ON deleted_rows (brand_id, (data->>'source'), (data->>'source_id'))
    WHERE source_table = 'transactions';
