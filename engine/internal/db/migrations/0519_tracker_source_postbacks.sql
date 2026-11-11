-- Conversions back to traffic sources (plans/ad-tracker.md, T7): a
-- traffic source with a postback URL is told of each tracker conversion on
-- its campaigns' clicks, by its own rules: the share of the payout to send
-- and its decimals, a minimum payout, the conversion types and statuses to
-- send, and whether a change of status or payout is sent again. Each send is
-- kept, once per state of the conversion, with what the network answered.
-- Only adds.
ALTER TABLE tracker_traffic_sources ADD COLUMN postback_share integer NOT NULL DEFAULT 100 CHECK (postback_share BETWEEN 0 AND 1000);
ALTER TABLE tracker_traffic_sources ADD COLUMN postback_decimals integer NOT NULL DEFAULT 2 CHECK (postback_decimals BETWEEN 0 AND 6);
ALTER TABLE tracker_traffic_sources ADD COLUMN postback_min_micros bigint NOT NULL DEFAULT 0 CHECK (postback_min_micros >= 0);
-- Types sent; empty for every type.
ALTER TABLE tracker_traffic_sources ADD COLUMN postback_types text[] NOT NULL DEFAULT '{}';
ALTER TABLE tracker_traffic_sources ADD COLUMN postback_statuses text[] NOT NULL DEFAULT '{approved}';
ALTER TABLE tracker_traffic_sources ADD COLUMN postback_resend boolean NOT NULL DEFAULT false;

CREATE TABLE tracker_source_postbacks (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    source_id     uuid NOT NULL REFERENCES tracker_traffic_sources (id) ON DELETE CASCADE,
    conversion_id uuid NOT NULL REFERENCES tracker_conversions (id) ON DELETE CASCADE,
    -- The conversion's status, type and payout when it was queued: each
    -- state is sent once.
    state         text NOT NULL CHECK (length(state) <= 120),
    -- The payout sent (the source's share of it), in millionths.
    payout_micros bigint NOT NULL DEFAULT 0,
    status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'failed')),
    method        text NOT NULL DEFAULT 'GET' CHECK (method IN ('GET', 'POST')),
    -- The URL called, filled in.
    url           text NOT NULL DEFAULT '' CHECK (length(url) <= 2000),
    http_status   integer,
    error         text NOT NULL DEFAULT '' CHECK (length(error) <= 300),
    attempts      integer NOT NULL DEFAULT 0,
    sent_at       timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (conversion_id, state)
);
CREATE INDEX tracker_source_postbacks_source_idx ON tracker_source_postbacks (source_id, created_at DESC);
