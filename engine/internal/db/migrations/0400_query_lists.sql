-- Smart lists built from a saved query (plans/customer-data-platform,
-- phase 4, the owner's decision of 2026-11-02): kind query refreshes every
-- 15 minutes from the record_id column of the saved query it names, like a
-- rules list, with the same members table and history.
--
-- Expand only (docs/migrations.md): a widened CHECK and a nullable column.
-- The previous version's sweep refreshes kind smart only, so it leaves
-- query lists as they are.

ALTER TABLE smart_lists DROP CONSTRAINT smart_lists_kind_check;
ALTER TABLE smart_lists ADD CONSTRAINT smart_lists_kind_check CHECK (kind IN ('smart', 'static', 'query'));
ALTER TABLE smart_lists ADD COLUMN saved_query_id uuid REFERENCES saved_queries (id) ON DELETE SET NULL;
