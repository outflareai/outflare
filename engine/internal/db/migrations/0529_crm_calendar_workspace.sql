-- Where reading a Workspace member's calendar into the CRM got to: the
-- calendar's own sync cursor (not a secret) and when it last finished.
ALTER TABLE crm_google_connections
    ADD COLUMN calendar_cursor text,
    ADD COLUMN calendar_synced_at timestamptz;
