-- What each name AI answers give next to a brand is, so it is decided once
-- and not asked about every day: a competitor (with its website, to say
-- Yes or No to on the Competitors page), a product of a competitor already
-- on the list (counted as that competitor), a company or tool that is not
-- a competitor (a library, a framework, a review site), not a brand at all
-- (a description, such as "web scraping API"), or not known yet. The
-- workspace's own model decides from the sites the answers cite. Only
-- adds, so the version before still runs.
CREATE TABLE ai_named_brands (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name          text NOT NULL,
    name_key      text NOT NULL,
    kind          text NOT NULL CHECK (kind IN ('competitor', 'product', 'other', 'not_brand', 'unknown')),
    -- what a company that is not a competitor is, in a few words
    label         text NOT NULL DEFAULT '',
    host          text NOT NULL DEFAULT '',
    sure          text NOT NULL DEFAULT 'unknown' CHECK (sure IN ('certain', 'likely', 'unknown')),
    -- the competitor on the list it is, or is a product of
    competitor_id uuid REFERENCES seo_competitors (id) ON DELETE SET NULL,
    checked_at    timestamptz NOT NULL DEFAULT now(),
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, name_key)
);
CREATE INDEX ai_named_brands_ws_idx ON ai_named_brands (workspace_id, brand_id, kind);
