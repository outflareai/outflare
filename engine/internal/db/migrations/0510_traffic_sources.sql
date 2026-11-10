-- Traffic sources and custom click fields (plans/ad-tracker.md, T3).
--
-- A traffic source is where a brand buys clicks (a network such as Taboola
-- or PropellerAds, or a custom one), usually made from a template in
-- integrations/traffic-sources/. It says which URL parameters of a click
-- carry the network's click id and cost, and which carry the values the
-- brand wants to report on, each into one of twenty custom fields. A
-- tracking campaign names its source, and its redirect clicks keep those
-- values. Only adds, so the version before still works.
CREATE TABLE tracker_traffic_sources (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    -- The template it was made from (integrations/traffic-sources/<id>.yaml),
    -- or '' for a custom source.
    template        text NOT NULL DEFAULT '' CHECK (template ~ '^[a-z0-9_]{0,40}$'),
    currency        text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- The URL parameter that carries the network's click id, and the
    -- network's token for it (what the network fills in).
    click_id_param  text NOT NULL DEFAULT '' CHECK (length(click_id_param) <= 64),
    click_id_token  text NOT NULL DEFAULT '' CHECK (length(click_id_token) <= 100),
    cost_param      text NOT NULL DEFAULT '' CHECK (length(cost_param) <= 64),
    cost_token      text NOT NULL DEFAULT '' CHECK (length(cost_token) <= 100),
    -- [{"name", "param", "token", "field" (1 to 20), "aliases": [...]}]
    parameters      jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(parameters) = 'array'),
    -- The network's conversion postback, sent from T7, with Outflare's
    -- tokens ({click_id}, {payout}, ...).
    postback_url    text NOT NULL DEFAULT '' CHECK (length(postback_url) <= 2000),
    postback_method text NOT NULL DEFAULT 'GET' CHECK (postback_method IN ('GET', 'POST')),
    status          text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tracker_traffic_sources_name_idx ON tracker_traffic_sources (workspace_id, brand_id, lower(name));

-- A campaign's traffic source, if it has one.
ALTER TABLE tracking_campaigns
    ADD COLUMN traffic_source_id uuid REFERENCES tracker_traffic_sources (id) ON DELETE SET NULL;
CREATE INDEX tracking_campaigns_traffic_source_idx ON tracking_campaigns (traffic_source_id) WHERE traffic_source_id IS NOT NULL;

-- A click's custom fields ({"1": "...", ..., "20": "..."}), the network's
-- own click id for it, and what the network says the click cost, in
-- millionths of the source's currency (0 when it doesn't say; cost models
-- read it in B6).
ALTER TABLE tracking_link_clicks
    ADD COLUMN fields          jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(fields) = 'object'),
    ADD COLUMN source_click_id text NOT NULL DEFAULT '' CHECK (length(source_click_id) <= 1000),
    ADD COLUMN cost_micros     bigint NOT NULL DEFAULT 0 CHECK (cost_micros >= 0);
