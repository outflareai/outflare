-- Offers (plans/ad-tracker.md, T6a): the pages a brand sends tracker clicks
-- to on an affiliate network or its own site, each with its payout (fixed,
-- by country, or what the postback says), a daily cap on clicks or
-- conversions with a fallback offer, and the network it pays through (a
-- postback source). A link's destination can be an offer: its URL is then
-- the offer's, kept in step, so the version before still sends clicks there.
-- Only adds.

-- The affiliate-network template a postback source follows
-- (integrations/affiliate-networks; '' for a custom one), and the parameter
-- in its offer links that carries the click id ('' leaves offer links as
-- they are).
ALTER TABLE tracker_postback_sources ADD COLUMN network text NOT NULL DEFAULT '' CHECK (network ~ '^[a-z0-9_]{0,40}$');
ALTER TABLE tracker_postback_sources ADD COLUMN click_id_param text NOT NULL DEFAULT '' CHECK (length(click_id_param) <= 64);

CREATE TABLE tracker_offers (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    -- The network it pays through; NULL for none (the brand's own offer).
    source_id         uuid REFERENCES tracker_postback_sources (id) ON DELETE SET NULL,
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    url               text NOT NULL CHECK (length(url) BETWEEN 1 AND 2000),
    -- postback: what the postback says, else the payout here; fixed: the
    -- payout here, whatever the postback says.
    payout_mode       text NOT NULL DEFAULT 'postback' CHECK (payout_mode IN ('postback', 'fixed')),
    -- Millionths of the currency.
    payout_micros     bigint NOT NULL DEFAULT 0 CHECK (payout_micros >= 0),
    currency          text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    -- Payouts by the click's country, {"US": 2500000}, before payout_micros.
    country_payouts   jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(country_payouts) = 'object'),
    -- The conversion type a postback without one gets; '' leaves it.
    conversion_type   text NOT NULL DEFAULT '' CHECK (conversion_type ~ '^[a-z0-9_.-]{0,40}$'),
    -- How many clicks or conversions a day it takes; NULL for no cap, 0
    -- stops it. Once capped, clicks go to the fallback offer.
    daily_cap         integer CHECK (daily_cap >= 0),
    cap_kind          text NOT NULL DEFAULT 'conversions' CHECK (cap_kind IN ('clicks', 'conversions')),
    fallback_offer_id uuid REFERENCES tracker_offers (id) ON DELETE SET NULL,
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tracker_offers_name_idx ON tracker_offers (workspace_id, brand_id, lower(name));
CREATE INDEX tracker_offers_source_idx ON tracker_offers (source_id) WHERE source_id IS NOT NULL;

-- Clicks and conversions each offer had on a day (UTC until brands have a
-- reporting time zone), so a cap holds across servers.
CREATE TABLE tracker_offer_days (
    offer_id     uuid NOT NULL REFERENCES tracker_offers (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    day          date NOT NULL,
    clicks       bigint NOT NULL DEFAULT 0 CHECK (clicks >= 0),
    conversions  bigint NOT NULL DEFAULT 0 CHECK (conversions >= 0),
    PRIMARY KEY (offer_id, day)
);
CREATE INDEX tracker_offer_days_day_idx ON tracker_offer_days (day);

-- A destination that is an offer. Its url is the offer's, updated with it.
ALTER TABLE tracking_link_destinations ADD COLUMN offer_id uuid REFERENCES tracker_offers (id) ON DELETE SET NULL;
CREATE INDEX tracking_link_destinations_offer_idx ON tracking_link_destinations (offer_id) WHERE offer_id IS NOT NULL;

-- The offer a click went to (a fallback offer when its offer was capped).
-- Not a foreign key, as the click table is large.
ALTER TABLE tracking_link_clicks ADD COLUMN offer_id uuid;

-- The offer a conversion's click went to.
ALTER TABLE tracker_conversions ADD COLUMN offer_id uuid REFERENCES tracker_offers (id) ON DELETE SET NULL;
CREATE INDEX tracker_conversions_offer_idx ON tracker_conversions (offer_id, converted_at) WHERE offer_id IS NOT NULL;
