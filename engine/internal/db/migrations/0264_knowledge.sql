-- Knowledge: what a business knows, written by its people for staff and
-- for every AI in Outflare (support replies, missions, the built-in agent,
-- MCP clients, drafts). Articles and quick answers (a question and its
-- answer) in folders, each with who may see it, a brand, and every version
-- kept. Company context and memories stay where they are; the web app
-- shows them beside knowledge.

-- Folders. They nest; a folder of a brand holds only that brand's items
-- and shared ones are null.
CREATE TABLE knowledge_collections (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    -- Deleting a folder moves what it holds up a level (the API does it);
    -- the cascade only runs when the workspace goes.
    parent_id    uuid REFERENCES knowledge_collections (id) ON DELETE CASCADE,
    name         text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    description  text NOT NULL DEFAULT '' CHECK (length(description) <= 1000),
    position     integer NOT NULL DEFAULT 0,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX knowledge_collections_workspace ON knowledge_collections (workspace_id, parent_id, position);

-- Items. kind article: a title and a Markdown body. kind snippet: a quick
-- answer, the question as title and the answer as body.
CREATE TABLE knowledge_items (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid REFERENCES brands (id) ON DELETE CASCADE,
    collection_id uuid REFERENCES knowledge_collections (id) ON DELETE SET NULL,
    kind          text NOT NULL CHECK (kind IN ('article', 'snippet')),
    title         text NOT NULL CHECK (length(title) BETWEEN 1 AND 300),
    body          text NOT NULL DEFAULT '' CHECK (length(body) <= 200000),
    -- public: the help centre, and AI may tell customers.
    -- customers_ai: AI may use it to answer customers; not published.
    -- team: staff and AI working for staff only, never told to a customer.
    visibility    text NOT NULL DEFAULT 'team' CHECK (visibility IN ('public', 'customers_ai', 'team')),
    -- draft: kept, but nothing reads it but people editing it.
    status        text NOT NULL DEFAULT 'published' CHECK (status IN ('draft', 'published')),
    version       integer NOT NULL DEFAULT 1,
    -- Who keeps it right, and when to check it again.
    owner_id      uuid REFERENCES actors (id) ON DELETE SET NULL,
    review_by     date,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    -- In the bin: hidden from everything but the bin, and deleted for good
    -- 30 days later.
    deleted_at    timestamptz,
    deleted_by    uuid REFERENCES actors (id) ON DELETE SET NULL
);
CREATE INDEX knowledge_items_workspace ON knowledge_items (workspace_id, collection_id, updated_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX knowledge_items_bin ON knowledge_items (deleted_at) WHERE deleted_at IS NOT NULL;
-- Word search, like workspace search (0200): 'simple' so names and any
-- language match as written.
CREATE INDEX knowledge_items_search ON knowledge_items USING gin (
    (setweight(to_tsvector('simple', title), 'A') || setweight(to_tsvector('simple', left(body, 100000)), 'C'))
) WHERE deleted_at IS NULL;

-- Every saved version of an item's text, so a change can be seen and
-- undone.
CREATE TABLE knowledge_versions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    item_id      uuid NOT NULL REFERENCES knowledge_items (id) ON DELETE CASCADE,
    version      integer NOT NULL,
    title        text NOT NULL,
    body         text NOT NULL,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (item_id, version)
);
CREATE INDEX knowledge_versions_workspace ON knowledge_versions (workspace_id, item_id);
