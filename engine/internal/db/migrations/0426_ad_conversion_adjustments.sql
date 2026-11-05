-- Refunds reach the ad platforms: when a payment whose conversion was sent
-- is refunded, Outflare changes that conversion on the platform (Google's
-- conversion adjustments), so bidding learns from net revenue. A partial
-- refund restates the conversion's value to what is left; a full one
-- retracts it. One row per refund and conversion sent, with how it went.

CREATE TABLE ad_conversion_adjustments (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The conversion sent, and the payment.refunded event that changes it.
    -- Outflare always sets send_id; it may be null only so that the brand
    -- sweep test can fill this table before ad_conversion_sends carries
    -- brand_id (plans/brands/README.md). A row without one is never sent.
    send_id         uuid REFERENCES ad_conversion_sends (id) ON DELETE CASCADE,
    refund_event_id uuid NOT NULL REFERENCES customer_events (id) ON DELETE CASCADE,
    -- restatement: the conversion's value becomes value_micros; retraction:
    -- the conversion is taken away.
    kind            text NOT NULL CHECK (kind IN ('restatement', 'retraction')),
    value_micros    bigint CHECK (value_micros >= 0),
    currency        text NOT NULL DEFAULT '',
    occurred_at     timestamptz NOT NULL,
    -- queued, sent, skipped (the platform can't take adjustments, or the
    -- conversion had no value), failed (refused, or not reached in time).
    status          text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'skipped', 'failed')),
    reason          text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    attempts        integer NOT NULL DEFAULT 0,
    -- A platform may not know a conversion for a few hours after it was
    -- sent, so a refused adjustment is tried again later.
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    sent_at         timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (send_id, refund_event_id)
);
CREATE INDEX ad_conversion_adjustments_brand_idx ON ad_conversion_adjustments (brand_id, created_at DESC);
CREATE INDEX ad_conversion_adjustments_queued_idx ON ad_conversion_adjustments (next_attempt_at) WHERE status = 'queued';
