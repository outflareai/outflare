-- Social publishing: the brand's own original posts (not replies) on its
-- own X account or LinkedIn page or profile, written by a person or
-- drafted by the workspace's own AI, approved by a person, and posted now
-- or at a set time through the workspace's own connection.

-- A recurring series: a person approves it once, and posts handed in for
-- it (a daily changelog summary from an API caller) are queued without
-- another approval, up to daily_limit a day.
CREATE TABLE social_post_series (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    account_id   uuid NOT NULL REFERENCES social_accounts (id) ON DELETE CASCADE,
    name         text NOT NULL,
    -- What the series posts, for the person who approves it: a sample or
    -- a description. Posts are not checked against it.
    template     text NOT NULL DEFAULT '',
    daily_limit  int NOT NULL DEFAULT 1 CHECK (daily_limit BETWEEN 1 AND 24),
    paused       boolean NOT NULL DEFAULT false,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    -- Unapproved until a person approves it; a change to the account,
    -- template or limit takes the approval away.
    approved_by  uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at  timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX social_post_series_ws ON social_post_series (workspace_id, created_at);
CREATE INDEX social_post_series_account ON social_post_series (account_id);

CREATE TABLE social_publications (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- The page or profile that posts, and the connection that reaches it
    -- when it was approved.
    account_id    uuid NOT NULL REFERENCES social_accounts (id) ON DELETE CASCADE,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    channel       text NOT NULL CHECK (channel IN ('x', 'linkedin')),
    -- The text as written; the tracked link, when there is one, goes after
    -- it on its own line.
    body          text NOT NULL DEFAULT '',
    -- The link with its UTM tags (one campaign per account), and the post's
    -- own content tag, which customer events are matched on.
    link_url      text NOT NULL DEFAULT '',
    utm_content   text NOT NULL,
    -- What the AI was asked to write about, for an AI draft.
    prompt        text NOT NULL DEFAULT '',
    -- NULL: as soon as it is approved.
    scheduled_for timestamptz,
    -- drafting: the AI is writing it. draft: waiting for a person.
    -- approved: queued for its time. publishing: the call is being made.
    -- published: it is up. failed: the channel refused, the outcome is
    -- unknown, or the AI could not write it. cancelled: a person said no.
    status        text NOT NULL DEFAULT 'draft'
                      CHECK (status IN ('drafting', 'draft', 'approved', 'publishing', 'published', 'failed', 'cancelled')),
    status_reason text,
    -- person, ai, or series (handed in for an approved series).
    drafted_via   text NOT NULL DEFAULT 'person' CHECK (drafted_via IN ('person', 'ai', 'series')),
    series_id     uuid REFERENCES social_post_series (id) ON DELETE SET NULL,
    mission_id    uuid REFERENCES missions (id) ON DELETE SET NULL,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    approved_at   timestamptz,
    -- What it made on the channel, and its row among the account's posts,
    -- whose numbers the daily sync reads.
    external_id   text,
    url           text,
    post_id       uuid REFERENCES social_posts (id) ON DELETE SET NULL,
    published_at  timestamptz,
    request_id    text,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX social_publications_ws ON social_publications (workspace_id, created_at DESC);
CREATE INDEX social_publications_account ON social_publications (account_id, created_at DESC);
CREATE INDEX social_publications_series ON social_publications (series_id, created_at) WHERE series_id IS NOT NULL;
CREATE INDEX social_publications_due ON social_publications (scheduled_for) WHERE status = 'approved';
CREATE INDEX social_publications_post ON social_publications (post_id) WHERE post_id IS NOT NULL;
