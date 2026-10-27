-- What sent each lifecycle email, and whether people opened it or clicked
-- its links.
ALTER TABLE emails
    -- What asked for it: api (a call to POST /emails from the API, the web
    -- app, MCP, the CLI or a mission), test (a test send), and in time
    -- event, journey and broadcast.
    ADD COLUMN trigger_kind text NOT NULL DEFAULT 'api'
        CHECK (trigger_kind IN ('api', 'test', 'event', 'journey', 'broadcast', 'system')),
    -- The event, journey step or broadcast behind it.
    ADD COLUMN trigger_ref  text,
    -- The API call that queued it (its row on the Logs page), where the call
    -- came from (web, api, mcp, cli, sdk or mission), and the API token or
    -- mission it ran as.
    ADD COLUMN request_id   text,
    ADD COLUMN surface      text,
    ADD COLUMN token_id     uuid,
    ADD COLUMN mission_id   uuid,
    -- Off unless the template or the send turns them on.
    ADD COLUMN track_opens  boolean NOT NULL DEFAULT false,
    ADD COLUMN track_clicks boolean NOT NULL DEFAULT false,
    ADD COLUMN opens        integer NOT NULL DEFAULT 0,
    ADD COLUMN clicks       integer NOT NULL DEFAULT 0,
    ADD COLUMN opened_at    timestamptz,
    ADD COLUMN clicked_at   timestamptz,
    -- Every recipient (to, cc and bcc) in lowercase, to find all the email
    -- one person was sent.
    ADD COLUMN recipients   text[] NOT NULL DEFAULT '{}';

UPDATE emails SET recipients = ARRAY(SELECT DISTINCT lower(x) FROM unnest(to_addresses || cc || bcc) x);
UPDATE emails SET trigger_kind = 'test' WHERE test;

CREATE INDEX emails_recipients ON emails USING gin (recipients);
CREATE INDEX emails_request ON emails (workspace_id, request_id) WHERE request_id IS NOT NULL;

ALTER TABLE email_templates
    ADD COLUMN track_opens  boolean NOT NULL DEFAULT false,
    ADD COLUMN track_clicks boolean NOT NULL DEFAULT false;

-- Click links in lifecycle email share tracked_links and /c/ with sequence
-- email: a row belongs to an enrollment step or to one email.
ALTER TABLE tracked_links
    ALTER COLUMN enrollment_id DROP NOT NULL,
    ADD COLUMN email_id uuid REFERENCES emails (id) ON DELETE CASCADE;
CREATE UNIQUE INDEX tracked_links_email_url ON tracked_links (email_id, url) WHERE email_id IS NOT NULL;
