-- Meetings read from a member's own calendar, kept like their email
-- (ADR 15): private to the calendar's owner unless they share the
-- contact, or team sharing shows those the AI check found to be about
-- work. They used to go into meetings, which every colleague sees; those
-- rows and their timeline events are removed, and each calendar is read
-- again into this table.
CREATE TABLE crm_calendar_events (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    owner_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    record_id uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    event_key text NOT NULL,
    content_sealed bytea NOT NULL,
    starts_at timestamptz NOT NULL,
    ends_at timestamptz,
    canceled boolean NOT NULL DEFAULT false,
    privacy text NOT NULL DEFAULT 'unchecked' CHECK (privacy IN ('unchecked', 'work', 'private')),
    privacy_checked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, owner_id, event_key)
);
CREATE INDEX crm_calendar_events_record_idx ON crm_calendar_events (record_id, starts_at DESC);
CREATE INDEX crm_calendar_events_unchecked_idx ON crm_calendar_events (workspace_id, brand_id, created_at) WHERE privacy = 'unchecked';

WITH gone AS (
    DELETE FROM meetings WHERE source = 'google_calendar' AND external_id LIKE 'workspace:%' RETURNING id::text
)
DELETE FROM customer_events e USING gone WHERE e.source = 'meetings' AND e.properties->>'meeting_id' = gone.id;
UPDATE crm_google_connections SET calendar_cursor = NULL, calendar_synced_at = NULL WHERE workspace_integration_id IS NOT NULL;
