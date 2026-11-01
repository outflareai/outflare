-- Query Studio (plans/brands/README.md, "Data pages"): saved, versioned
-- queries, one brand's each. A saved query is private to the person who
-- saved it, or shared with everyone who can query the brand. Every change
-- to its SQL or chart is a new version, so an old one can be read or
-- restored; renaming it isn't.
--
-- Rollback-safe: two new tables, which the previous version never reads.

CREATE TABLE saved_queries (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    description     text NOT NULL DEFAULT '' CHECK (length(description) <= 2000),
    -- private: only its owner sees it; brand: everyone who may query the
    -- brand sees and runs it.
    visibility      text NOT NULL DEFAULT 'private' CHECK (visibility IN ('private', 'brand')),
    -- The person it belongs to (for a token, the person who made it).
    owner_id        uuid REFERENCES users (id) ON DELETE SET NULL,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    current_version integer NOT NULL DEFAULT 1 CHECK (current_version > 0),
    archived_at     timestamptz,
    archived_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX saved_queries_brand ON saved_queries (brand_id, updated_at DESC) WHERE archived_at IS NULL;
CREATE INDEX saved_queries_workspace ON saved_queries (workspace_id);

CREATE TABLE saved_query_versions (
    query_id      uuid NOT NULL REFERENCES saved_queries (id) ON DELETE CASCADE,
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    version       integer NOT NULL CHECK (version > 0),
    sql           text NOT NULL CHECK (length(sql) BETWEEN 1 AND 20000),
    -- How the results are drawn: {"type": "bar"|"line"|"area", "x": col,
    -- "y": [cols]}, or null for a table.
    chart         jsonb CHECK (chart IS NULL OR jsonb_typeof(chart) = 'object'),
    note          text NOT NULL DEFAULT '' CHECK (length(note) <= 500),
    -- The version this one restored, if it was a restore.
    restored_from integer,
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (query_id, version)
);

CREATE INDEX saved_query_versions_workspace ON saved_query_versions (workspace_id);
