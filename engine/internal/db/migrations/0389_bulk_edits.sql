-- Bulk edits (plans/customer-data-platform, step 6, governance): an owner
-- changes many records at once (a smart list's members, a saved query's
-- rows or records picked by hand) after a preview. The owner's decisions
-- of 2026-11-02: only owners run them, with no size approval; and every
-- change lands in each record's own history, never only in an aggregate.
--
-- Expand only (docs/migrations.md): a new table, nullable columns with a
-- default and indexes. Old code never sets the setting, so its rows' new
-- columns stay null.

CREATE TABLE bulk_edits (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- person or account: the records it changes.
    record_type   text NOT NULL CHECK (record_type IN ('person', 'account')),
    -- Where the records came from: smart_list (source_id is the list),
    -- query (a saved query) or records (picked by hand), or undo
    -- (source_id is the bulk edit it undoes).
    source        text NOT NULL CHECK (source IN ('smart_list', 'query', 'records', 'undo')),
    source_id     uuid,
    -- What it does to each record, as sent (docs/bulk-edits.md).
    changes       jsonb NOT NULL,
    -- The records, fixed when it was made: what the preview showed.
    record_ids    uuid[] NOT NULL,
    -- queued, running, done or failed.
    status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- Records changed, unchanged (already so) and skipped (gone, merged
    -- or another brand's), and how many field values changed.
    counts        jsonb NOT NULL DEFAULT '{}',
    actor_id      uuid REFERENCES actors (id) ON DELETE SET NULL,
    user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    request_id    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    finished_at   timestamptz,
    error         text,
    -- Set on the original when an undo of it is made.
    undone_by     uuid REFERENCES bulk_edits (id) ON DELETE SET NULL
);

CREATE INDEX bulk_edits_brand_idx ON bulk_edits (brand_id, created_at DESC, id DESC);

-- Each history row a bulk edit writes carries it, so a record's history
-- says which bulk edit (and through it who) made the change. The job sets
-- the transaction's outflare.bulk_edit_id (set_config(..., true)) and the
-- defaults pick it up, like outflare.batch_id (migration 0330).
ALTER TABLE attribute_changes ADD COLUMN bulk_edit_id uuid;
ALTER TABLE smart_list_changes ADD COLUMN bulk_edit_id uuid;
ALTER TABLE attribute_changes ALTER COLUMN bulk_edit_id SET DEFAULT NULLIF(current_setting('outflare.bulk_edit_id', true), '')::uuid;
ALTER TABLE smart_list_changes ALTER COLUMN bulk_edit_id SET DEFAULT NULLIF(current_setting('outflare.bulk_edit_id', true), '')::uuid;
CREATE INDEX attribute_changes_bulk_edit_idx ON attribute_changes (bulk_edit_id) WHERE bulk_edit_id IS NOT NULL;
CREATE INDEX smart_list_changes_bulk_edit_idx ON smart_list_changes (bulk_edit_id) WHERE bulk_edit_id IS NOT NULL;

-- Query (docs/query.md).
CREATE VIEW query.bulk_edits WITH (security_barrier) AS
SELECT b.id, b.brand_id, b.record_type, b.source, b.source_id, b.changes, cardinality(b.record_ids) AS records,
    b.status, b.counts, b.actor_id, b.created_at, b.finished_at, b.undone_by
FROM bulk_edits b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR b.brand_id = s.brand);
