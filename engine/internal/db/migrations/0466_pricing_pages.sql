-- Prices: one pricing page for the brand and for each tracked competitor,
-- read once a week through the workspace's own page reader. A read whose
-- plans differ from the last kept one is kept with what changed (a plan
-- added or removed, a price up or down), so the history shows when a
-- competitor changed its prices. Only that one page is read: a light look,
-- never a crawl.

-- A pricing page: the brand's own (competitor_id NULL) or a competitor's.
-- Clearing the address deletes the page and its history.
CREATE TABLE pricing_pages (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    competitor_id  uuid REFERENCES seo_competitors (id) ON DELETE CASCADE,
    url            text NOT NULL,
    last_read_at   timestamptz,
    last_failed_at timestamptz,
    next_read_at   timestamptz NOT NULL DEFAULT now(),
    -- The code of the last read's failure (blocked, no_prices_found,
    -- page_failed, connector_error); NULL after a read that worked.
    last_error     text,
    failures       integer NOT NULL DEFAULT 0,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX pricing_pages_one ON pricing_pages (brand_id, coalesce(competitor_id, '00000000-0000-0000-0000-000000000000'::uuid));
CREATE INDEX pricing_pages_ws ON pricing_pages (workspace_id, brand_id);
CREATE INDEX pricing_pages_due ON pricing_pages (next_read_at);

-- A kept read: the plans the page showed and what changed from the read
-- before. A read that finds the same plans only moves the page's
-- last_read_at.
CREATE TABLE pricing_reads (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    page_id      uuid NOT NULL REFERENCES pricing_pages (id) ON DELETE CASCADE,
    read_at      timestamptz NOT NULL DEFAULT now(),
    plans        jsonb NOT NULL DEFAULT '[]',
    changes      jsonb NOT NULL DEFAULT '[]'
);
CREATE INDEX pricing_reads_page ON pricing_reads (page_id, read_at DESC);

-- A competitor moved to another brand takes its pricing page along.
CREATE TRIGGER seo_competitors_pricing_moved AFTER UPDATE OF brand_id ON seo_competitors FOR EACH ROW
    WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id AND NEW.brand_id IS NOT NULL)
    EXECUTE FUNCTION outflare_children_moved('pricing_pages', 'competitor_id');
CREATE TRIGGER pricing_pages_rows_moved AFTER UPDATE OF brand_id ON pricing_pages FOR EACH ROW
    WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_children_moved('pricing_reads', 'page_id');
