-- Each conversion once it has been credited, and its credits under every
-- model, so switching the model in a report needs no recomputing.
CREATE TABLE attribution_conversions (
    event_id     uuid PRIMARY KEY REFERENCES customer_events (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid REFERENCES records (id) ON DELETE SET NULL,
    brand_id     uuid REFERENCES brands (id) ON DELETE SET NULL,
    name         text NOT NULL,
    occurred_at  timestamptz NOT NULL,
    -- Money in minor units (cents); a refund is negative. Null: no value.
    value_minor  bigint,
    currency     text NOT NULL DEFAULT '' CHECK (currency ~ '^([A-Z]{3})?$'),
    -- The visits it was matched to, and how: the person's record or only
    -- the browser's anonymous id.
    touches      integer NOT NULL DEFAULT 0,
    matched_by   text NOT NULL DEFAULT 'none' CHECK (matched_by IN ('record', 'anonymous', 'none')),
    credited_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX attribution_conversions_ws_idx ON attribution_conversions (workspace_id, occurred_at DESC);

-- One row per touch that got credit under a model. A conversion with no
-- visit gets one 'direct' row with no session.
CREATE TABLE attribution_credits (
    event_id             uuid NOT NULL REFERENCES attribution_conversions (event_id) ON DELETE CASCADE,
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    model                text NOT NULL CHECK (model IN ('last_non_direct', 'first_touch', 'linear', 'position', 'time_decay')),
    position             integer NOT NULL CHECK (position >= 0),
    session_id           uuid REFERENCES site_sessions (id) ON DELETE SET NULL,
    touch_at             timestamptz,
    channel              text NOT NULL,
    source               text NOT NULL DEFAULT '',
    campaign             text NOT NULL DEFAULT '',
    tracking_campaign_id uuid REFERENCES tracking_campaigns (id) ON DELETE SET NULL,
    tag_method           text NOT NULL DEFAULT '',
    -- The touch's share of the conversion, 0 to 1; the shares of one
    -- conversion under one model add up to 1.
    weight               numeric(7, 6) NOT NULL CHECK (weight > 0 AND weight <= 1),
    PRIMARY KEY (event_id, model, position)
);
CREATE INDEX attribution_credits_report_idx ON attribution_credits (workspace_id, model, channel);
CREATE INDEX attribution_credits_campaign_idx ON attribution_credits (tracking_campaign_id) WHERE tracking_campaign_id IS NOT NULL;
