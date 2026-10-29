-- Knowledge retrieval: every AI in Outflare answers from the knowledge base.
-- Items are split into passages (chunks) that word search and
-- meaning-based search can find; each question asked of the knowledge and
-- the items each answer used are kept, for the use counts and the gaps.

-- Passages of a knowledge item: a quick answer is one, an article one per
-- section (by heading), cut to a readable length. Rebuilt whenever the
-- item's text changes; they go with the item.
CREATE TABLE knowledge_chunks (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    item_id      uuid NOT NULL REFERENCES knowledge_items (id) ON DELETE CASCADE,
    position     integer NOT NULL,
    -- The section heading it sits under, if any.
    heading      text NOT NULL DEFAULT '',
    text         text NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (item_id, position)
);
CREATE INDEX knowledge_chunks_workspace ON knowledge_chunks (workspace_id, item_id);

-- Questions asked of the knowledge (the Ask box, agents, support), with
-- whether it had an answer. The unanswered ones are the gaps to write.
CREATE TABLE knowledge_questions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    question     text NOT NULL CHECK (length(question) BETWEEN 1 AND 1000),
    -- team: for staff, every item. customers: only what customers may be told.
    audience     text NOT NULL CHECK (audience IN ('team', 'customers')),
    -- ask (the Ask box and the API), agent, mcp.
    surface      text NOT NULL DEFAULT 'ask',
    answered     boolean NOT NULL,
    asked_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- An unanswered question someone set aside (answered it, or it
    -- needs no answer): it leaves the list of unanswered questions.
    dismissed_at timestamptz
);
CREATE INDEX knowledge_questions_workspace ON knowledge_questions (workspace_id, created_at DESC);

-- Which items an answer used, for "used 34 times this month".
CREATE TABLE knowledge_uses (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    item_id      uuid NOT NULL REFERENCES knowledge_items (id) ON DELETE CASCADE,
    question_id  uuid NOT NULL REFERENCES knowledge_questions (id) ON DELETE CASCADE,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (question_id, item_id)
);
CREATE INDEX knowledge_uses_item ON knowledge_uses (workspace_id, item_id, created_at DESC);

-- Items written before this have no passages yet; the engine splits them
-- the first time it reads the workspace's knowledge.

-- Meaning-based search learns a fifth kind, knowledge passages. The table
-- may not exist (no pgvector), or be made later by semantic_search_setup,
-- so the function makes it with the wider check and widens an older one.
CREATE OR REPLACE FUNCTION semantic_search_setup() RETURNS boolean
LANGUAGE plpgsql AS $$
BEGIN
    IF to_regclass('search_embeddings') IS NULL THEN
        -- Servers starting together set up once.
        PERFORM pg_advisory_xact_lock(hashtext('semantic_search_setup'));
    END IF;
    IF to_regclass('search_embeddings') IS NULL THEN
        IF NOT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'vector') THEN
            IF NOT EXISTS (SELECT 1 FROM pg_available_extensions WHERE name = 'vector') THEN
                RETURN false;
            END IF;
            BEGIN
                CREATE EXTENSION IF NOT EXISTS vector;
            EXCEPTION WHEN OTHERS THEN
                -- Usually the database user may not add extensions. An
                -- owner can run CREATE EXTENSION vector; the engine checks
                -- again.
                RAISE NOTICE 'pgvector could not be added: %', SQLERRM;
                RETURN false;
            END;
        END IF;
        -- One vector per item: a record, a reply (an inbound message), a
        -- meeting, a deal or a knowledge passage. It holds no text, only a
        -- hash of the text it was made from. model says which made it.
        EXECUTE $ddl$
            CREATE TABLE search_embeddings (
                workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
                kind         text NOT NULL CONSTRAINT search_embeddings_kind_check CHECK (kind IN ('record', 'message', 'meeting', 'deal', 'knowledge')),
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
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'search_embeddings'::regclass AND contype = 'c'
                   AND pg_get_constraintdef(oid) LIKE '%knowledge%') THEN
        PERFORM pg_advisory_xact_lock(hashtext('semantic_search_setup'));
        EXECUTE 'ALTER TABLE search_embeddings DROP CONSTRAINT IF EXISTS search_embeddings_kind_check';
        EXECUTE $ddl$ALTER TABLE search_embeddings ADD CONSTRAINT search_embeddings_kind_check
            CHECK (kind IN ('record', 'message', 'meeting', 'deal', 'knowledge'))$ddl$;
    END IF;
    RETURN true;
END
$$;

SELECT semantic_search_setup();
