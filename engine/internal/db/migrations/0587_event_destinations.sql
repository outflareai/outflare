-- Destinations: a brand's customer events forwarded, as they arrive, to
-- the tools the brand already uses (its own webhook, Slack, Google
-- Analytics, HubSpot), each on the workspace's own connection or key.
-- Conversions for ad platforms stay in ad_conversion_rules.

CREATE TABLE event_destinations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- webhook, slack, ga4 or hubspot; the engine checks it, so a later
    -- kind needs no migration.
    kind            text NOT NULL CHECK (length(kind) BETWEEN 2 AND 31),
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    -- Catalog names of the events to send. Empty: every tracked event.
    events          text[] NOT NULL DEFAULT '{}',
    -- What the kind needs that is not a secret: a webhook's url, a Slack
    -- channel, a GA4 measurement id, the HubSpot properties to set.
    config          jsonb NOT NULL DEFAULT '{}',
    -- The workspace's own connection, for kinds that send through one.
    integration_id  uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- Sealed with the destination's id: a webhook's signing secret, a GA4
    -- Measurement Protocol API secret.
    secret          bytea,
    enabled         boolean NOT NULL DEFAULT true,
    -- Events received up to here have been looked at.
    matched_until   timestamptz NOT NULL DEFAULT now(),
    failing_since   timestamptz,
    last_error      text NOT NULL DEFAULT '' CHECK (length(last_error) <= 500),
    last_success_at timestamptz,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX event_destinations_brand_idx ON event_destinations (brand_id);
CREATE INDEX event_destinations_workspace_idx ON event_destinations (workspace_id);

-- One row per event per destination: what was sent, skipped or refused,
-- so a person can see exactly what reached each tool.
CREATE TABLE event_destination_sends (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    destination_id uuid NOT NULL REFERENCES event_destinations (id) ON DELETE CASCADE,
    event_row_id   uuid NOT NULL REFERENCES customer_events (id) ON DELETE CASCADE,
    event_name     text NOT NULL,
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'skipped', 'failed')),
    reason         text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    attempts       integer NOT NULL DEFAULT 0,
    sent_at        timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (destination_id, event_row_id)
);
CREATE INDEX event_destination_sends_queued_idx ON event_destination_sends (destination_id, created_at) WHERE status = 'queued';
CREATE INDEX event_destination_sends_recent_idx ON event_destination_sends (destination_id, created_at DESC);
CREATE INDEX event_destination_sends_workspace_idx ON event_destination_sends (workspace_id);
CREATE INDEX event_destination_sends_event_idx ON event_destination_sends (event_row_id);

-- Matching reads the events a brand received since the last look.
CREATE INDEX customer_events_brand_received_idx ON customer_events (brand_id, received_at);
