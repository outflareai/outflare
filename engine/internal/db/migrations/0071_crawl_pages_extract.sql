-- What a crawl keeps of each HTML page for the SEO audit: its on-page facts
-- and its links. Page text is never stored, only counted and hashed (to
-- find duplicates).
ALTER TABLE crawl_pages
    ADD COLUMN title            text NOT NULL DEFAULT '',
    -- How many <title> tags the page has; more than one is a problem.
    ADD COLUMN title_count      integer NOT NULL DEFAULT 0,
    ADD COLUMN meta_description text NOT NULL DEFAULT '',
    ADD COLUMN canonical        text NOT NULL DEFAULT '',
    ADD COLUMN canonical_count  integer NOT NULL DEFAULT 0,
    ADD COLUMN h1               text[] NOT NULL DEFAULT '{}',
    ADD COLUMN lang             text NOT NULL DEFAULT '',
    -- [{"lang": "en-au", "url": "https://..."}]
    ADD COLUMN hreflang         jsonb NOT NULL DEFAULT '[]',
    -- JSON-LD @type values: Organization, Product, FAQPage.
    ADD COLUMN schema_types     text[] NOT NULL DEFAULT '{}',
    ADD COLUMN word_count       integer NOT NULL DEFAULT 0,
    ADD COLUMN images           integer NOT NULL DEFAULT 0,
    -- Images with no alt attribute (alt="" is a decorative image, fine).
    ADD COLUMN images_no_alt    integer NOT NULL DEFAULT 0,
    -- A hash of the page's visible words, the same for duplicate pages.
    ADD COLUMN content_hash     text NOT NULL DEFAULT '',
    ADD COLUMN viewport         boolean NOT NULL DEFAULT true,
    -- The X-Robots-Tag header, which can also say noindex or nofollow.
    ADD COLUMN x_robots_tag     text NOT NULL DEFAULT '',
    -- Links to this page from other pages the crawl read.
    ADD COLUMN links_in         integer NOT NULL DEFAULT 0;

CREATE INDEX crawl_pages_hash_idx ON crawl_pages (run_id, content_hash) WHERE content_hash <> '';

-- Every link the crawl saw on a page it read, to its site and elsewhere,
-- so an audit can find broken links, orphan pages and where a page is
-- linked from.
CREATE TABLE crawl_links (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    run_id       uuid NOT NULL REFERENCES crawl_runs (id) ON DELETE CASCADE,
    from_page    bigint NOT NULL REFERENCES crawl_pages (id) ON DELETE CASCADE,
    to_url       text NOT NULL,
    anchor       text NOT NULL DEFAULT '',
    -- On the site (its host or its www. twin) or elsewhere.
    internal     boolean NOT NULL,
    -- rel="nofollow", "ugc" or "sponsored".
    nofollow     boolean NOT NULL DEFAULT false
);
CREATE INDEX crawl_links_from_idx ON crawl_links (from_page);
CREATE INDEX crawl_links_to_idx ON crawl_links (run_id, to_url);
