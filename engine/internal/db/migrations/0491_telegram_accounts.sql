-- Company Telegram accounts signed in to Outflare as a linked device, the
-- way Telegram Desktop is: someone scans a QR code on their phone, once.
-- One row per Telegram account in the team directory (staff_accounts of
-- kind telegram). The session that keeps it signed in is sealed with the
-- server key, bound to the account id, and is only used to read.
--
-- The Telegram app (api_id and api_hash from my.telegram.org) is the
-- workspace's own connection (connector telegram), like any other key.

CREATE TABLE telegram_sessions (
    account_id        uuid PRIMARY KEY REFERENCES staff_accounts (id) ON DELETE CASCADE,
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The workspace's Telegram app connection it signed in with.
    integration_id    uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- starting: a sign-in was asked for; waiting_scan: a QR code is shown;
    -- needs_password: scanned, and the account's two-step password is
    -- needed; checking_password: it is being checked; connected: signed in;
    -- expired: nobody scanned in time; disconnecting: being signed out;
    -- signed_out: Telegram ended the session (or it was signed out);
    -- failed: something broke (error_code says what).
    status            text NOT NULL DEFAULT 'starting' CHECK (status IN (
        'starting', 'waiting_scan', 'needs_password', 'checking_password', 'connected',
        'expired', 'disconnecting', 'signed_out', 'failed')),
    -- Bumped on every new sign-in, so a job for an older one stops.
    attempt           integer NOT NULL DEFAULT 1,
    -- The tg://login link the QR code shows, while waiting for a scan.
    login_url         text,
    login_expires_at  timestamptz,
    password_hint     text NOT NULL DEFAULT '',
    session_sealed    bytea,
    -- The two-step password, sealed, only until the job has checked it.
    password_sealed   bytea,
    -- Who it is signed in as, read from Telegram.
    telegram_user_id  bigint,
    telegram_username text,
    telegram_phone    text,
    telegram_name     text,
    -- Read from Telegram on each check: whether a two-step password and a
    -- login email are set, and how many other sessions are signed in.
    two_step          boolean,
    login_email       boolean,
    other_sessions    integer,
    -- Lock-down steps people tick by hand ({"step": true}).
    checklist         jsonb NOT NULL DEFAULT '{}',
    error_code        text,
    connected_at      timestamptz,
    last_checked_at   timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX telegram_sessions_workspace_idx ON telegram_sessions (workspace_id);
CREATE INDEX telegram_sessions_check_idx ON telegram_sessions (last_checked_at) WHERE status = 'connected';
