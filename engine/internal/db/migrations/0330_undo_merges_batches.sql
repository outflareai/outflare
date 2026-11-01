-- Undo for bad data, the rest of plans/brands/README.md "Undo for bad data"
-- and "Answered later" (the owner's decisions of 2026-10-30):
--   * Unmerge: a merge can be undone, and every moved row goes back.
--   * Undo a batch: every write an import or ingest batch makes carries the
--     batch, so a bad import or a bad batch of events can be rolled back in
--     one step, after a preview, undoing only what's untouched or
--     everything (restorable for 7 days, as one deletion).
--
-- Expand only (docs/migrations.md): new tables, nullable columns, a column
-- default, a widened CHECK and indexes. Old code never sets the batch
-- setting, so the default leaves its rows' batch_id null.

-- ---- Unmerge: the merge ledger ----

-- A merge made by this version writes down every row it moved, so it can
-- be undone. Older merges are not reversible: undoing one answers 409
-- merge_not_reversible.
ALTER TABLE record_merges ADD COLUMN reversible boolean NOT NULL DEFAULT false;
-- What the merge changed on the two records themselves: the merged
-- record's archived_at before, and the kept record's name.
ALTER TABLE record_merges ADD COLUMN snapshot jsonb NOT NULL DEFAULT '{}';
ALTER TABLE record_merges ADD COLUMN undone_at timestamptz;
ALTER TABLE record_merges ADD COLUMN undone_by uuid REFERENCES actors (id) ON DELETE SET NULL;
CREATE INDEX record_merges_from_idx ON record_merges (from_id);

-- One row per kind of change a merge made:
--   follow:     rows of table_name whose column_name moved to the kept
--               record; data is their primary keys, as moved.
--   identity_closed / identity_added: identity ids closed on the merged
--               record, and opened on the kept one (data: [ids]).
--   link_closed / link_added: the same for links.
--   merged_into: records that pointed at the merged record (data: [ids]).
--   field:      a field of the kept record the merge changed; data holds
--               key, prev, next, had and prev_source.
--   pointer:    another record's reference field repointed from the merged
--               record to the kept one; row_id is that record, data holds
--               key, prev and next.
--   candidate:  duplicate pairs the merge settled; data: [{id, status}].
CREATE TABLE record_merge_moves (
    id           bigserial PRIMARY KEY,
    merge_id     uuid NOT NULL REFERENCES record_merges (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    kind         text NOT NULL,
    table_name   text,
    column_name  text,
    row_id       text,
    data         jsonb NOT NULL DEFAULT '{}'
);

CREATE INDEX record_merge_moves_merge_idx ON record_merge_moves (merge_id, id);

-- ---- Undo a batch ----

-- One import or ingest batch: a CSV import of leads (csv_import), a CRM
-- import (crm_import, same id as crm_imports) or one POST of events to a
-- brand (events, same id as event_inbox.batch_id).
CREATE TABLE batches (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind          text NOT NULL CHECK (kind IN ('csv_import', 'crm_import', 'events')),
    -- Where it came from: csv, the CRM connector (attio) or the events'
    -- source label.
    source        text NOT NULL DEFAULT '',
    -- What people call it: the list a CSV went into, or empty.
    label         text NOT NULL DEFAULT '',
    actor_id      uuid REFERENCES actors (id) ON DELETE SET NULL,
    actor_type    text NOT NULL DEFAULT 'system',
    user_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    token_id      uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    request_id    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    -- Undoing it: queued or running in a job, then done or failed.
    undo_status   text CHECK (undo_status IN ('queued', 'running', 'done', 'failed')),
    undo_mode     text CHECK (undo_mode IN ('untouched', 'everything')),
    undone_at     timestamptz,
    undone_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    -- What the undo did, by kind, and what it left, by kind.
    undo_counts   jsonb NOT NULL DEFAULT '{}',
    -- The deletion that holds what it removed (Recently deleted).
    deletion_id   uuid REFERENCES deletions (id) ON DELETE SET NULL,
    undo_error    text
);

CREATE INDEX batches_brand_idx ON batches (brand_id, created_at DESC, id DESC);
CREATE INDEX batches_deletion_idx ON batches (deletion_id) WHERE deletion_id IS NOT NULL;

-- Each write a batch makes carries it. The writers set the transaction's
-- outflare.batch_id (set_config(..., true)) and these defaults pick it
-- up, so every insert under a batch is marked without passing it down.
-- Rows already here keep a null batch: there is no backfill.
ALTER TABLE records ADD COLUMN batch_id uuid;
ALTER TABLE record_identities ADD COLUMN batch_id uuid;
ALTER TABLE record_relationships ADD COLUMN batch_id uuid;
ALTER TABLE attribute_changes ADD COLUMN batch_id uuid;
ALTER TABLE customer_events ADD COLUMN batch_id uuid;
ALTER TABLE static_list_members ADD COLUMN batch_id uuid;
ALTER TABLE leads ADD COLUMN batch_id uuid;
ALTER TABLE lead_list_members ADD COLUMN batch_id uuid;

ALTER TABLE records ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE record_identities ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE record_relationships ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE attribute_changes ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE customer_events ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE static_list_members ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE leads ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;
ALTER TABLE lead_list_members ALTER COLUMN batch_id SET DEFAULT NULLIF(current_setting('outflare.batch_id', true), '')::uuid;

CREATE INDEX records_batch_idx ON records (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX record_identities_batch_idx ON record_identities (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX record_relationships_batch_idx ON record_relationships (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX attribute_changes_batch_idx ON attribute_changes (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX customer_events_batch_idx ON customer_events (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX static_list_members_batch_idx ON static_list_members (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX leads_batch_idx ON leads (batch_id) WHERE batch_id IS NOT NULL;
CREATE INDEX lead_list_members_batch_idx ON lead_list_members (batch_id) WHERE batch_id IS NOT NULL;

-- An undone batch is one deletion of kind batch: Recently deleted
-- restores it in one step.
ALTER TABLE deletions DROP CONSTRAINT deletions_kind_check;
ALTER TABLE deletions ADD CONSTRAINT deletions_kind_check
    CHECK (kind IN ('record', 'field_value', 'identity', 'relationship', 'event', 'batch'));
