-- Webhooks: URLs a workspace registers to hear about events, and every
-- delivery made to them (kept 30 days).
CREATE TABLE webhook_endpoints (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    url              text NOT NULL,
    description      text NOT NULL DEFAULT '',
    events           text[] NOT NULL,
    secret           bytea NOT NULL, -- sealed with the endpoint id
    -- After a rotation the old secret signs too, for a day, so receivers
    -- can switch over without dropping deliveries.
    previous_secret            bytea,
    previous_secret_expires_at timestamptz,
    enabled          boolean NOT NULL DEFAULT true,
    disabled_reason  text,
    failing_since    timestamptz,
    last_success_at  timestamptz,
    created_by       uuid,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_endpoints_workspace_idx ON webhook_endpoints (workspace_id) WHERE enabled;

CREATE TABLE webhook_deliveries (
    id                bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    endpoint_id       uuid NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    event_id          text NOT NULL,
    event_type        text NOT NULL,
    payload           jsonb NOT NULL,
    status            text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'delivered', 'failed', 'cancelled')),
    attempts          int NOT NULL DEFAULT 0,
    last_status_code  int,
    last_error        text,
    last_attempt_at   timestamptz,
    delivered_at      timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_deliveries_endpoint_idx ON webhook_deliveries (endpoint_id, id DESC);
CREATE INDEX webhook_deliveries_created_idx ON webhook_deliveries (created_at);
