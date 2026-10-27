-- Sending mailboxes, grouped into pools.

CREATE TABLE mailbox_pools (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    name         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX mailbox_pools_name_idx ON mailbox_pools (workspace_id, lower(name));

CREATE TABLE mailboxes (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    pool_id          uuid REFERENCES mailbox_pools (id) ON DELETE SET NULL,
    email            text NOT NULL,
    domain           text NOT NULL GENERATED ALWAYS AS (lower(split_part(email, '@', 2))) STORED,
    display_name     text NOT NULL DEFAULT '',
    provider         text NOT NULL CHECK (provider IN ('google', 'microsoft', 'other')),
    -- How Outflare authenticates: app passwords over SMTP/IMAP now, OAuth later.
    auth_method      text NOT NULL DEFAULT 'password' CHECK (auth_method IN ('password', 'oauth')),
    smtp_host        text NOT NULL,
    smtp_port        integer NOT NULL,
    smtp_username    text NOT NULL,
    imap_host        text NOT NULL,
    imap_port        integer NOT NULL,
    imap_username    text NOT NULL,
    credentials      bytea NOT NULL, -- secrets.Box ciphertext, bound to the mailbox id
    status           text NOT NULL DEFAULT 'verifying'
                     CHECK (status IN ('verifying', 'active', 'paused', 'error')),
    status_reason    text,
    daily_limit      integer NOT NULL DEFAULT 30 CHECK (daily_limit BETWEEN 1 AND 500),
    smtp_ok          boolean,
    imap_ok          boolean,
    last_verified_at timestamptz,
    source           text NOT NULL DEFAULT 'csv', -- csv, api, cheapinboxes, ...
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX mailboxes_email_idx ON mailboxes (workspace_id, lower(email));
CREATE INDEX mailboxes_pool_idx ON mailboxes (pool_id);
CREATE INDEX mailboxes_domain_idx ON mailboxes (workspace_id, domain);

-- Authentication records for each sending domain, refreshed by a job.
CREATE TABLE sending_domains (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    domain       text NOT NULL,
    mx_provider  text,           -- google, microsoft, other, none
    spf          text NOT NULL,  -- pass, missing, invalid
    spf_record   text,
    dkim         text NOT NULL,  -- pass, missing
    dkim_selector text,
    dmarc        text NOT NULL,  -- pass, missing, none_policy
    dmarc_record text,
    checked_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, domain)
);
