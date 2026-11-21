-- Mentions on YouTube. A scan searches the web, through the workspace's own
-- web search connection, for videos that name the brand or a tracked
-- competitor, and keeps each video it finds here. Its watch page and its
-- captions are then read, through the workspace's own page reading
-- connection, so a mention can quote what was said and link to the second
-- it was said. Nothing here is read from YouTube with a key of Outflare's.

-- Videos a brand's scans found, with their captions once read.
CREATE TABLE mention_videos (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- YouTube's id for the video.
    video_id      text NOT NULL CHECK (video_id ~ '^[A-Za-z0-9_-]{11}$'),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    channel_name  text NOT NULL DEFAULT '' CHECK (length(channel_name) <= 200),
    channel_url   text NOT NULL DEFAULT '' CHECK (length(channel_url) <= 2048),
    views         bigint CHECK (views >= 0),
    seconds       integer CHECK (seconds >= 0),
    published_at  timestamptz,
    -- The search that found it, and what the result said.
    found_by      text NOT NULL DEFAULT '' CHECK (length(found_by) <= 200),
    snippet       text NOT NULL DEFAULT '' CHECK (length(snippet) <= 2000),
    -- Reading its captions: waiting, read, none (the video has none),
    -- failed (after a few tries, with the error code).
    captions_status text NOT NULL DEFAULT 'waiting' CHECK (captions_status IN ('waiting', 'read', 'none', 'failed')),
    captions_lang text NOT NULL DEFAULT '' CHECK (length(captions_lang) <= 20),
    -- What was said: [{"start": seconds, "text": "..."}], in time order.
    captions      jsonb,
    tries         integer NOT NULL DEFAULT 0,
    last_error_code text,
    read_at       timestamptz,
    found_at      timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mention_videos_brand_video_idx ON mention_videos (brand_id, video_id);
CREATE INDEX mention_videos_ws_idx ON mention_videos (workspace_id);
CREATE INDEX mention_videos_waiting_idx ON mention_videos (found_at) WHERE captions_status = 'waiting';

-- Mentions found by searching for videos.
ALTER TABLE brand_mentions DROP CONSTRAINT brand_mentions_origin_check;
ALTER TABLE brand_mentions ADD CONSTRAINT brand_mentions_origin_check CHECK (origin IN ('social_items', 'news', 'presence', 'manual', 'video_search'));

-- Videos in Query, with what was said in them.
CREATE VIEW query.mention_videos WITH (security_barrier) AS
SELECT v.id, v.brand_id, v.video_id, v.title, v.channel_name, v.channel_url, v.views, v.seconds, v.published_at,
    v.found_by, v.snippet, v.captions_status, v.captions_lang, v.captions, v.tries, v.last_error_code, v.read_at,
    v.found_at, v.updated_at
FROM mention_videos v JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR v.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR v.brand_id = s.brand);
