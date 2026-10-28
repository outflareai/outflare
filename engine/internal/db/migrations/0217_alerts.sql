-- Alerts: one place where any module tells people something needs them
-- now (a hot reply, a hot website visitor, a negative review), shown in
-- the app, emailed to the person it is for and sent as the alert.raised
-- webhook.

-- Where each kind of alert goes. email: the person it is for (owner),
-- every owner and admin (admins), or nobody (off); null follows the
-- workspace (for a brand) or the default. slack: a channel in one of the
-- workspace's own Slack connections. A brand can choose differently from
-- the workspace.
CREATE TABLE alert_settings (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id             uuid REFERENCES brands (id) ON DELETE CASCADE,
    kind                 text NOT NULL,
    email                text CHECK (email IN ('owner', 'admins', 'off')),
    slack_integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    slack_channel        text,
    slack_channel_name   text,
    updated_by           uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX alert_settings_scope_idx ON alert_settings
    (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), kind);

-- One row per alert. Names and text are read from the target when shown,
-- never copied here.
CREATE TABLE alerts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    kind         text NOT NULL,
    -- The same thing raises one alert: a reply's id, a record and week.
    dedupe_key   text NOT NULL,
    target_type  text NOT NULL,
    target_id    uuid NOT NULL,
    record_id    uuid REFERENCES records (id) ON DELETE SET NULL,
    -- The person it is for, when there is one.
    owner_id     uuid REFERENCES users (id) ON DELETE SET NULL,
    data         jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX alerts_dedupe_idx ON alerts (workspace_id, kind, dedupe_key);
CREATE INDEX alerts_workspace_idx ON alerts (workspace_id, created_at DESC);

-- What happened to each alert on each channel: email, for each person,
-- or a chat channel.
CREATE TABLE alert_deliveries (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    alert_id       uuid NOT NULL REFERENCES alerts (id) ON DELETE CASCADE,
    channel        text NOT NULL CHECK (channel IN ('email', 'slack')),
    user_id        uuid REFERENCES users (id) ON DELETE CASCADE,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The chat channel posted to.
    target         text,
    status         text NOT NULL CHECK (status IN ('queued', 'sent', 'skipped', 'failed')),
    reason         text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    sent_at        timestamptz
);

CREATE UNIQUE INDEX alert_deliveries_once_idx ON alert_deliveries
    (alert_id, channel, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX alert_deliveries_user_idx ON alert_deliveries (user_id, sent_at) WHERE status = 'sent';

-- A person who doesn't want a kind of alert by email in a workspace.
CREATE TABLE alert_mutes (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    kind         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id, kind)
);
