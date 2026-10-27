-- Meaning-based search: search finds records, replies, meetings and deals
-- by what they mean as well as by their words. Each one is turned into a
-- vector (an embedding) with the workspace's own AI key, and search
-- compares the vectors with pgvector.
--
-- pgvector is a Postgres extension, and not every database has it. So
-- the vectors' table is made by semantic_search_setup(), which this
-- migration runs once and the engine runs again at start-up: it makes the
-- table where pgvector is installed or can be, and otherwise does nothing
-- and says so. A database without pgvector migrates and runs as before,
-- with meaning-based search off.

-- Whether meaning-based search is on in the workspace. It is on unless an
-- admin turns it off (no row means on): it runs once the workspace connects
-- an AI model that makes embeddings, and spends a little on that provider
-- bill, which Settings and AI usage explain.
CREATE TABLE semantic_search_settings (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    enabled      boolean NOT NULL DEFAULT true,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid
);

-- semantic_search_setup makes the vectors' table when pgvector is there
-- (or can be added), and reports whether the table exists.
CREATE FUNCTION semantic_search_setup() RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF to_regclass('search_embeddings') IS NOT NULL THEN
        RETURN true;
    END IF;
    -- Servers starting together set up once.
    PERFORM pg_advisory_xact_lock(hashtext('semantic_search_setup'));
    IF to_regclass('search_embeddings') IS NOT NULL THEN
        RETURN true;
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
        IF NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
            RETURN false;
        END IF;
        BEGIN
            CREATE EXTENSION IF NOT EXISTS vector;
        EXCEPTION WHEN OTHERS THEN
            -- Usually the database user may not add extensions. An owner
            -- can run CREATE EXTENSION vector; the engine checks again.
            RAISE NOTICE 'pgvector could not be added: %', SQLERRM;
            RETURN false;
        END;
    END IF;
    -- One vector per item: a record, a reply (an inbound message), a
    -- meeting or a deal. It holds no text, only a hash of the text it was
    -- made from, so a changed item is embedded again and an unchanged one
    -- isn't. Vectors from another model are never compared: model says
    -- which made it.
    EXECUTE $ddl$
        CREATE TABLE search_embeddings (
            workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
            kind         text NOT NULL CHECK (kind IN ('record', 'message', 'meeting', 'deal')),
            item_id      uuid NOT NULL,
            model        text NOT NULL,
            content_hash text NOT NULL,
            embedding    vector(1024) NOT NULL,
            embedded_at  timestamptz NOT NULL DEFAULT now(),
            PRIMARY KEY (workspace_id, kind, item_id)
        )
    $ddl$;
    EXECUTE 'CREATE INDEX search_embeddings_hnsw_idx ON search_embeddings USING hnsw (embedding vector_cosine_ops)';
    RETURN true;
END
$$;

SELECT semantic_search_setup();
