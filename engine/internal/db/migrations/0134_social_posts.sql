-- The brand's own posts on each social account, and how each did over
-- time. Posts are read from the channel every day; posts Outflare
-- publishes (later) are rows here too, so there is one list either way.

CREATE TABLE social_posts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid REFERENCES brands (id) ON DELETE SET NULL,
    account_id     uuid NOT NULL REFERENCES social_accounts (id) ON DELETE CASCADE,
    channel        text NOT NULL CHECK (channel IN ('reddit', 'x', 'linkedin')),
    -- The channel's own id: an X post id, a LinkedIn share or ugcPost URN.
    external_id    text NOT NULL,
    url            text NOT NULL DEFAULT '',
    body           text NOT NULL DEFAULT '',
    posted_at      timestamptz NOT NULL,
    -- The latest counts. NULL: the channel does not report it.
    impressions    bigint,
    likes          bigint,
    replies        bigint,
    reposts        bigint,
    quotes         bigint,
    bookmarks      bigint,
    clicks         bigint,
    metrics_at     timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (account_id, external_id)
);
CREATE INDEX social_posts_ws ON social_posts (workspace_id, posted_at DESC);
CREATE INDEX social_posts_account ON social_posts (account_id, posted_at DESC);

-- A post's counts each time they were read, so posts can be compared at
-- the same age (a day, a week) and growth shows day by day.
CREATE TABLE social_post_snapshots (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    post_id      uuid NOT NULL REFERENCES social_posts (id) ON DELETE CASCADE,
    taken_at     timestamptz NOT NULL DEFAULT now(),
    impressions  bigint,
    likes        bigint,
    replies      bigint,
    reposts      bigint,
    quotes       bigint,
    bookmarks    bigint,
    clicks       bigint,
    PRIMARY KEY (post_id, taken_at)
);
CREATE INDEX social_post_snapshots_ws ON social_post_snapshots (workspace_id, taken_at);
