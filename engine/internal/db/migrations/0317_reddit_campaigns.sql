-- Reddit campaigns: comments, replies, posts, threads and upvotes placed
-- through a managed account network (Engain) with the workspace's own key,
-- run by people or agents within a per-brand budget. Every action is a
-- row; a poller reads Engain's status and what Reddit shows a logged-out
-- visitor, so each action's lifespan is known. Experiments group actions
-- under one hypothesis so results can be compared.

-- Per-brand settings: which connection and Engain project, and the limits
-- checked before anything spends credits.
CREATE TABLE reddit_campaign_settings (
    workspace_id                 uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id                     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    integration_id               uuid REFERENCES integrations (id) ON DELETE SET NULL,
    engain_project_id            text NOT NULL DEFAULT '',
    -- Credits a month and a day; NULL means no limit.
    monthly_credit_limit         numeric(12, 2) CHECK (monthly_credit_limit >= 0),
    daily_credit_limit           numeric(12, 2) CHECK (daily_credit_limit >= 0),
    max_actions_per_subreddit_day integer NOT NULL DEFAULT 2 CHECK (max_actions_per_subreddit_day BETWEEN 1 AND 100),
    max_upvotes_per_item         integer NOT NULL DEFAULT 50 CHECK (max_upvotes_per_item BETWEEN 0 AND 2000),
    -- A person approves each action before it is sent.
    require_approval             boolean NOT NULL DEFAULT false,
    -- What one credit costs, for showing spend in money.
    credit_price_cents           numeric(10, 4) NOT NULL DEFAULT 0 CHECK (credit_price_cents >= 0),
    currency                     text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    created_at                   timestamptz NOT NULL DEFAULT now(),
    updated_at                   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, brand_id)
);

-- A test: one hypothesis, one strategy, and its conclusion once closed.
CREATE TABLE reddit_experiments (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    hypothesis   text NOT NULL DEFAULT '' CHECK (length(hypothesis) <= 2000),
    strategy     text NOT NULL DEFAULT '' CHECK (length(strategy) <= 100),
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'closed')),
    conclusion   text NOT NULL DEFAULT '' CHECK (length(conclusion) <= 5000),
    mission_id   uuid,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    closed_at    timestamptz,
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reddit_experiments_ws ON reddit_experiments (workspace_id, brand_id, created_at DESC);

-- One thing done on Reddit: a comment, reply, post, thread group or upvote
-- order, and everything known about it since.
CREATE TABLE reddit_actions (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    experiment_id     uuid REFERENCES reddit_experiments (id) ON DELETE SET NULL,
    integration_id    uuid REFERENCES integrations (id) ON DELETE SET NULL,
    kind              text NOT NULL CHECK (kind IN ('comment', 'reply', 'post', 'thread', 'upvotes')),
    strategy          text NOT NULL DEFAULT '' CHECK (length(strategy) <= 100),
    -- The thread (comment, upvotes on a post), the comment (reply, upvotes
    -- on a comment) or the subreddit (post).
    target_url        text NOT NULL CHECK (length(target_url) <= 2000),
    target_kind       text NOT NULL DEFAULT '' CHECK (target_kind IN ('', 'thread', 'comment', 'subreddit')),
    subreddit         text NOT NULL DEFAULT '',
    title             text NOT NULL DEFAULT '' CHECK (length(title) <= 300),
    content           text NOT NULL DEFAULT '' CHECK (length(content) <= 40000),
    -- Thread groups: the child replies as sent.
    children          jsonb NOT NULL DEFAULT '[]',
    link_url          text NOT NULL DEFAULT '',
    link_type         text NOT NULL DEFAULT 'none' CHECK (link_type IN ('none', 'brand_site', 'review_site', 'docs', 'third_party', 'other')),
    brand_mentioned   boolean NOT NULL DEFAULT false,
    competitors       text[] NOT NULL DEFAULT '{}',
    tone              text NOT NULL DEFAULT '' CHECK (length(tone) <= 50),
    account_tier      text NOT NULL DEFAULT '' CHECK (account_tier IN ('', 'shared', 'dedicated')),
    -- Upvote plan sent with a task, or the order's own numbers.
    upvotes_goal      integer NOT NULL DEFAULT 0 CHECK (upvotes_goal BETWEEN 0 AND 2000),
    upvotes_per_day   integer NOT NULL DEFAULT 0 CHECK (upvotes_per_day BETWEEN 0 AND 10),
    upvotes_start     text NOT NULL DEFAULT '' CHECK (upvotes_start IN ('', 'immediately', 'after_3_days', 'after_5_days', 'after_7_days')),
    -- An upvote order on one of our own actions.
    parent_action_id  uuid REFERENCES reddit_actions (id) ON DELETE SET NULL,
    scheduled_at      timestamptz,
    notes             text NOT NULL DEFAULT '' CHECK (length(notes) <= 5000),
    -- draft: waiting for approval. approved: queued to send. sending: the
    -- call is out. submitted: Engain took it. live, frozen: visible on
    -- Reddit. removed, hidden, gone: was live, then not. failed: Engain
    -- refused or never published. cancelled: withdrawn before sending.
    status            text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'approved', 'sending', 'submitted', 'live', 'frozen',
                          'removed', 'hidden', 'gone', 'failed', 'cancelled', 'done')),
    status_reason     text,
    estimated_credits numeric(10, 2) NOT NULL DEFAULT 0,
    credits           numeric(10, 2),
    preflight         jsonb,
    engain_ids        text[] NOT NULL DEFAULT '{}',
    engain_status     text NOT NULL DEFAULT '',
    engain_raw        jsonb,
    -- On Reddit, once live.
    reddit_fullname   text NOT NULL DEFAULT '',
    permalink         text NOT NULL DEFAULT '',
    author            text NOT NULL DEFAULT '',
    score             integer,
    peak_score        integer,
    reply_count       integer,
    sibling_rank      integer,
    upvotes_delivered integer,
    removal_category  text NOT NULL DEFAULT '',
    sent_at           timestamptz,
    live_at           timestamptz,
    ended_at          timestamptz,
    last_checked_at   timestamptz,
    next_check_at     timestamptz,
    check_error       text,
    mission_id        uuid,
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_by       uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at       timestamptz,
    request_id        text,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reddit_actions_ws ON reddit_actions (workspace_id, brand_id, created_at DESC);
CREATE INDEX reddit_actions_experiment ON reddit_actions (experiment_id) WHERE experiment_id IS NOT NULL;
CREATE INDEX reddit_actions_due ON reddit_actions (next_check_at) WHERE next_check_at IS NOT NULL;
CREATE INDEX reddit_actions_subreddit_day ON reddit_actions (workspace_id, brand_id, lower(subreddit), created_at);

-- What changed and when: sent, live, removed, restored and so on.
CREATE TABLE reddit_action_events (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    action_id    uuid NOT NULL REFERENCES reddit_actions (id) ON DELETE CASCADE,
    kind         text NOT NULL,
    from_status  text NOT NULL DEFAULT '',
    to_status    text NOT NULL DEFAULT '',
    detail       jsonb NOT NULL DEFAULT '{}',
    at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX reddit_action_events_action ON reddit_action_events (action_id, at);

-- Each check of an action: what a logged-out visitor saw.
CREATE TABLE reddit_action_snapshots (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    action_id        uuid NOT NULL REFERENCES reddit_actions (id) ON DELETE CASCADE,
    at               timestamptz NOT NULL DEFAULT now(),
    state            text NOT NULL,
    score            integer,
    reply_count      integer,
    removal_category text NOT NULL DEFAULT '',
    locked           boolean NOT NULL DEFAULT false,
    archived         boolean NOT NULL DEFAULT false,
    body_hash        text NOT NULL DEFAULT ''
);
CREATE INDEX reddit_action_snapshots_action ON reddit_action_snapshots (action_id, at);
