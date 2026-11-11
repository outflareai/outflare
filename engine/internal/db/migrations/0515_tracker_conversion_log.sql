-- The tracker conversion log (plans/ad-tracker.md, T5c): every postback and
-- pixel call a brand's postback sources made, with what they sent, what
-- Outflare answered and why, so the brand can see why a network's
-- conversion wasn't recorded. Calls with an unknown secret have no brand and
-- aren't kept. Rows are kept 30 days. Only adds.
CREATE TABLE tracker_conversion_log (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    source_id     uuid NOT NULL REFERENCES tracker_postback_sources (id) ON DELETE CASCADE,
    conversion_id uuid REFERENCES tracker_conversions (id) ON DELETE SET NULL,
    via           text NOT NULL CHECK (via IN ('postback', 'pixel')),
    method        text NOT NULL DEFAULT 'GET' CHECK (method IN ('GET', 'POST')),
    -- The parameters it sent, as Outflare read them (the secret is in the
    -- path, never here).
    request       jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(request) = 'object'),
    click_key     text NOT NULL DEFAULT '' CHECK (length(click_key) <= 32),
    status        integer NOT NULL,
    -- accepted, duplicate, updated, or why it was refused: wrong_domain,
    -- ip_refused, invalid, unknown_click, expired_click, rate_limited, error.
    outcome       text NOT NULL CHECK (length(outcome) BETWEEN 1 AND 40),
    reason        text NOT NULL DEFAULT '' CHECK (length(reason) <= 300),
    duration_ms   integer NOT NULL DEFAULT 0,
    request_id    text NOT NULL DEFAULT '' CHECK (length(request_id) <= 100),
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_conversion_log_recent ON tracker_conversion_log (workspace_id, brand_id, created_at DESC);
CREATE INDEX tracker_conversion_log_prune ON tracker_conversion_log (created_at);
