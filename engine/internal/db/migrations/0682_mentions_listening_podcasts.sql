-- Mentions: what is listened for, own channels, and podcasts.
--
-- What the mention listener listens for, adjusted by hand. By default a
-- brand listens for its own name and site and each tracked competitor's
-- brand name and site, read live from the brand and its Competitors list.
-- A rule here changes one of those rows: turns it off, adds a term the
-- automatic list leaves out, or skips one it would use. A row with no rule
-- listens for its automatic terms.
CREATE TABLE mention_listen_rules (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The competitor the rule is for; empty for the brand's own row.
    competitor_id uuid REFERENCES seo_competitors (id) ON DELETE CASCADE,
    -- Off: scans don't listen for this row at all.
    listening     boolean NOT NULL DEFAULT true,
    -- Terms added by hand, listened for as well as the automatic ones.
    added_terms   text[] NOT NULL DEFAULT '{}',
    -- Automatic terms not listened for.
    skipped_terms text[] NOT NULL DEFAULT '{}',
    updated_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mention_listen_rules_competitor_idx ON mention_listen_rules (brand_id, competitor_id) WHERE competitor_id IS NOT NULL;
CREATE UNIQUE INDEX mention_listen_rules_brand_idx ON mention_listen_rules (brand_id) WHERE competitor_id IS NULL;
CREATE INDEX mention_listen_rules_ws_idx ON mention_listen_rules (workspace_id);
CREATE INDEX mention_listen_rules_competitor_fk_idx ON mention_listen_rules (competitor_id);

CREATE VIEW query.mention_listen_rules WITH (security_barrier) AS
SELECT x.id, x.brand_id, x.competitor_id, x.listening, x.added_terms, x.skipped_terms, x.created_at, x.updated_at
FROM mention_listen_rules x JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);

-- A video posted on the brand's own channel or a competitor's is not a
-- mention: the listener looks for other people naming them. Reading a
-- video's page says whose channel it is; such a video is kept here, marked,
-- so later scans skip it.
ALTER TABLE mention_videos
    ADD COLUMN own_about text CHECK (own_about IN ('brand', 'competitor')),
    ADD COLUMN own_competitor_id uuid REFERENCES seo_competitors (id) ON DELETE SET NULL;
CREATE INDEX mention_videos_own_competitor_idx ON mention_videos (own_competitor_id) WHERE own_competitor_id IS NOT NULL;

CREATE OR REPLACE VIEW query.mention_videos WITH (security_barrier) AS
SELECT v.id, v.brand_id, v.video_id, v.title, v.channel_name, v.channel_url, v.views, v.seconds, v.published_at,
    v.found_by, v.snippet, v.captions_status, v.captions_lang, v.captions, v.tries, v.last_error_code, v.read_at,
    v.found_at, v.updated_at, v.own_about, v.own_competitor_id
FROM mention_videos v JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR v.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR v.brand_id = s.brand);

-- Mentions in podcasts. A scan searches a podcast directory, on the
-- workspace's own connection, for episodes whose title, show notes or
-- people name the brand or a tracked competitor, and keeps each episode
-- here. When the show publishes a transcript in its feed, it is read, so a
-- mention can quote what was said and the second it was said.
CREATE TABLE mention_episodes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The directory's id for the episode, and which directory.
    episode_id    text NOT NULL CHECK (length(episode_id) <= 100),
    directory     text NOT NULL CHECK (length(directory) <= 50),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    -- The episode's show notes, as the show wrote them.
    description   text NOT NULL DEFAULT '' CHECK (length(description) <= 20000),
    link          text NOT NULL DEFAULT '' CHECK (length(link) <= 2048),
    audio_url     text NOT NULL DEFAULT '' CHECK (length(audio_url) <= 2048),
    seconds       integer CHECK (seconds >= 0),
    published_at  timestamptz,
    -- The show it is part of, and who makes it.
    show_id       text NOT NULL DEFAULT '' CHECK (length(show_id) <= 100),
    show_title    text NOT NULL DEFAULT '' CHECK (length(show_title) <= 500),
    show_link     text NOT NULL DEFAULT '' CHECK (length(show_link) <= 2048),
    show_image    text NOT NULL DEFAULT '' CHECK (length(show_image) <= 2048),
    author        text NOT NULL DEFAULT '' CHECK (length(author) <= 300),
    -- The search that found it.
    found_by      text NOT NULL DEFAULT '' CHECK (length(found_by) <= 200),
    -- The transcript the show publishes, and reading it: waiting, read,
    -- none (the show publishes none), failed (after a few tries).
    transcript_url  text NOT NULL DEFAULT '' CHECK (length(transcript_url) <= 2048),
    transcript_type text NOT NULL DEFAULT '' CHECK (length(transcript_type) <= 100),
    transcript_status text NOT NULL DEFAULT 'none' CHECK (transcript_status IN ('waiting', 'read', 'none', 'failed')),
    -- What was said: [{"start": seconds, "text": "..."}], in time order.
    transcript    jsonb,
    -- Set when the show is the brand's own or a competitor's: not a mention.
    own_about     text CHECK (own_about IN ('brand', 'competitor')),
    own_competitor_id uuid REFERENCES seo_competitors (id) ON DELETE SET NULL,
    tries         integer NOT NULL DEFAULT 0,
    last_error_code text,
    read_at       timestamptz,
    found_at      timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mention_episodes_brand_episode_idx ON mention_episodes (brand_id, directory, episode_id);
CREATE INDEX mention_episodes_ws_idx ON mention_episodes (workspace_id);
CREATE INDEX mention_episodes_own_competitor_idx ON mention_episodes (own_competitor_id) WHERE own_competitor_id IS NOT NULL;

-- Mentions found by searching for podcast episodes.
ALTER TABLE brand_mentions DROP CONSTRAINT brand_mentions_origin_check;
ALTER TABLE brand_mentions ADD CONSTRAINT brand_mentions_origin_check CHECK (origin IN ('social_items', 'news', 'presence', 'manual', 'video_search', 'podcast_search'));

CREATE VIEW query.mention_episodes WITH (security_barrier) AS
SELECT e.id, e.brand_id, e.episode_id, e.directory, e.title, e.description, e.link, e.audio_url, e.seconds, e.published_at,
    e.show_id, e.show_title, e.show_link, e.show_image, e.author, e.found_by, e.transcript_url, e.transcript_type,
    e.transcript_status, e.transcript, e.own_about, e.own_competitor_id, e.tries, e.last_error_code, e.read_at, e.found_at, e.updated_at
FROM mention_episodes e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR e.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR e.brand_id = s.brand);
