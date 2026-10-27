-- Social accounts: the brand's own pages and profiles on a channel (an X
-- account, a LinkedIn company page, a person's own LinkedIn profile),
-- found through the workspace's own connection, with their followers and
-- posts read every day. They sit beside the shared watches, items and
-- actions (0116), so actions say which page or profile acts.

CREATE TABLE social_accounts (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    channel          text NOT NULL CHECK (channel IN ('reddit', 'x', 'linkedin')),
    -- profile: an X account or a person's own LinkedIn profile.
    -- page: a company page.
    kind             text NOT NULL CHECK (kind IN ('profile', 'page')),
    -- The connection that reaches it. A LinkedIn sign-in reaches the
    -- person's profile and every page they administer.
    integration_id   uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The channel's own id: an X user id, a LinkedIn URN.
    external_id      text NOT NULL,
    handle           text NOT NULL DEFAULT '',
    name             text NOT NULL DEFAULT '',
    url              text NOT NULL DEFAULT '',
    avatar_url       text NOT NULL DEFAULT '',
    -- active: read every day. paused: kept, not read.
    -- disconnected: its connection no longer reaches it.
    status           text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'disconnected')),
    -- The latest counts, also kept day by day in social_account_days.
    followers        bigint,
    following        bigint,
    posts            bigint,
    last_synced_at   timestamptz,
    next_sync_at     timestamptz NOT NULL DEFAULT now(),
    last_error       text,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, channel, external_id)
);
CREATE INDEX social_accounts_ws ON social_accounts (workspace_id, channel, created_at);
CREATE INDEX social_accounts_due ON social_accounts (next_sync_at) WHERE status = 'active';
CREATE INDEX social_accounts_integration ON social_accounts (integration_id);

-- One row per account per day: the counts as last read that day. A count
-- the channel does not report is NULL, never zero.
CREATE TABLE social_account_days (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    account_id   uuid NOT NULL REFERENCES social_accounts (id) ON DELETE CASCADE,
    day          date NOT NULL,
    followers    bigint,
    following    bigint,
    posts        bigint,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (account_id, day)
);
CREATE INDEX social_account_days_ws ON social_account_days (workspace_id, day);

-- The shared approval queue (0116) points at the page or profile that acts.
ALTER TABLE social_actions
    ADD CONSTRAINT social_actions_account_fk FOREIGN KEY (social_account_id) REFERENCES social_accounts (id) ON DELETE SET NULL;
