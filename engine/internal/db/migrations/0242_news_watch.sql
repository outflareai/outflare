-- News watch: saved searches a workspace runs on a schedule through its
-- own web search connection (Geonode), the articles they find, and the
-- articles about a brand, kept as its coverage. Topic searches follow a
-- subject and the journalists who write about it; coverage searches look
-- for the brand itself. Every coverage item is a mention (mention.found)
-- on the outlet's company record.

CREATE TABLE news_searches (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand it is for; shared topic searches have none.
    brand_id          uuid REFERENCES brands (id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    -- What is searched, as typed: "residential proxies", "Northpeak".
    query             text NOT NULL CHECK (length(query) BETWEEN 1 AND 500),
    -- topic: a subject to follow. coverage: the brand itself; each article
    -- found becomes coverage of the brand.
    purpose           text NOT NULL CHECK (purpose IN ('topic', 'coverage')),
    -- A language or language-region tag (en, en-GB), '' for the
    -- provider's default.
    locale            text NOT NULL DEFAULT '' CHECK (length(locale) <= 10),
    -- How far back each run looks.
    time_range        text NOT NULL DEFAULT 'week' CHECK (time_range IN ('day', 'week', 'month')),
    check_every_hours integer NOT NULL DEFAULT 24 CHECK (check_every_hours BETWEEN 6 AND 168),
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused')),
    last_checked_at   timestamptz,
    next_check_at     timestamptz NOT NULL DEFAULT now(),
    -- The error code of the last run, when it failed.
    last_error_code   text,
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    CHECK (purpose = 'topic' OR brand_id IS NOT NULL)
);
CREATE INDEX news_searches_ws_idx ON news_searches (workspace_id, created_at DESC);
CREATE INDEX news_searches_due_idx ON news_searches (next_check_at) WHERE status = 'active';

-- An article a search found, once per workspace however many searches
-- find it. The outlet is the URL's website.
CREATE TABLE news_articles (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    url           text NOT NULL CHECK (length(url) <= 2048),
    title         text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    snippet       text NOT NULL DEFAULT '' CHECK (length(snippet) <= 1000),
    first_seen_at timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, url)
);
CREATE INDEX news_articles_seen_idx ON news_articles (workspace_id, first_seen_at DESC);

-- Which search found which article, and where it ranked the first time.
CREATE TABLE news_hits (
    search_id    uuid NOT NULL REFERENCES news_searches (id) ON DELETE CASCADE,
    article_id   uuid NOT NULL REFERENCES news_articles (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    position     integer NOT NULL DEFAULT 0,
    found_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (search_id, article_id)
);
CREATE INDEX news_hits_article_idx ON news_hits (article_id);

-- Coverage: an article that mentions a brand, on the outlet's company
-- record. New items wait for a person to confirm or dismiss them.
CREATE TABLE coverage (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    article_id        uuid NOT NULL REFERENCES news_articles (id) ON DELETE CASCADE,
    -- The outlet: the company whose website the article is on.
    record_id         uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- The search that found it, if any.
    search_id         uuid REFERENCES news_searches (id) ON DELETE SET NULL,
    status            text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'confirmed', 'dismissed')),
    -- Whether the article links to the brand's site: null until checked.
    linked            boolean,
    -- news_watch, or manual when a person or agent added it.
    source            text NOT NULL CHECK (source IN ('news_watch', 'manual')),
    found_at          timestamptz NOT NULL DEFAULT now(),
    status_changed_at timestamptz NOT NULL DEFAULT now(),
    updated_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, article_id)
);
CREATE INDEX coverage_ws_idx ON coverage (workspace_id, status, found_at DESC);
CREATE INDEX coverage_record_idx ON coverage (workspace_id, record_id);
