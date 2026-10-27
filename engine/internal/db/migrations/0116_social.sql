-- Social: one set of tables for every social channel (Reddit first, then X
-- and LinkedIn), so watches, what they find and the queue of actions a
-- person approves are the same whatever the network. The channel column
-- says which network a row belongs to.
--
-- Actions only ever run on the brand's own accounts, through the
-- workspace's own connection, after the approval its type needs. There is
-- no posting from accounts the brand does not own, no bought votes and no
-- reply that hides who wrote it. Visits and sign-ups that come from an
-- action's link are read from customer events by its UTM content tag, so
-- results land on the same customer records as every other channel.

-- A watch: what to look for on one channel for one brand, and how replies
-- to what it finds are written.
CREATE TABLE social_watches (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id             uuid REFERENCES brands (id) ON DELETE SET NULL,
    channel              text NOT NULL CHECK (channel IN ('reddit', 'x', 'linkedin')),
    name                 text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- An item matches when its title or text has any keyword and none of
    -- the excluded ones. Communities (subreddits on Reddit) narrow the
    -- search; empty searches the whole channel.
    keywords             text[] NOT NULL CHECK (cardinality(keywords) BETWEEN 1 AND 50),
    exclude_keywords     text[] NOT NULL DEFAULT '{}',
    communities          text[] NOT NULL DEFAULT '{}',
    -- The workspace's connection for the channel: it searches, and acts
    -- once it is signed in to the brand's account.
    integration_id       uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- Where links in replies go; each action adds its own UTM tags.
    landing_url          text NOT NULL DEFAULT '',
    -- Said in every reply, such as "Disclosure: I work at Acme."
    disclosure           text NOT NULL CHECK (length(disclosure) BETWEEN 3 AND 300),
    -- What a reply should know and how it should sound.
    guidance             text NOT NULL DEFAULT '',
    -- Customer event names that count as a conversion, such as signed_up.
    conversion_events    text[] NOT NULL DEFAULT '{signed_up}',
    -- Draft a reply with the workspace's own AI for each new item.
    auto_draft           boolean NOT NULL DEFAULT false,
    -- The most actions the watch may run in a day.
    daily_limit          integer NOT NULL DEFAULT 5 CHECK (daily_limit BETWEEN 1 AND 50),
    status               text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    check_every_minutes  integer NOT NULL DEFAULT 60 CHECK (check_every_minutes BETWEEN 15 AND 1440),
    last_checked_at      timestamptz,
    next_check_at        timestamptz NOT NULL DEFAULT now(),
    last_error           text,
    created_by           uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX social_watches_ws ON social_watches (workspace_id, created_at DESC);
CREATE INDEX social_watches_due ON social_watches (next_check_at) WHERE status = 'active';

-- A post or mention a watch found.
CREATE TABLE social_items (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid REFERENCES brands (id) ON DELETE SET NULL,
    channel          text NOT NULL CHECK (channel IN ('reddit', 'x', 'linkedin')),
    watch_id         uuid NOT NULL REFERENCES social_watches (id) ON DELETE CASCADE,
    -- The channel's own id (on Reddit the fullname, t3_ and the post id).
    external_id      text NOT NULL,
    -- Where it was posted: the subreddit on Reddit.
    community        text NOT NULL DEFAULT '',
    title            text NOT NULL DEFAULT '',
    body             text NOT NULL DEFAULT '',
    author           text NOT NULL DEFAULT '',
    url              text NOT NULL,
    score            integer NOT NULL DEFAULT 0,
    comments         integer NOT NULL DEFAULT 0,
    posted_at        timestamptz NOT NULL,
    matched_keywords text[] NOT NULL DEFAULT '{}',
    -- new: waiting for someone. drafted: an action is being written or
    -- approved. handled: an action ran. ignored: not worth acting on.
    status           text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'drafted', 'handled', 'ignored')),
    -- The channel closed it to replies (archived or locked).
    closed           boolean NOT NULL DEFAULT false,
    found_at         timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, watch_id, external_id)
);
CREATE INDEX social_items_ws ON social_items (workspace_id, status, posted_at DESC);
CREATE INDEX social_items_watch ON social_items (watch_id, posted_at DESC);

-- The approval queue: something to do on a channel from the brand's own
-- account, drafted by a person or the workspace's AI, approved by a
-- person, then run by a job. Reddit has replies.
CREATE TABLE social_actions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid REFERENCES brands (id) ON DELETE SET NULL,
    channel           text NOT NULL CHECK (channel IN ('reddit', 'x', 'linkedin')),
    type              text NOT NULL CHECK (type IN ('reply', 'comment', 'like', 'repost', 'quote', 'follow', 'connect', 'dm_reply')),
    item_id           uuid REFERENCES social_items (id) ON DELETE CASCADE,
    watch_id          uuid REFERENCES social_watches (id) ON DELETE CASCADE,
    -- The connection it runs on, set when it is approved.
    integration_id    uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The brand's page or profile that acts, once accounts are tracked
    -- (social_accounts), and what runs it: official (the channel's own
    -- API) or a provider's id.
    social_account_id uuid,
    provider          text,
    -- The text posted, which always holds the disclosure.
    body              text NOT NULL DEFAULT '',
    disclosure        text NOT NULL DEFAULT '',
    -- The link it may carry, with its UTM tags; utm_content is its own tag,
    -- which customer events are matched on.
    link_url          text NOT NULL DEFAULT '',
    utm_content       text NOT NULL,
    -- drafting: the AI is writing it. draft: waiting for a person.
    -- approved: queued to run. done: it ran. rejected: a person said no.
    -- failed: the channel refused, or the AI could not write it.
    status            text NOT NULL DEFAULT 'draft'
                          CHECK (status IN ('drafting', 'draft', 'approved', 'done', 'rejected', 'failed')),
    status_reason     text,
    -- person or ai.
    drafted_via       text NOT NULL DEFAULT 'person' CHECK (drafted_via IN ('person', 'ai')),
    -- The AI's view of how well the brand can help, 0 to 1, and why.
    relevance         real,
    relevance_reason  text,
    mission_id        uuid REFERENCES missions (id) ON DELETE SET NULL,
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at       timestamptz,
    done_at           timestamptz,
    -- What it made on the channel (on Reddit the comment, t1_ and its id),
    -- with its link, score and whether a moderator took it down.
    external_id       text,
    external_url      text,
    score             integer,
    removed           boolean NOT NULL DEFAULT false,
    synced_at         timestamptz,
    request_id        text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, utm_content)
);
CREATE INDEX social_actions_ws ON social_actions (workspace_id, status, created_at DESC);
CREATE INDEX social_actions_item ON social_actions (item_id);
CREATE INDEX social_actions_done ON social_actions (watch_id, done_at DESC) WHERE done_at IS NOT NULL;
-- One reply per item goes out: never a second one from the brand.
CREATE UNIQUE INDEX social_actions_one_reply ON social_actions (item_id) WHERE type = 'reply' AND status IN ('approved', 'done');

-- Customer events carry UTM tags in context.campaign (as Segment sends
-- them) or in properties; channels read their results by the content tag.
CREATE INDEX customer_events_utm_content ON customer_events
    (workspace_id, (COALESCE(context->'campaign'->>'content', properties->>'utm_content')))
    WHERE COALESCE(context->'campaign'->>'content', properties->>'utm_content') IS NOT NULL;
