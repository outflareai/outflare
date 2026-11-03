-- Importing a business's own website into its knowledge: each run reads up
-- to max_pages pages of the site and adds or updates one article per page,
-- found again by its address (knowledge_items.source_url), so a second run
-- updates instead of duplicating.
ALTER TABLE knowledge_items ADD COLUMN source_url text CHECK (length(source_url) <= 2000);
CREATE UNIQUE INDEX knowledge_items_source_url ON knowledge_items (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), source_url)
    WHERE source_url IS NOT NULL AND deleted_at IS NULL;

CREATE TABLE knowledge_website_imports (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid REFERENCES brands (id) ON DELETE CASCADE,
    url            text NOT NULL CHECK (length(url) <= 2000),
    max_pages      integer NOT NULL DEFAULT 100 CHECK (max_pages BETWEEN 1 AND 500),
    -- What the imported articles may be used for, and whether they are
    -- published or kept as drafts for a person to go through first.
    visibility     text NOT NULL DEFAULT 'customers_ai' CHECK (visibility IN ('public', 'customers_ai', 'team')),
    publish        boolean NOT NULL DEFAULT true,
    collection_id  uuid REFERENCES knowledge_collections (id) ON DELETE SET NULL,
    status         text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    pages_found    integer NOT NULL DEFAULT 0,
    pages_added    integer NOT NULL DEFAULT 0,
    pages_updated  integer NOT NULL DEFAULT 0,
    pages_skipped  integer NOT NULL DEFAULT 0,
    error          text NOT NULL DEFAULT '' CHECK (length(error) <= 1000),
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    finished_at    timestamptz
);
CREATE INDEX knowledge_website_imports_list ON knowledge_website_imports (workspace_id, created_at DESC);
