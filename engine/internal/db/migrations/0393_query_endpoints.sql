-- Published query endpoints (plans/customer-data-platform, step 6,
-- governance): a saved query, pinned to one version, that another system
-- calls with an Outflare API token to read its rows. The owner's decisions
-- of 2026-11-01 and 2026-11-02: only owners and admins publish one; it is
-- called with an API token holding query.endpoints and limited to its
-- brand (no secret links); every call is logged, with a rate limit and a
-- row cap.
--
-- Expand only (docs/migrations.md): new tables and indexes.

CREATE TABLE query_endpoints (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    saved_query_id  uuid NOT NULL REFERENCES saved_queries (id) ON DELETE CASCADE,
    -- The saved query's version it runs: a later edit of the query changes
    -- nothing here until someone moves the endpoint to it.
    version         integer NOT NULL CHECK (version > 0),
    -- Its address: POST /v1/brands/{brand}/query-endpoints/{slug}/run.
    slug            text NOT NULL,
    name            text NOT NULL,
    description     text NOT NULL DEFAULT '',
    -- The parameters callers send, bound in order as $1, $2, …:
    -- [{"name", "type", "required", "default"}].
    params          jsonb NOT NULL DEFAULT '[]',
    -- What it may read: the read scopes of the person who published it,
    -- fixed then, so a caller needs only query.endpoints.
    scopes          text[] NOT NULL,
    -- The most rows one call returns, and calls a minute per token.
    row_cap         integer NOT NULL DEFAULT 1000,
    rate_per_minute integer NOT NULL DEFAULT 60,
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX query_endpoints_slug_idx ON query_endpoints (brand_id, slug);
CREATE INDEX query_endpoints_query_idx ON query_endpoints (saved_query_id);

-- Every call, allowed or not: who called (the token), with what, and what
-- came back.
CREATE TABLE query_endpoint_calls (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    endpoint_id  uuid NOT NULL REFERENCES query_endpoints (id) ON DELETE CASCADE,
    version      integer NOT NULL,
    token_id     uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    actor_id     uuid REFERENCES actors (id) ON DELETE SET NULL,
    -- success, failure (refused: bad parameters, rate limit) or error.
    outcome      text NOT NULL CHECK (outcome IN ('success', 'failure', 'error')),
    error_code   text,
    rows         integer NOT NULL DEFAULT 0,
    truncated    boolean NOT NULL DEFAULT false,
    duration_ms  integer NOT NULL DEFAULT 0,
    -- The parameters as sent.
    params       jsonb NOT NULL DEFAULT '{}',
    request_id   text,
    at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX query_endpoint_calls_endpoint_idx ON query_endpoint_calls (endpoint_id, at DESC, id DESC);
CREATE INDEX query_endpoint_calls_token_idx ON query_endpoint_calls (endpoint_id, token_id, at);
