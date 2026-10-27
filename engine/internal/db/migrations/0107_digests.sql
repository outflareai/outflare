-- Digests: a daily or weekly summary of the whole workspace (fixes found
-- and confirmed, deploys, site health, outreach, lifecycle email, missions
-- waiting for a person, connections that need attention), kept in the app
-- and sent as the digest.ready webhook. Email goes through the workspace's
-- own email provider, only when one is picked.

-- One row per workspace that changed the defaults: weekly on Mondays at
-- 08:00 UTC, kept in the app, no email.
CREATE TABLE digest_settings (
    workspace_id  uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    frequency     text NOT NULL DEFAULT 'weekly' CHECK (frequency IN ('off', 'daily', 'weekly')),
    -- 0 is Sunday, for weekly digests.
    weekday       smallint NOT NULL DEFAULT 1 CHECK (weekday BETWEEN 0 AND 6),
    hour          smallint NOT NULL DEFAULT 8 CHECK (hour BETWEEN 0 AND 23),
    timezone      text NOT NULL DEFAULT 'UTC',
    -- Who gets it by email: nobody, the owners and admins, or every member
    -- who sees every brand. Members can turn it off for themselves.
    recipients    text NOT NULL DEFAULT 'none' CHECK (recipients IN ('none', 'admins', 'members')),
    -- The workspace's own email provider and sender, set together.
    provider_id   uuid REFERENCES email_providers (id) ON DELETE SET NULL,
    from_address  text NOT NULL DEFAULT '',
    updated_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at    timestamptz NOT NULL DEFAULT now()
);

-- Members who turned the digest email off for themselves.
CREATE TABLE digest_optouts (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, user_id)
);

CREATE TABLE digests (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    frequency     text NOT NULL CHECK (frequency IN ('daily', 'weekly')),
    -- scheduled, or manual when someone asked for one.
    trigger       text NOT NULL DEFAULT 'scheduled' CHECK (trigger IN ('scheduled', 'manual')),
    period_start  timestamptz NOT NULL,
    period_end    timestamptz NOT NULL,
    -- The sections, as the API returns them.
    sections      jsonb NOT NULL DEFAULT '[]',
    -- Lifecycle emails queued for it (package emails tracks delivery).
    emailed       integer NOT NULL DEFAULT 0,
    email_error   text,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX digests_ws_idx ON digests (workspace_id, created_at DESC);
-- One scheduled digest per workspace and period.
CREATE UNIQUE INDEX digests_period_idx ON digests (workspace_id, frequency, period_end) WHERE trigger = 'scheduled';
