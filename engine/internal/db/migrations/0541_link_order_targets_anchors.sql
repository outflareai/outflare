-- Link orders: the brand's own article and product pages a paid link can
-- point at, and two anchors on each order.
--
-- coverage_link_targets is the list a person ticks: each row is one page
-- on the brand's own site, marked as an article or a product page.
-- selected is the checkbox; an unticked row keeps its kind and is no
-- longer offered when ordering a link. Suggestions are computed from
-- already-stored data (competitor keywords, competitor link reads, the
-- site's own keywords) and are never stored here until a person saves one.
CREATE TABLE coverage_link_targets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    url text NOT NULL CHECK (length(url) BETWEEN 1 AND 2048),
    kind text NOT NULL DEFAULT 'article' CHECK (kind IN ('article', 'product')),
    title text NOT NULL DEFAULT '' CHECK (length(title) <= 500),
    selected boolean NOT NULL DEFAULT true,
    -- manual (a person added it) or suggested (saved from a suggestion).
    source text NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'suggested')),
    created_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    updated_by uuid REFERENCES actors(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, brand_id, url)
);
CREATE INDEX coverage_link_targets_brand_idx
    ON coverage_link_targets (workspace_id, brand_id, selected, kind);

-- The order's target page and anchors. Every column has a default, so
-- the previous release keeps writing orders without them.
--   target_choice: manual (a person ticked or typed the page) or
--   suggested (the page Outflare suggested, kept as is).
--   anchor_primary_source: where the primary anchor came from:
--   se_ranking (stored SE Ranking keywords), page_target (the page's
--   keyword target), seo_keywords (another SEO connection's stored
--   keywords) or manual.
--   anchor_secondary_type: brand, url or keyword.
ALTER TABLE coverage_link_orders
    ADD COLUMN target_id uuid REFERENCES coverage_link_targets(id) ON DELETE SET NULL,
    ADD COLUMN target_kind text NOT NULL DEFAULT '' CHECK (target_kind IN ('', 'article', 'product')),
    ADD COLUMN target_choice text NOT NULL DEFAULT 'manual' CHECK (target_choice IN ('manual', 'suggested')),
    ADD COLUMN anchor_primary text NOT NULL DEFAULT '' CHECK (length(anchor_primary) <= 500),
    ADD COLUMN anchor_primary_source text NOT NULL DEFAULT '' CHECK (anchor_primary_source IN ('', 'manual', 'se_ranking', 'page_target', 'seo_keywords')),
    ADD COLUMN anchor_secondary text NOT NULL DEFAULT '' CHECK (length(anchor_secondary) <= 500),
    ADD COLUMN anchor_secondary_type text NOT NULL DEFAULT '' CHECK (anchor_secondary_type IN ('', 'brand', 'url', 'keyword'));
CREATE INDEX coverage_link_orders_target_idx
    ON coverage_link_orders (target_id) WHERE target_id IS NOT NULL;
