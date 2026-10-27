-- Regressions: what got worse for an SEO site, found by comparing the
-- latest numbers the syncs stored with the ones before: search clicks to
-- a page, a tracked keyword's rank, a page dropping out of Google's index,
-- conversions from a landing page, and the site's health score. Each open
-- one is also a fix on the fixes list (source seo_keywords); this table
-- keeps the numbers and the history after it recovers.
CREATE TABLE seo_regressions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_id      uuid NOT NULL REFERENCES seo_sites (id) ON DELETE CASCADE,
    -- The fix rule: search_clicks_dropped, rank_dropped, page_deindexed,
    -- conversions_dropped or health_dropped.
    kind         text NOT NULL,
    -- What dropped: a page URL, a tracked keyword, or '' for the site.
    subject      text NOT NULL DEFAULT '',
    -- The number before and now (clicks or conversions a week, a position,
    -- a health score); NULL when it has none, such as a page out of the
    -- top 100.
    before       double precision,
    after        double precision,
    -- What changed, in words.
    detail       text NOT NULL DEFAULT '',
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved')),
    detected_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    resolved_at  timestamptz
);
CREATE UNIQUE INDEX seo_regressions_open_idx ON seo_regressions (site_id, kind, subject) WHERE status = 'open';
CREATE INDEX seo_regressions_ws_idx ON seo_regressions (workspace_id, site_id, detected_at DESC);
