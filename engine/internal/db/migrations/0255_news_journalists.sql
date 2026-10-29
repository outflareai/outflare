-- Journalists: OutflareBot reads each new news article's page for its
-- byline and date, and the email finder looks for the author's address at
-- the outlet. An author with an address is a person record on the outlet's
-- company; the articles they wrote are kept here, so a journalist's record
-- shows what they cover. An author without an address is only the byline
-- on the article, as written.

ALTER TABLE news_articles
    -- The byline as the page gives it, and when it says it was published.
    ADD COLUMN author       text NOT NULL DEFAULT '' CHECK (length(author) <= 200),
    ADD COLUMN published_at timestamptz,
    -- The read of the article's page: up, gone, blocked (robots.txt),
    -- opted_out, unreachable or failed; null until read.
    ADD COLUMN read_status  text CHECK (read_status IN ('up', 'gone', 'blocked', 'opted_out', 'unreachable', 'failed')),
    ADD COLUMN read_at      timestamptz;

-- Who wrote which article: a journalist's person record. No unique key on
-- the pair, so merging two records never collides; writes check first.
CREATE TABLE news_article_authors (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    article_id   uuid NOT NULL REFERENCES news_articles (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- 0 to 100: how sure the address reaches them.
    confidence   integer NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 100),
    found_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX news_article_authors_article_idx ON news_article_authors (article_id);
CREATE INDEX news_article_authors_record_idx ON news_article_authors (workspace_id, record_id);
