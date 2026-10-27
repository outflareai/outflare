-- Push: the supplier sends mailboxes into Outflare through the
-- EmailBison-compatible API, the way it does for sequencers. Outflare sets
-- the connection up at the supplier with an API token made for it.
ALTER TABLE vendor_syncs
    -- off, connecting (the job is adding the connection), on or failed.
    ADD COLUMN push_state        text NOT NULL DEFAULT 'off'
        CHECK (push_state IN ('off', 'connecting', 'on', 'failed')),
    -- The supplier's id for the connection.
    ADD COLUMN push_remote_id    text,
    -- The token the supplier uses. Revoked when push is turned off.
    ADD COLUMN push_token_id     uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    -- The token's secret, sealed with secrets.Box and bound to the
    -- integration id, only until the connect job has handed it over.
    ADD COLUMN push_token_sealed bytea,
    -- Fills the sequencer login the supplier asks for.
    ADD COLUMN push_login_email  text,
    ADD COLUMN push_error        text,
    ADD COLUMN push_connected_at timestamptz,
    ADD COLUMN push_last_at      timestamptz,
    -- Counts from the last push: {"synced", "failed", "skipped"}.
    ADD COLUMN push_last_result  jsonb;
