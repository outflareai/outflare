-- Mailbox suppliers (Cheap Inboxes and others like it): how each connected
-- supplier account is kept in sync, and the mailboxes it holds.

-- One row per supplier integration that has been synced.
CREATE TABLE vendor_syncs (
    integration_id  uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Import every active mailbox without being asked, including new ones.
    auto_import     boolean NOT NULL DEFAULT false,
    -- Ask the supplier for a Google app password before importing a
    -- Google mailbox; Google refuses plain passwords over SMTP and IMAP.
    app_passwords   boolean NOT NULL DEFAULT true,
    -- Daily send cap for imported mailboxes. Null: the supplier's cap, or
    -- the engine default.
    daily_limit     integer CHECK (daily_limit BETWEEN 1 AND 500),
    -- The supplier's webhook subscription, when the engine has a public URL.
    webhook_id      text,
    webhook_secret  bytea, -- secrets.Box ciphertext, bound to the integration id
    last_attempt_at timestamptz,
    last_synced_at  timestamptz,
    last_sync_error text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX vendor_syncs_ws_idx ON vendor_syncs (workspace_id);

-- Every mailbox seen at a supplier, imported or not.
CREATE TABLE vendor_mailboxes (
    integration_id  uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    external_id     text NOT NULL,
    email           text NOT NULL,
    first_name      text NOT NULL DEFAULT '',
    last_name       text NOT NULL DEFAULT '',
    provider        text NOT NULL CHECK (provider IN ('google', 'microsoft', 'other')),
    -- Supplier-neutral: provisioning, active, cancelling, cancelled, failed.
    status          text NOT NULL,
    vendor_status   text NOT NULL DEFAULT '',
    daily_limit     integer,
    tags            text[] NOT NULL DEFAULT '{}',
    -- Other sending platforms the supplier says it is connected to.
    connected_to    text[] NOT NULL DEFAULT '{}',
    -- available: not imported. queued: import pending. waiting: will import
    -- when the supplier finishes setting it up. app_password: waiting for
    -- the supplier to create a Google app password. imported. failed: see
    -- import_error.
    import_state    text NOT NULL DEFAULT 'available'
                    CHECK (import_state IN ('available', 'queued', 'waiting', 'app_password', 'imported', 'failed')),
    import_error    text,
    mailbox_id      uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    app_password_requested_at timestamptz,
    imported_at     timestamptz,
    seen_at         timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, external_id)
);

CREATE INDEX vendor_mailboxes_ws_idx ON vendor_mailboxes (workspace_id, integration_id, import_state);
CREATE INDEX vendor_mailboxes_mailbox_idx ON vendor_mailboxes (mailbox_id) WHERE mailbox_id IS NOT NULL;

-- Webhook deliveries already handled; suppliers deliver at least once.
CREATE TABLE vendor_webhook_events (
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    event_id       text NOT NULL,
    received_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (integration_id, event_id)
);
