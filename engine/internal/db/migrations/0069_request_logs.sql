-- Request logs: one row for every API call made in a workspace, from the
-- web app, the API, MCP, the CLI or an agent's mission, for the Logs page.
-- Only what describes the call is kept: never request or response bodies,
-- query values, headers, keys or IP addresses. Rows are deleted after the
-- retention period (30 days unless OUTFLARE_LOG_RETENTION_DAYS says
-- otherwise).
CREATE TABLE request_logs (
    id           bigserial PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    request_id   text NOT NULL,
    method       text NOT NULL,
    -- The route that served it ("GET /v1/workspaces/{ws}/leads/{id}") and
    -- its operationId in api/openapi.yaml.
    route        text NOT NULL,
    operation    text NOT NULL,
    path         text NOT NULL,
    query_keys   text[] NOT NULL DEFAULT '{}',
    status       integer NOT NULL,
    duration_ms  integer NOT NULL,
    error_code   text NOT NULL DEFAULT '',
    -- The field an invalid request named ("email"), never its value, or
    -- the scope a refused call was missing ("leads.manage").
    error_param  text NOT NULL DEFAULT '',
    -- web, api, mcp, cli, sdk or mission; the client and MCP tool its
    -- headers named, and the agent's app.
    surface      text NOT NULL,
    client       text NOT NULL DEFAULT '',
    mcp_tool     text NOT NULL DEFAULT '',
    agent        text NOT NULL DEFAULT '',
    user_agent   text NOT NULL DEFAULT '',
    mission_id   uuid,
    actor_id     uuid,
    actor_type   text NOT NULL DEFAULT '',
    token_id     uuid,
    -- The brand and project the call was about, when it said.
    brand_id     uuid,
    project_id   uuid,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX request_logs_ws_idx ON request_logs (workspace_id, id DESC);
CREATE INDEX request_logs_request_idx ON request_logs (request_id);
CREATE INDEX request_logs_errors_idx ON request_logs (workspace_id, id DESC) WHERE status >= 400;
CREATE INDEX request_logs_created_idx ON request_logs (created_at);
