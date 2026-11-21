-- Mentions: who talks about a brand or its competitors in conversational
-- places (videos, podcasts, social posts, forums, news), found by scanning
-- what the brand's own searches and connections bring in. What is listened
-- for is read from the brand itself (its name) and its Competitors list
-- (tracked competitors' names, aliases and sites), never copied here, so a
-- change on the Competitors page applies to the next scan.
--
-- Each mention is one place one name was said: the same video naming two
-- competitors is two mentions. Nothing here contacts anyone; the person
-- behind a mention becomes a customer record only when someone chooses to.

-- A brand's mention settings: whether listening is on and which sources it
-- reads. Off until someone turns it on.
CREATE TABLE brand_mention_settings (
    brand_id     uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT false,
    -- The sources it reads, from the sources the engine knows. A source
    -- whose connection is missing is shown as needing one and skipped.
    sources      text[] NOT NULL DEFAULT '{reddit,news,web}',
    -- How often a scan runs while enabled.
    scan_every_hours integer NOT NULL DEFAULT 6 CHECK (scan_every_hours BETWEEN 1 AND 168),
    last_scan_at timestamptz,
    next_scan_at timestamptz NOT NULL DEFAULT now(),
    -- The error code of the last scan, when it failed.
    last_error_code text,
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX brand_mention_settings_ws_idx ON brand_mention_settings (workspace_id);
CREATE INDEX brand_mention_settings_due_idx ON brand_mention_settings (next_scan_at) WHERE enabled;

CREATE TABLE brand_mentions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- Where it was said.
    source        text NOT NULL CHECK (source IN ('youtube', 'podcast', 'x', 'reddit', 'hacker_news', 'bluesky', 'news', 'web')),
    -- The source's own id for the piece (a video id, a post id), or the URL.
    external_id   text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 2048),
    url           text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    -- The piece's title: the video, episode, post or article.
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    -- The words around the name, as said or written.
    quote         text NOT NULL DEFAULT '' CHECK (length(quote) <= 2000),
    -- Seconds into a video or episode where it was said, when known.
    at_seconds    integer CHECK (at_seconds >= 0),
    -- Who was named: the brand itself, or one of its competitors.
    about         text NOT NULL CHECK (about IN ('brand', 'competitor')),
    competitor_id uuid REFERENCES seo_competitors (id) ON DELETE CASCADE,
    -- The name or alias that matched, as listened for.
    matched       text NOT NULL CHECK (length(matched) BETWEEN 1 AND 200),
    -- What kind of mention it is (review, tutorial, comparison, complaint,
    -- recommendation, sponsored), when known.
    label         text CHECK (label IN ('review', 'tutorial', 'comparison', 'complaint', 'recommendation', 'question', 'sponsored', 'other')),
    sentiment     text CHECK (sentiment IN ('positive', 'neutral', 'negative', 'mixed')),
    -- Who said it, as the source shows them. They become a customer record
    -- only when someone adds them.
    author_name   text NOT NULL DEFAULT '' CHECK (length(author_name) <= 200),
    author_handle text NOT NULL DEFAULT '' CHECK (length(author_handle) <= 200),
    author_url    text NOT NULL DEFAULT '' CHECK (length(author_url) <= 2048),
    -- Their audience where it was said: subscribers, followers, listeners
    -- or upvotes, when known.
    reach         bigint CHECK (reach >= 0),
    -- How worth a look it is, 0 to 100.
    score         integer NOT NULL DEFAULT 0 CHECK (score BETWEEN 0 AND 100),
    status        text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'contacted', 'replied', 'not_a_fit', 'on_board')),
    -- What found it: social_items, news, presence, manual.
    origin        text NOT NULL CHECK (origin IN ('social_items', 'news', 'presence', 'manual')),
    -- The row it came from, when it came from another table.
    origin_id     uuid,
    published_at  timestamptz,
    found_at      timestamptz NOT NULL DEFAULT now(),
    status_changed_at timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    CHECK ((about = 'competitor') = (competitor_id IS NOT NULL))
);
-- One mention per piece and name: a brand mention, or one per competitor.
CREATE UNIQUE INDEX brand_mentions_piece_idx ON brand_mentions (brand_id, source, external_id, COALESCE(competitor_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX brand_mentions_list_idx ON brand_mentions (workspace_id, brand_id, found_at DESC, id);
CREATE INDEX brand_mentions_competitor_idx ON brand_mentions (competitor_id) WHERE competitor_id IS NOT NULL;

-- Mentions in Query. Like every query view, each shows one workspace's rows
-- and the brands the caller may see, and needs the social.read scope the
-- API asks for the same data.
CREATE VIEW query.brand_mentions WITH (security_barrier) AS
SELECT m.id, m.brand_id, m.source, m.external_id, m.url, m.title, m.quote, m.at_seconds, m.about, m.competitor_id,
    m.matched, m.label, m.sentiment, m.author_name, m.author_handle, m.author_url, m.reach, m.score, m.status,
    m.origin, m.published_at, m.found_at, m.status_changed_at, m.created_at, m.updated_at
FROM brand_mentions m JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR m.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR m.brand_id = s.brand);

-- Whether the listener is on for a brand, what it reads and how often.
CREATE VIEW query.brand_mention_settings WITH (security_barrier) AS
SELECT x.brand_id, x.enabled, x.sources, x.scan_every_hours, x.last_scan_at, x.next_scan_at, x.last_error_code,
    x.created_at, x.updated_at
FROM brand_mention_settings x JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes)
    AND (s.brands IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR x.brand_id = s.brand);
