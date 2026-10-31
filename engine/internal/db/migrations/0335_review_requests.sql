-- Brand review invitations share lifecycle email, customer records and journeys.
CREATE TABLE review_request_settings (
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid PRIMARY KEY REFERENCES brands(id) ON DELETE CASCADE,
    config jsonb NOT NULL DEFAULT '{}'
);
CREATE TABLE review_request_campaigns (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    config jsonb NOT NULL,
    status text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft','active','paused','archived')),
    send_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX review_request_campaigns_brand ON review_request_campaigns(workspace_id,brand_id,created_at DESC);
CREATE TABLE review_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
    campaign_id uuid NOT NULL REFERENCES review_request_campaigns(id) ON DELETE CASCADE,
    record_id uuid NOT NULL REFERENCES records(id) ON DELETE CASCADE,
    -- A launch/customer or journey run/step: retries never ask twice.
    source_key text NOT NULL,
    platforms text[] NOT NULL DEFAULT '{}',
    destinations jsonb NOT NULL DEFAULT '[]',
    -- Snapshot of the configuration that produced this request.
    config jsonb NOT NULL,
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','queued','skipped','failed')),
    reason text NOT NULL DEFAULT '',
    email_id uuid REFERENCES emails(id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (campaign_id,source_key)
);
CREATE INDEX review_requests_pending ON review_requests(workspace_id,brand_id,campaign_id) WHERE status = 'pending';
CREATE INDEX review_requests_frequency ON review_requests(workspace_id,brand_id,record_id,created_at DESC);
