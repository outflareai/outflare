-- Field sources (plans/customer-data-platform, the owner's decision of
-- 2026-11-02): every source's latest value of each field is kept, and a
-- hand edit wins for a while (180 days by default) rather than forever,
-- unless it is pinned.

-- How long a hand edit wins, in days, for the brand's fields; 0: forever.
ALTER TABLE brands ADD COLUMN manual_edit_days integer NOT NULL DEFAULT 180 CHECK (manual_edit_days BETWEEN 0 AND 3650);
-- The same for one field; null: the brand's.
ALTER TABLE field_definitions ADD COLUMN manual_edit_days integer CHECK (manual_edit_days BETWEEN 0 AND 3650);

-- The latest value each source gave a record's field, whether or not it
-- won: what "Use this" offers, and what takes over when a hand edit
-- expires. A source clearing the field removes its row.
CREATE TABLE field_values (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    field_key    text NOT NULL,
    source       text NOT NULL,
    value        jsonb NOT NULL,
    seen_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (record_id, field_key, source)
);
CREATE INDEX field_values_workspace_idx ON field_values (workspace_id, brand_id);

-- The hand edit that set a field's current value: when it stops winning
-- (expires_at, null for never), whether it is pinned (never expires), and
-- when it expired (the sweep handed the field to another source, or found
-- none and left the value for the next source to replace).
CREATE TABLE hand_edits (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    field_key    text NOT NULL,
    set_at       timestamptz NOT NULL DEFAULT now(),
    set_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    expires_at   timestamptz,
    pinned       boolean NOT NULL DEFAULT false,
    pinned_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    expired_at   timestamptz,
    PRIMARY KEY (record_id, field_key)
);
CREATE INDEX hand_edits_due_idx ON hand_edits (expires_at) WHERE expired_at IS NULL AND NOT pinned AND expires_at IS NOT NULL;
CREATE INDEX hand_edits_workspace_idx ON hand_edits (workspace_id, brand_id);
