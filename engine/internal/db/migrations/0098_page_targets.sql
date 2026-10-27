-- The keyword each page of an SEO site targets. A person or agent sets
-- it, or accepts the one Search Console suggests (the query that brings the
-- page the most clicks); a dismissed page gets no more suggestions. The
-- site's latest crawl is checked against each target, and what is missing
-- (the keyword not in the title, H1 or description) goes on the fixes
-- list as source seo_keywords.
CREATE TABLE page_targets (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id      uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    url          text NOT NULL,
    -- Lowercase, single spaces.
    keyword      text NOT NULL DEFAULT '',
    secondary    text[] NOT NULL DEFAULT '{}',
    -- set (the page's target) or dismissed (no suggestions for it).
    status       text NOT NULL DEFAULT 'set' CHECK (status IN ('set', 'dismissed')),
    -- manual or suggested: how the target was chosen.
    origin       text NOT NULL DEFAULT 'manual' CHECK (origin IN ('manual', 'suggested')),
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (site_id, url)
);
CREATE INDEX page_targets_ws_idx ON page_targets (workspace_id, site_id, status);
