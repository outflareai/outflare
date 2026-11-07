-- What reading a page AI answers cite showed: whether it links to, or only
-- names, the brand and each of its competitors. One row per brand and page,
-- replaced by each new read.
CREATE TABLE ai_page_checks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    url          text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    -- queued until read; done when the page loaded; unreachable when it
    -- didn't, or its robots.txt or owner asked not to be read.
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'done', 'unreachable')),
    title        text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    -- [{"name", "competitor_id" (absent for the brand), "how": link,
    -- mention or absent, "nofollow", "anchor"}]
    found        jsonb NOT NULL DEFAULT '[]',
    requested_at timestamptz NOT NULL DEFAULT now(),
    checked_at   timestamptz,
    UNIQUE (brand_id, url)
);
CREATE INDEX ai_page_checks_ws_idx ON ai_page_checks (workspace_id, brand_id, checked_at);
