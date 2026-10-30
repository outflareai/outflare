-- The 7-day recovery window (plans/customer-data-platform/README.md, the
-- owner's decision of 2026-10-28): a deleted record, field value, identity,
-- link or product event (payments are product events) is hidden
-- everywhere at once, shows under Recently deleted, can be restored for 7
-- days, and is purged after that. Legal erasure (DELETE .../records/{id})
-- stays separate and permanent.
--
-- One person's action is one deletion: a record with its identities, links
-- and events, or a single field value, identity, link or event. Restoring
-- a deletion brings back everything in it together.
--
-- How a deleted item is hidden:
--   * A record keeps its row, with deleted_at and deletion_id set. Deleting
--     also archives it (archived_at, unless it already was), so every path
--     that leaves out archived records (smart lists, rules, journeys,
--     search, sends, Query's people and companies) leaves it out with no
--     change of its own; the few that show archived records check
--     deleted_at too. Restoring clears archived_at again only when the
--     deletion set it.
--   * Identities, links, product events, and the rows that hang off an
--     event (its ad conversion sends and attribution), move out of their
--     tables into deleted_rows, as JSON, in the same transaction. Nothing
--     can read them by mistake, unique values (an email, a message id) are
--     free for the live data again, and the event mirror's delete trigger
--     tells ClickHouse. Restoring inserts them again; a row whose unique
--     value was taken since, or whose other end is gone, stays here and
--     the restore says why.
--   * A field value is removed from the record's attributes; the value,
--     and the source that set it, wait in deleted_rows.
--
-- deleted_rows hold the moved rows themselves, not a copy: the live row is
-- gone. They carry the record they belong to, so erasing the record
-- deletes them too (ON DELETE CASCADE), and a privacy export includes them
-- (customers.Owned) because they are still held.
--
-- Expand only (docs/migrations.md): two new tables, two nullable columns,
-- indexes, and query.records replaced with the same columns and a filter.

CREATE TABLE deletions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- What was deleted: record, field_value, identity, relationship, event.
    kind          text NOT NULL CHECK (kind IN ('record', 'field_value', 'identity', 'relationship', 'event')),
    -- The record it is about; null only for an event of nobody known.
    -- Erasing the record deletes the deletion with it.
    record_id     uuid REFERENCES records (id) ON DELETE CASCADE,
    -- The id of what was deleted, or the field's key for a field value.
    target        text NOT NULL,
    -- Rows in it by table, as deleted.
    counts        jsonb NOT NULL DEFAULT '{}',
    -- Who deleted it.
    actor_id      uuid REFERENCES actors (id) ON DELETE SET NULL,
    actor_type    text NOT NULL DEFAULT 'system',
    user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    token_id      uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    request_id    text,
    deleted_at    timestamptz NOT NULL DEFAULT now(),
    -- When the purge removes it for good: 7 days after deleted_at.
    purge_after   timestamptz NOT NULL,
    -- Set once everything in it came back.
    restored_at   timestamptz,
    restored_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    -- What the last restore could not bring back, and why:
    -- [{"table", "id", "reason"}]. Those rows stay in deleted_rows.
    not_restored  jsonb NOT NULL DEFAULT '[]'
);

-- Recently deleted: a brand's deletions, newest first.
CREATE INDEX deletions_brand_idx ON deletions (brand_id, deleted_at DESC, id DESC) WHERE restored_at IS NULL;
CREATE INDEX deletions_record_idx ON deletions (record_id) WHERE record_id IS NOT NULL;
-- The purge.
CREATE INDEX deletions_purge_idx ON deletions (purge_after);

CREATE TABLE deleted_rows (
    id            bigserial PRIMARY KEY,
    deletion_id   uuid NOT NULL REFERENCES deletions (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The deletion's brand.
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The record the row was about, so erasing it deletes the row too.
    record_id     uuid REFERENCES records (id) ON DELETE CASCADE,
    -- Where the row lived (record_identities, record_relationships,
    -- customer_events, ad_conversion_sends, attribution_conversions,
    -- attribution_credits), event_inbox for an inbox row's link to its
    -- event, or attributes for a field value.
    source_table  text NOT NULL,
    row_id        text NOT NULL,
    data          jsonb NOT NULL
);

CREATE INDEX deleted_rows_deletion_idx ON deleted_rows (deletion_id, source_table, id);
CREATE INDEX deleted_rows_record_idx ON deleted_rows (record_id) WHERE record_id IS NOT NULL;

ALTER TABLE records ADD COLUMN deleted_at timestamptz;
ALTER TABLE records ADD COLUMN deletion_id uuid REFERENCES deletions (id) ON DELETE SET NULL;
CREATE INDEX records_deleted_idx ON records (deletion_id) WHERE deletion_id IS NOT NULL;

-- Query never shows a deleted record. people and companies already leave
-- out archived ones, which deleted records are.
CREATE OR REPLACE VIEW query.records WITH (security_barrier) AS
SELECT r.id, r.record_type, r.brand_id, r.name, r.attributes, r.origin, r.version, r.merged_into, r.archived_at, r.created_at, r.updated_at
FROM records r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE r.deleted_at IS NULL
    AND 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));
