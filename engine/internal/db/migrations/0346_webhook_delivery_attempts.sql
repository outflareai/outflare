-- Every try of a webhook delivery, not only the last: when, the receiver's
-- status code or what went wrong, how long it took, and whether a person
-- sent it (a redelivery or a test). They go with their delivery after 30
-- days. The brand is the endpoint's.
CREATE TABLE webhook_delivery_attempts (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    delivery_id  bigint NOT NULL REFERENCES webhook_deliveries (id) ON DELETE CASCADE,
    endpoint_id  uuid NOT NULL REFERENCES webhook_endpoints (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    status_code  integer,
    error        text CHECK (length(error) <= 500),
    duration_ms  integer NOT NULL DEFAULT 0,
    manual       boolean NOT NULL DEFAULT false,
    ok           boolean NOT NULL,
    at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX webhook_delivery_attempts_delivery ON webhook_delivery_attempts (delivery_id, id);
CREATE INDEX webhook_delivery_attempts_endpoint ON webhook_delivery_attempts (endpoint_id, at);
