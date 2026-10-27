-- Browser events wait here between being received and being stored. The
-- collector (POST /v1/site/events) only checks the key and limits, writes
-- the batch as one row and answers, so a slow step in storing events (a
-- big catalog, a busy table, a deploy of the workers) never makes a site
-- lose events. Workers store each row's events in order, per workspace,
-- and delete the row in the same transaction. A row that keeps failing is
-- kept with its error (failed_at set) so it can be looked at and retried.
CREATE TABLE site_event_inbox (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    key_id       uuid NOT NULL REFERENCES site_keys (id) ON DELETE CASCADE,
    -- The browser's own user agent, read for device, browser and system.
    user_agent   text NOT NULL DEFAULT '' CHECK (length(user_agent) <= 1000),
    -- The events as the browser sent them (at most 20, under 64 KB).
    batch        jsonb NOT NULL,
    received_at  timestamptz NOT NULL DEFAULT now(),
    attempts     integer NOT NULL DEFAULT 0,
    last_error   text NOT NULL DEFAULT '',
    failed_at    timestamptz
);

CREATE INDEX site_event_inbox_pending ON site_event_inbox (workspace_id, id) WHERE failed_at IS NULL;
CREATE INDEX site_event_inbox_key ON site_event_inbox (key_id);
