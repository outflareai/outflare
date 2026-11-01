-- The inbound conversion log: every conversion call a workspace's backend
-- or tracker made (the API or a postback URL), with what it sent and what
-- Outflare answered, so a team can see why one wasn't credited. The
-- customer is the record it resolved to; their email is never copied
-- here, only a hash of it to search by. Rows are kept 30 days.
CREATE TABLE partner_conversion_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The program's brand, when the call named or found a program.
    brand_id      uuid REFERENCES brands (id) ON DELETE CASCADE,
    program_id    uuid REFERENCES partner_programs (id) ON DELETE CASCADE,
    record_id     uuid REFERENCES records (id) ON DELETE SET NULL,
    conversion_id uuid REFERENCES partner_conversions (id) ON DELETE SET NULL,
    source        text NOT NULL CHECK (source IN ('api', 'postback')),
    type          text NOT NULL DEFAULT '' CHECK (length(type) <= 40),
    -- What the call sent, as Outflare read it (email left out).
    request       jsonb NOT NULL DEFAULT '{}',
    email_hash    text NOT NULL DEFAULT '',
    status        integer NOT NULL,
    -- credited, not_credited (with reason), duplicate, rejected (bad input)
    -- or error.
    outcome       text NOT NULL CHECK (outcome IN ('credited', 'not_credited', 'duplicate', 'rejected', 'error')),
    reason        text NOT NULL DEFAULT '' CHECK (length(reason) <= 60),
    response      jsonb NOT NULL DEFAULT '{}',
    duration_ms   integer NOT NULL DEFAULT 0,
    request_id    text NOT NULL DEFAULT '' CHECK (length(request_id) <= 100),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX partner_conversion_log_recent ON partner_conversion_log (workspace_id, created_at DESC);
CREATE INDEX partner_conversion_log_prune ON partner_conversion_log (created_at);
