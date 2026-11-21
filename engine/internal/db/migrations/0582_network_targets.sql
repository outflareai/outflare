-- Network targets (plans/ad-tracker.md, B8): the state and bid a network
-- (Zeropark first, on the workspace's own token) holds for each target of
-- the campaigns a traffic source's clicks came from, read when its cost
-- is. The Targets table shows them beside each target's results, so a
-- target paused in the network's own panel reads as paused here, and a
-- bid can be seen before it is changed.

-- One row per traffic source, network campaign and target.
CREATE TABLE tracker_network_targets (
    id             bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    source_id      uuid NOT NULL REFERENCES tracker_traffic_sources (id) ON DELETE CASCADE,
    -- The network (zeropark), named by its traffic-source template; the
    -- engine checks the name.
    network        text NOT NULL CHECK (length(network) BETWEEN 1 AND 40),
    -- The network's campaign id, as the clicks carry it.
    campaign_ref   text NOT NULL CHECK (length(campaign_ref) BETWEEN 1 AND 100),
    -- The network's own id for the target.
    target_ref     text NOT NULL CHECK (length(target_ref) BETWEEN 1 AND 300),
    -- Every value a click can carry for the target (its name, address and
    -- id), so a click's target finds its row whichever the campaign URL
    -- passed.
    keys           text[] NOT NULL DEFAULT '{}',
    -- active, paused (the advertiser paused it) or stopped (the network
    -- holds it back; state_detail has the network's own word). The engine
    -- checks the names.
    state          text NOT NULL DEFAULT 'active' CHECK (length(state) BETWEEN 1 AND 40),
    state_detail   text NOT NULL DEFAULT '' CHECK (length(state_detail) <= 60),
    -- The bid in millionths of the network account's currency; null when
    -- the network showed none. auto_bid says the network sets it itself.
    bid_micros     bigint CHECK (bid_micros >= 0),
    auto_bid       boolean NOT NULL DEFAULT false,
    min_bid_micros bigint CHECK (min_bid_micros >= 0),
    -- When the network last listed the target.
    seen_at        timestamptz NOT NULL DEFAULT now(),
    UNIQUE (source_id, campaign_ref, target_ref)
);
CREATE INDEX tracker_network_targets_brand_idx ON tracker_network_targets (brand_id);
CREATE INDEX tracker_network_targets_ws_idx ON tracker_network_targets (workspace_id);
