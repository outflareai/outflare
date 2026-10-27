-- Incoming webhooks: each connection that can tell Outflare when something
-- happens (a Geonode scrape finished) gets its own private address, and
-- every event that arrives is logged so it can be checked and replayed.

-- One row per connection with live updates.
CREATE TABLE connection_hooks (
    integration_id uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    connector      text NOT NULL,
    -- The address is /v1/hooks/<token>. The token is kept sealed so the
    -- address can be shown again, and looked up by its SHA-256.
    token          bytea NOT NULL, -- secrets.Box ciphertext, bound to the integration id
    token_hash     bytea NOT NULL UNIQUE,
    -- The connector's signing secrets as a sealed JSON array: one per
    -- subscription when it issues several. Null: the address is the lock.
    signing_secrets bytea,
    -- auto: Outflare registered the address through the connector's API.
    -- manual: the person pasted it there.
    setup          text NOT NULL DEFAULT 'manual' CHECK (setup IN ('auto', 'manual')),
    -- setting_up: registering at the connector. waiting: no event yet.
    -- receiving: events arrive. failed: registering failed, see last_error.
    status         text NOT NULL DEFAULT 'waiting' CHECK (status IN ('setting_up', 'waiting', 'receiving', 'failed')),
    last_error     text,
    -- The connector's ids for the subscriptions Outflare made.
    remote_ids     text[] NOT NULL DEFAULT '{}',
    last_event_at  timestamptz,
    rotated_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX connection_hooks_ws_idx ON connection_hooks (workspace_id);

-- Every event a connection's webhook delivered.
CREATE TABLE connection_hook_events (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    connector      text NOT NULL,
    -- The connector's own id for the event, so repeats are ignored.
    event_id       text NOT NULL,
    event_type     text NOT NULL,
    -- queued: waiting for a worker. handled: done. ignored: nothing in
    -- Outflare uses this event yet. failed: see error_code.
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'handled', 'ignored', 'failed')),
    error_code     text,
    -- Ids, counts and statuses the connector reported, for the log.
    summary        jsonb NOT NULL DEFAULT '{}',
    -- The delivery as received, sealed, for replays. Cleared after 30 days.
    payload        bytea,
    duplicates     integer NOT NULL DEFAULT 0,
    attempts       integer NOT NULL DEFAULT 0,
    received_at    timestamptz NOT NULL DEFAULT now(),
    handled_at     timestamptz,
    UNIQUE (integration_id, event_id)
);

CREATE INDEX connection_hook_events_ws_idx ON connection_hook_events (workspace_id, received_at DESC);
CREATE INDEX connection_hook_events_integration_idx ON connection_hook_events (integration_id, received_at DESC);
