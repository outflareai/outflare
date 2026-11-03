-- Accounts (plans/brand-agent, "Accounts"): the external accounts a brand's
-- agent may act through (its social profiles, community and review site
-- accounts, partner platforms, sites with no API), how each signs in,
-- whether access works, and what depends on what. Tracking is the tools'
-- job: every managed sign-in, verification and session use records an
-- attempt and its evidenced outcome, which keeps the account's health and
-- opens or resolves incidents. Secrets live apart, sealed with the
-- server's key, and are never returned.
CREATE TABLE brand_accounts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The platform (a site or service id, such as g2, producthunt, x) and
    -- what the account is called there.
    platform         text NOT NULL CHECK (length(platform) BETWEEN 1 AND 100),
    label            text NOT NULL CHECK (length(label) BETWEEN 1 AND 200),
    -- The identity it signs in as (a username or an email address of the
    -- brand's own; shown to the team, never to a model).
    identity         text NOT NULL DEFAULT '' CHECK (length(identity) <= 320),
    -- How it signs in: local_password, social_sign_in (through the identity
    -- in depends_on), api_oauth (through the integration), browser_session
    -- or mailbox_verification (through the mailbox).
    sign_in          text NOT NULL CHECK (sign_in IN ('local_password', 'social_sign_in', 'api_oauth', 'browser_session', 'mailbox_verification')),
    -- The upstream identity it depends on (the brand's Google account, a
    -- mailbox), so one upstream failure is one incident.
    depends_on       uuid REFERENCES brand_accounts (id) ON DELETE SET NULL,
    integration_id   uuid REFERENCES integrations (id) ON DELETE SET NULL,
    mailbox_id       uuid REFERENCES mailboxes (id) ON DELETE SET NULL,
    url              text NOT NULL DEFAULT '' CHECK (length(url) <= 2000),
    -- unverified: recorded, never used; healthy: the last use worked;
    -- needs_attention: an open incident; blocked: a person stopped its use;
    -- archived: no longer the brand's.
    status           text NOT NULL DEFAULT 'unverified' CHECK (status IN ('unverified', 'healthy', 'needs_attention', 'blocked', 'archived')),
    owner_id         uuid REFERENCES users (id) ON DELETE SET NULL,
    notes            text NOT NULL DEFAULT '' CHECK (length(notes) <= 5000),
    last_verified_at timestamptz,
    last_attempt_at  timestamptz,
    created_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX brand_accounts_brand_idx ON brand_accounts (brand_id, status, platform);
CREATE INDEX brand_accounts_depends_idx ON brand_accounts (depends_on) WHERE depends_on IS NOT NULL;

-- The vault: one sealed value per kind per account (password, totp_seed,
-- recovery, session), opened only for an authorised use, never over the
-- API.
CREATE TABLE brand_account_secrets (
    account_id   uuid NOT NULL REFERENCES brand_accounts (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('password', 'totp_seed', 'recovery', 'session')),
    sealed       bytea NOT NULL,
    set_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    set_at       timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, kind)
);

-- Attempts: every managed sign-in, verification and session use, with its
-- outcome and evidence (never a secret or a code).
CREATE TABLE brand_account_attempts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    account_id   uuid NOT NULL REFERENCES brand_accounts (id) ON DELETE CASCADE,
    -- verify (a check), sign_in, session_use, oauth_refresh, verification_email.
    purpose      text NOT NULL CHECK (purpose IN ('verify', 'sign_in', 'session_use', 'oauth_refresh', 'verification_email')),
    -- authenticated, verification_pending, mfa_required, wrong_identity,
    -- credential_failed, session_expired, locked, rate_limited, unresolved.
    outcome      text NOT NULL CHECK (outcome IN ('authenticated', 'verification_pending', 'mfa_required', 'wrong_identity', 'credential_failed', 'session_expired', 'locked', 'rate_limited', 'unresolved')),
    -- Who or what made it: a token id, a user id or a job name.
    actor_type   text NOT NULL DEFAULT 'system' CHECK (actor_type IN ('user', 'token', 'system')),
    actor_id     text NOT NULL DEFAULT '' CHECK (length(actor_id) <= 100),
    evidence     jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX brand_account_attempts_account_idx ON brand_account_attempts (account_id, created_at DESC);

-- Incidents: what stops an account from being used, with an owner, a
-- severity, how often it repeated and when to try again. One open incident
-- per account and kind; an upstream failure opens one on the identity and
-- links every account that depends on it through root_id.
CREATE TABLE brand_account_incidents (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    account_id    uuid NOT NULL REFERENCES brand_accounts (id) ON DELETE CASCADE,
    root_id       uuid REFERENCES brand_account_incidents (id) ON DELETE SET NULL,
    kind          text NOT NULL CHECK (kind IN ('credential', 'session', 'mfa', 'verification', 'wrong_identity', 'locked', 'rate_limit', 'upstream')),
    severity      text NOT NULL DEFAULT 'warning' CHECK (severity IN ('info', 'warning', 'critical')),
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'snoozed', 'resolved')),
    owner_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    repeats       integer NOT NULL DEFAULT 1,
    next_retry_at timestamptz,
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    opened_at     timestamptz NOT NULL DEFAULT now(),
    resolved_at   timestamptz,
    resolution    text NOT NULL DEFAULT '' CHECK (length(resolution) <= 1000)
);

CREATE UNIQUE INDEX brand_account_incidents_open_idx ON brand_account_incidents (account_id, kind) WHERE status <> 'resolved';
CREATE INDEX brand_account_incidents_brand_idx ON brand_account_incidents (brand_id, status, opened_at DESC);
