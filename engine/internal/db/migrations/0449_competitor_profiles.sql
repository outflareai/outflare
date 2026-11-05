-- Competitors' social accounts and review pages. A tracked competitor
-- keeps the links to its profiles (an X account, a LinkedIn page, a
-- Trustpilot or G2 page): suggested from the links on its home page, then
-- confirmed or dismissed by a person, or added by hand. Confirmed ones are
-- read every day by the same readers as the brand's own, through the
-- workspace's own page reader, and their accounts and pages carry the
-- competitor, so the brand's own social and review numbers leave them out.
-- Only adds, so the version before still runs.

-- Each element: {"kind": "x", "url": "https://x.com/acme",
-- "state": "suggested" | "confirmed" | "dismissed", "source": "home_page" | "manual"}.
ALTER TABLE seo_competitors ADD COLUMN links jsonb NOT NULL DEFAULT '[]';

ALTER TABLE social_accounts ADD COLUMN competitor_id uuid REFERENCES seo_competitors (id) ON DELETE CASCADE;
CREATE INDEX social_accounts_competitor_idx ON social_accounts (competitor_id) WHERE competitor_id IS NOT NULL;

ALTER TABLE review_pages ADD COLUMN competitor_id uuid REFERENCES seo_competitors (id) ON DELETE CASCADE;
CREATE INDEX review_pages_competitor_idx ON review_pages (competitor_id) WHERE competitor_id IS NOT NULL;

-- Queries over the brand's review numbers leave competitors' pages out,
-- like the API.
CREATE OR REPLACE VIEW query.review_page_days WITH (security_barrier) AS
SELECT d.page_id, d.brand_id, p.platform, d.day, d.score, d.review_count, d.new_reviews, d.stars_1, d.stars_2, d.stars_3, d.stars_4, d.stars_5,
    d.outflare_score, d.momentum
FROM review_page_days d JOIN review_pages p ON p.id = d.page_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id AND p.workspace_id = s.workspace_id
WHERE 'social.read' = ANY (s.scopes) AND (s.brands IS NULL OR d.brand_id IS NULL OR d.brand_id = ANY (s.brands)) AND p.competitor_id IS NULL;
