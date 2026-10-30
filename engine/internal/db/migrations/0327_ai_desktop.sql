-- Outflare for desktop (plans/ai-accounts, step 4): the tray app each
-- person installs reads usage from the AI tools on their computer and
-- reports only numbers.

-- Linking a computer: the app asks for a link and shows its user code; the
-- person approves the code in Outflare (a signed-in workspace member), and
-- the app's next poll gets its device token. Before approval a link belongs
-- to no workspace. Links expire after 15 minutes.
CREATE TABLE ai_device_links (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    device_code_hash bytea NOT NULL UNIQUE,
    user_code text NOT NULL UNIQUE CHECK (user_code ~ '^[A-Z0-9]{4}-[A-Z0-9]{4}$'),
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    platform text NOT NULL CHECK (platform IN ('macos', 'windows', 'linux')),
    app_version text NOT NULL CHECK (length(app_version) <= 40),
    workspace_id uuid REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id uuid REFERENCES users (id) ON DELETE CASCADE,
    approved_at timestamptz,
    claimed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX ai_device_links_expires_idx ON ai_device_links (expires_at);

-- A linked computer: one person's, in one workspace, with a revocable
-- token (ofd_, only its hash kept).
CREATE TABLE ai_devices (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    name text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    platform text NOT NULL CHECK (platform IN ('macos', 'windows', 'linux')),
    app_version text NOT NULL CHECK (length(app_version) <= 40),
    -- What the app said about itself in its last report: ok, attention
    -- (something the person can fix there) or broken.
    status text NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'attention', 'broken')),
    last_error_code text CHECK (length(last_error_code) <= 60),
    last_report_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    unlinked_at timestamptz
);
CREATE INDEX ai_devices_workspace_idx ON ai_devices (workspace_id, user_id) WHERE unlinked_at IS NULL;

-- Usage read from a tool on a linked computer: one row per limit window
-- (five_hour, weekly, weekly_opus, monthly, premium_requests, credits...),
-- how much of it is used and when it resets. Kept 90 days.
CREATE TABLE ai_usage_readings (
    id bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES ai_accounts (id) ON DELETE CASCADE,
    device_id uuid REFERENCES ai_devices (id) ON DELETE SET NULL,
    window_kind text NOT NULL CHECK (window_kind ~ '^[a-z][a-z0-9_]{1,39}$'),
    used_percent numeric(6, 2) NOT NULL CHECK (used_percent BETWEEN 0 AND 1000),
    resets_at timestamptz,
    -- official: the tool's own documented way; extended: the endpoints the
    -- tool's own app calls, with the login already on the computer.
    source text NOT NULL CHECK (source IN ('official', 'extended')),
    observed_at timestamptz NOT NULL
);
CREATE INDEX ai_usage_readings_latest_idx ON ai_usage_readings (account_id, window_kind, observed_at DESC);
CREATE INDEX ai_usage_readings_observed_idx ON ai_usage_readings (observed_at);

-- A source the operator has switched off for every computer (a provider
-- pushed back, or a reader broke): GET /v1/desktop/config tells the apps
-- within minutes, without a release. The same for every workspace.
CREATE TABLE ai_desktop_sources_off (
    provider text NOT NULL,
    source text NOT NULL CHECK (source IN ('official', 'extended')),
    reason text NOT NULL DEFAULT '' CHECK (length(reason) <= 200),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, source)
);
