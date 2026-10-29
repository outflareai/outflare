-- What a workspace counts as a conversion, defined once on the customer
-- events stream (engine/internal/conversions): a customer event, filters
-- on its properties, what it is worth, and each ad network's own name for
-- it. Attribution credits channels with these; ad send-back and partner
-- commissions read the same definitions. Never tied to one integration.
CREATE TABLE conversion_definitions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    key               text NOT NULL CHECK (key ~ '^[a-z][a-z0-9_-]{0,39}$'),
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 80),
    event_name        text NOT NULL CHECK (event_name ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$' AND length(event_name) <= 100),
    -- [{"property": "duration_s", "op": "gt", "value": 60}], all must match.
    filters           jsonb NOT NULL DEFAULT '[]',
    value_mode        text NOT NULL DEFAULT 'none' CHECK (value_mode IN ('none', 'fixed', 'property')),
    fixed_value_minor bigint CHECK (fixed_value_minor >= 0),
    value_property    text NOT NULL DEFAULT '' CHECK (length(value_property) <= 330),
    value_unit        text NOT NULL DEFAULT 'major' CHECK (value_unit IN ('major', 'minor')),
    -- '' takes the event's currency property.
    currency          text NOT NULL DEFAULT '' CHECK (currency ~ '^([A-Z]{3})?$'),
    -- {"meta": "Purchase", "tiktok": "CompletePayment"}
    network_events    jsonb NOT NULL DEFAULT '{}',
    -- Whether attribution credits channels with it.
    attribution       boolean NOT NULL DEFAULT true,
    archived_at       timestamptz,
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, key)
);
CREATE INDEX conversion_definitions_event_idx ON conversion_definitions (workspace_id, event_name) WHERE archived_at IS NULL;

-- Which definition a credited conversion matched, if one did (an event in
-- the settings' conversion_events alone matches none).
ALTER TABLE attribution_conversions ADD COLUMN definition_id uuid REFERENCES conversion_definitions (id) ON DELETE SET NULL;
