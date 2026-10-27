-- Domains: every sending domain in the workspace gets a row, with settings
-- that apply to all of its mailboxes together.

ALTER TABLE sending_domains
    -- Null until the first DNS check has run.
    ALTER COLUMN checked_at DROP NOT NULL,
    ALTER COLUMN checked_at DROP DEFAULT,
    -- paused: no sequence or test sends from any mailbox on the domain.
    ADD COLUMN status       text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    -- Most sends from all of the domain's mailboxes together in 24 hours.
    -- Null leaves only the per-mailbox limits.
    ADD COLUMN daily_limit  integer CHECK (daily_limit BETWEEN 1 AND 100000),
    ADD COLUMN notes        text NOT NULL DEFAULT '',
    -- mailbox: added when a mailbox on it was connected. manual: added by
    -- hand, for example to check DNS before buying inboxes.
    ADD COLUMN source       text NOT NULL DEFAULT 'mailbox' CHECK (source IN ('mailbox', 'manual')),
    ADD COLUMN mx_hosts     text[] NOT NULL DEFAULT '{}',
    ADD COLUMN dmarc_policy text, -- none, quarantine, reject
    -- [{name, zone, status: clear|listed|unknown, detail}]
    ADD COLUMN blocklists   jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN created_at   timestamptz NOT NULL DEFAULT now(),
    ADD COLUMN updated_at   timestamptz NOT NULL DEFAULT now();

-- Domains whose mailboxes were connected before their first check finished.
INSERT INTO sending_domains (workspace_id, domain, spf, dkim, dmarc, checked_at)
SELECT DISTINCT workspace_id, domain, 'pending', 'pending', 'pending', NULL::timestamptz FROM mailboxes
ON CONFLICT (workspace_id, domain) DO NOTHING;

-- Check every domain again now, which also starts the daily re-checks.
INSERT INTO jobs (workspace_id, kind, payload, max_attempts)
SELECT workspace_id, 'domain.check', jsonb_build_object('domain', domain), 3 FROM sending_domains;

-- Per-domain send counts read messages by sender domain.
CREATE INDEX messages_ws_from_domain_idx ON messages (workspace_id, lower(split_part(from_email, '@', 2)), created_at)
    WHERE status = 'sent';
