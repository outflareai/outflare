-- Per-field classification (plans/customer-data-platform, blueprint 24.4;
-- the owner's decisions of 2026-11-03). Besides its sensitivity, every
-- field and event carries who owns it, how long its data is meant to be
-- kept and where it may be sent. They are labels only: nothing reads them
-- to block, hide or delete anything ("Label only").
--
-- Fields: built-in fields have no row of their own in field_definitions,
-- so a field's labels, built in or not, live in field_labels by its record
-- type and key. A built-in field's sensitivity can be set here too; a
-- custom field keeps its sensitivity in field_definitions.
-- Events: every event has a row in event_definitions, so the labels are
-- columns there.
--
-- retention_days null is forever. destinations null is every destination
-- (webhooks, exports, ai, published_queries, email), so one added later
-- is allowed for fields nobody restricted.
--
-- Expand only (docs/migrations.md): a new table and nullable columns.

CREATE TABLE field_labels (
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_type    text NOT NULL,
    key            text NOT NULL,
    -- Only for a built-in field; null keeps its default.
    sensitivity    text CHECK (sensitivity IN ('public', 'internal', 'confidential', 'restricted')),
    -- The team member who looks after it.
    owner_id       uuid REFERENCES users (id) ON DELETE SET NULL,
    retention_days int CHECK (retention_days BETWEEN 1 AND 36500),
    destinations   text[] CHECK (destinations <@ ARRAY['webhooks', 'exports', 'ai', 'published_queries', 'email']::text[]),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id, record_type, key)
);
CREATE INDEX field_labels_workspace_idx ON field_labels (workspace_id);
CREATE INDEX field_labels_owner_idx ON field_labels (owner_id) WHERE owner_id IS NOT NULL;

ALTER TABLE event_definitions ADD COLUMN owner_id uuid REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE event_definitions ADD COLUMN retention_days int CHECK (retention_days BETWEEN 1 AND 36500);
ALTER TABLE event_definitions ADD COLUMN destinations text[]
    CHECK (destinations <@ ARRAY['webhooks', 'exports', 'ai', 'published_queries', 'email']::text[]);
CREATE INDEX event_definitions_owner_idx ON event_definitions (owner_id) WHERE owner_id IS NOT NULL;
