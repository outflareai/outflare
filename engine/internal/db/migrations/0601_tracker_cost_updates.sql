-- Cost entered by hand (plans/ad-tracker.md, B6): what a tracker campaign
-- cost over a period, typed in or uploaded from a CSV, for networks whose
-- cost Outflare can't read. A job shares the amount out evenly over the
-- campaign's clicks in the period (tracking_link_clicks.network_cost_micros,
-- which the tracker report reads before a click's own {cost}), to the
-- millionth, so the report's total is the amount entered. Each update is
-- kept with how it went. Only adds.
CREATE TABLE tracker_cost_updates (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id      uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    campaign_id   uuid NOT NULL REFERENCES tracking_campaigns (id) ON DELETE CASCADE,
    -- The period: days in the brand's reporting time zone, both included,
    -- and the instants they span.
    from_day      date NOT NULL,
    to_day        date NOT NULL,
    starts_at     timestamptz NOT NULL,
    ends_at       timestamptz NOT NULL,
    -- total shares amount_micros out; clear puts the clicks back on the
    -- cost they carried themselves.
    mode          text NOT NULL DEFAULT 'total' CHECK (mode IN ('total', 'clear')),
    -- Millionths of the campaign's traffic source's currency.
    amount_micros bigint NOT NULL DEFAULT 0 CHECK (amount_micros >= 0),
    status        text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'applied', 'failed')),
    -- The clicks that carry it, once applied.
    clicks        bigint NOT NULL DEFAULT 0,
    error         text NOT NULL DEFAULT '' CHECK (length(error) <= 300),
    created_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    applied_at    timestamptz,
    CHECK (to_day >= from_day)
);
CREATE INDEX tracker_cost_updates_brand_idx ON tracker_cost_updates (brand_id, created_at DESC);
CREATE INDEX tracker_cost_updates_campaign_idx ON tracker_cost_updates (campaign_id, created_at DESC);
CREATE INDEX tracker_cost_updates_ws_idx ON tracker_cost_updates (workspace_id);
