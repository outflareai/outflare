-- Network cost (plans/ad-tracker.md, Z2; the owner's decisions 16 and
-- 17): each day, a network's spend per campaign and target (Zeropark
-- first, on the workspace's own token) is shared out over the tracker's
-- clicks from that campaign and target, in proportion to the cost each
-- click carried ({visit_cost}), evenly where it carried none, so every
-- report has cost and the totals match the network's exactly.

-- A click's share of what the network reported spending; the tracker
-- report uses it before the click's own cost token. Millionths of the
-- traffic source's currency.
ALTER TABLE tracking_link_clicks ADD COLUMN network_cost_micros bigint CHECK (network_cost_micros >= 0);

-- One row per traffic source, network campaign and day (in the network
-- account's time zone): what the network reported and where it went.
CREATE TABLE tracker_network_costs (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    source_id      uuid NOT NULL REFERENCES tracker_traffic_sources (id) ON DELETE CASCADE,
    integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The network (zeropark), named by its traffic-source template; the
    -- engine checks the name.
    network        text NOT NULL CHECK (length(network) BETWEEN 1 AND 40),
    -- The network's campaign id, as the clicks carry it.
    campaign_ref   text NOT NULL CHECK (length(campaign_ref) BETWEEN 1 AND 100),
    day            date NOT NULL,
    currency       text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- What the network reported, and how it was shared: on the clicks of
    -- the target that spent it, evenly over the campaign's clicks that
    -- named no reported target, or on no click at all.
    spend_micros        bigint NOT NULL DEFAULT 0 CHECK (spend_micros >= 0),
    matched_micros      bigint NOT NULL DEFAULT 0 CHECK (matched_micros >= 0),
    spread_micros       bigint NOT NULL DEFAULT 0 CHECK (spread_micros >= 0),
    unattributed_micros bigint NOT NULL DEFAULT 0 CHECK (unattributed_micros >= 0),
    clicks         integer NOT NULL DEFAULT 0,
    targets        integer NOT NULL DEFAULT 0,
    synced_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, campaign_ref, day)
);
CREATE INDEX tracker_network_costs_brand_idx ON tracker_network_costs (brand_id, day);
CREATE INDEX tracker_network_costs_ws_idx ON tracker_network_costs (workspace_id);
