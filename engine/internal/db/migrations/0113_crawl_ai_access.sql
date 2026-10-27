-- AI crawlability: what each crawl found about AI crawlers' access to the
-- site. Per AI crawler, whether robots.txt lets it read the start page and
-- how many of the crawled pages it keeps it from; and whether the site has
-- an llms.txt.

CREATE TABLE crawl_ai_bots (
    run_id        uuid NOT NULL REFERENCES crawl_runs (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The robots.txt user-agent token, such as OAI-SearchBot.
    token         text NOT NULL,
    -- search (finds pages AI answers cite), user (fetches a page a person
    -- asked about) or training (collects pages to train models).
    purpose       text NOT NULL CHECK (purpose IN ('search', 'user', 'training')),
    -- Whether robots.txt lets it read the start page.
    root_allowed  boolean NOT NULL,
    -- The crawled HTML pages, and those robots.txt keeps it from.
    pages         integer NOT NULL,
    blocked_pages integer NOT NULL,
    PRIMARY KEY (run_id, token)
);

-- The site's /llms.txt as the crawl found it: its HTTP status (0 for no
-- answer, null when not checked) and size.
ALTER TABLE crawl_runs
    ADD COLUMN llms_txt_status integer,
    ADD COLUMN llms_txt_bytes integer NOT NULL DEFAULT 0;
