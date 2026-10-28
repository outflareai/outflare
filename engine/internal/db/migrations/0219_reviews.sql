-- Reviews: each brand's own review pages (Trustpilot, G2, Capterra, a
-- Google Maps place), from its links, read once a day through the
-- workspace's own page reader. Every review on a page is kept with its
-- rating, sentiment, dates, link and the business's reply, and each page
-- gets a row a day with the platform's own score and count, so the trend
-- shows from the first day. Outflare's own review score and momentum are
-- worked out from the reviews' dates, so they need no table of their own.

-- A review page: one brand link of a review kind. Its address is the
-- brand's link; removing the link marks the page removed and keeps its
-- reviews.
CREATE TABLE review_pages (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid REFERENCES brands (id) ON DELETE SET NULL,
    platform       text NOT NULL CHECK (platform IN ('trustpilot', 'g2', 'capterra', 'google_reviews')),
    -- The page's id on the platform (a Trustpilot domain, a G2 slug), so
    -- the same page pasted twice is one page.
    external_id    text NOT NULL,
    url            text NOT NULL,
    name           text NOT NULL DEFAULT '',
    -- active: read every day. paused: kept, not read. removed: no longer
    -- in the brand's links.
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'removed')),
    -- The platform's own headline score on a 1 to 5 scale, and how many
    -- reviews it says it has. NULL until read, never zero.
    score          numeric(4, 2),
    review_count   bigint,
    -- The first read takes every review the page shows (up to a limit);
    -- later reads take the newest.
    backfilled_at  timestamptz,
    last_read_at   timestamptz,
    next_read_at   timestamptz NOT NULL DEFAULT now(),
    last_error     text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, platform, external_id)
);
CREATE INDEX review_pages_ws ON review_pages (workspace_id, brand_id, platform);
CREATE INDEX review_pages_due ON review_pages (next_read_at) WHERE status = 'active';

-- One review. Rating is on a 1 to 5 scale; rating_native as the platform
-- shows it. Sentiment comes from the stars (rating) until the workspace's
-- own AI reads the text (ai). Text is the reviewer's, kept as written:
-- data, never instructions.
CREATE TABLE reviews (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid REFERENCES brands (id) ON DELETE SET NULL,
    page_id         uuid NOT NULL REFERENCES review_pages (id) ON DELETE CASCADE,
    platform        text NOT NULL,
    external_id     text NOT NULL,
    url             text NOT NULL DEFAULT '',
    rating          numeric(3, 2) NOT NULL CHECK (rating >= 0 AND rating <= 5),
    rating_native   numeric(6, 2),
    rating_scale    numeric(6, 2),
    title           text NOT NULL DEFAULT '',
    body            text NOT NULL DEFAULT '',
    language        text NOT NULL DEFAULT '',
    -- The reviewer as the platform shows them, never matched by name.
    author_name     text NOT NULL DEFAULT '',
    author_country  text NOT NULL DEFAULT '',
    verified        boolean NOT NULL DEFAULT false,
    posted_at       timestamptz NOT NULL,
    edited_at       timestamptz,
    sentiment       text NOT NULL CHECK (sentiment IN ('positive', 'neutral', 'negative', 'mixed')),
    sentiment_source text NOT NULL DEFAULT 'rating' CHECK (sentiment_source IN ('rating', 'ai')),
    -- The business's public reply, as read from the page or marked
    -- replied in Outflare.
    reply_body      text NOT NULL DEFAULT '',
    replied_at      timestamptz,
    -- live: on the page. removed: gone from the page, kept here.
    state           text NOT NULL DEFAULT 'live' CHECK (state IN ('live', 'removed')),
    first_seen_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at    timestamptz NOT NULL DEFAULT now(),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (page_id, external_id)
);
CREATE INDEX reviews_ws ON reviews (workspace_id, brand_id, posted_at DESC);
CREATE INDEX reviews_page ON reviews (page_id, posted_at DESC);
CREATE INDEX reviews_unanswered ON reviews (page_id) WHERE replied_at IS NULL AND sentiment = 'negative' AND state = 'live';

-- One row per review page per day: the platform's numbers as last read
-- that day, and the star breakdown of the reviews kept. A number the page
-- didn't show is NULL, never zero. Shaped so the shared daily metrics
-- table can take these rows when it exists.
CREATE TABLE review_page_days (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    page_id       uuid NOT NULL REFERENCES review_pages (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    day           date NOT NULL,
    score         numeric(4, 2),
    review_count  bigint,
    new_reviews   integer NOT NULL DEFAULT 0,
    stars_1       integer NOT NULL DEFAULT 0,
    stars_2       integer NOT NULL DEFAULT 0,
    stars_3       integer NOT NULL DEFAULT 0,
    stars_4       integer NOT NULL DEFAULT 0,
    stars_5       integer NOT NULL DEFAULT 0,
    -- Outflare's review score for the page that day, out of 100, and its
    -- momentum (growing, improving, steady, stagnating, slipping, too_few).
    outflare_score integer,
    momentum      text,
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (page_id, day)
);
CREATE INDEX review_page_days_ws ON review_page_days (workspace_id, day);

-- How a brand answers reviews, for drafting: its tone, who signs, where
-- to send people for help, what never to say, and notes such as known
-- issues and fixes. Filled in from the brand's memories the first time.
CREATE TABLE review_reply_guides (
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    tone            text NOT NULL DEFAULT '',
    signed_by       text NOT NULL DEFAULT '',
    support_contact text NOT NULL DEFAULT '',
    never_say       text NOT NULL DEFAULT '',
    notes           text NOT NULL DEFAULT '',
    updated_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (brand_id)
);
CREATE INDEX review_reply_guides_ws ON review_reply_guides (workspace_id);

-- A drafted answer to a review. The workspace's own AI writes it
-- (drafting, then draft or failed); a person edits it and posts it on the
-- platform. It is never posted from here.
CREATE TABLE review_replies (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    review_id     uuid NOT NULL REFERENCES reviews (id) ON DELETE CASCADE,
    status        text NOT NULL DEFAULT 'drafting' CHECK (status IN ('drafting', 'draft', 'failed', 'skipped')),
    body          text NOT NULL DEFAULT '',
    -- What the person asked for on a rewrite ("shorter").
    hint          text NOT NULL DEFAULT '',
    status_reason text,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX review_replies_review ON review_replies (review_id, created_at DESC);
CREATE INDEX review_replies_ws ON review_replies (workspace_id, created_at);

-- Query: reviews and their daily numbers (social.read, like Social).

CREATE VIEW query.reviews WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.page_id, r.platform, r.url, r.rating, r.title, r.body, r.language, r.author_name, r.author_country, r.verified,
    r.posted_at, r.edited_at, r.sentiment, r.sentiment_source, r.reply_body, r.replied_at, r.state, r.first_seen_at
FROM reviews r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands));

CREATE VIEW query.review_page_days WITH (security_barrier) AS
SELECT d.page_id, d.brand_id, p.platform, d.day, d.score, d.review_count, d.new_reviews, d.stars_1, d.stars_2, d.stars_3, d.stars_4, d.stars_5,
    d.outflare_score, d.momentum
FROM review_page_days d JOIN review_pages p ON p.id = d.page_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id AND p.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR d.brand_id IS NULL OR d.brand_id = ANY (s.brands));
