-- Idempotency-Key: a retried write returns the first answer instead of
-- running again. Keys live for 24 hours.
CREATE TABLE idempotency_keys (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    actor_id      uuid NOT NULL,
    key           text NOT NULL,
    method        text NOT NULL,
    path          text NOT NULL,
    request_hash  bytea NOT NULL,
    status        int,          -- null while the first request is still running
    content_type  text,
    body          bytea,
    created_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, actor_id, key)
);

CREATE INDEX idempotency_keys_created_idx ON idempotency_keys (created_at);
