-- Tracker conversions by click id (plans/ad-tracker.md, T5a, decision 3:
-- a click ledger for conversions with no person).
--
-- A postback source is where a brand's conversions come from (an affiliate
-- network, an offer's own tracker, the brand's backend): a secret postback
-- URL on the brand's tracking domain, with an optional IP allowlist, how
-- long after a click a conversion still counts, and what a repeated
-- transaction id does. Each postback that names a redirect click (its key,
-- ofclk) writes a conversion to the ledger, with the person's record when
-- the click's visit knows them. Only adds, so the version before still works.
CREATE TABLE tracker_postback_sources (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id          uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    name              text NOT NULL CHECK (length(name) BETWEEN 1 AND 120),
    -- The secret in the postback URL, hashed; the hint is its first
    -- characters, to tell sources apart.
    token_hash        bytea NOT NULL UNIQUE,
    token_hint        text NOT NULL DEFAULT '' CHECK (length(token_hint) <= 40),
    -- Addresses or ranges a postback must come from; empty for any.
    ip_allowlist      text[] NOT NULL DEFAULT '{}',
    -- What a transaction id seen before does: ignore it, record a new
    -- conversion, update the one recorded, or add its payout (upsells).
    duplicate_policy  text NOT NULL DEFAULT 'ignore' CHECK (duplicate_policy IN ('ignore', 'new', 'update', 'add')),
    click_expiry_days integer NOT NULL DEFAULT 30 CHECK (click_expiry_days BETWEEN 1 AND 365),
    currency          text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX tracker_postback_sources_name_idx ON tracker_postback_sources (workspace_id, brand_id, lower(name));

CREATE TABLE tracker_conversions (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    source_id      uuid NOT NULL REFERENCES tracker_postback_sources (id) ON DELETE CASCADE,
    -- The redirect click it credits (tracking_link_clicks.id; not a foreign
    -- key, as the click table is large) and what the click was on.
    click_id       bigint NOT NULL,
    click_key      text NOT NULL CHECK (length(click_key) <= 32),
    campaign_id    uuid REFERENCES tracking_campaigns (id) ON DELETE SET NULL,
    link_id        uuid,
    clicked_at     timestamptz NOT NULL,
    -- The network's transaction id, or the click key and type when it sent
    -- none; repeats follow the source's duplicate policy.
    transaction_id text NOT NULL CHECK (length(transaction_id) BETWEEN 1 AND 200),
    type           text NOT NULL DEFAULT 'conversion' CHECK (type ~ '^[a-z0-9_.-]{1,40}$'),
    status         text NOT NULL DEFAULT 'approved' CHECK (status IN ('pending', 'approved', 'declined')),
    -- Millionths of the currency.
    payout_micros  bigint NOT NULL DEFAULT 0 CHECK (payout_micros >= 0),
    currency       text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    -- Up to 20 values the postback carried (p1 to p20).
    params         jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(params) = 'object'),
    -- The person, when the click's visit knew them.
    record_id      uuid REFERENCES records (id) ON DELETE SET NULL,
    postbacks      integer NOT NULL DEFAULT 1,
    converted_at   timestamptz NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX tracker_conversions_tx_idx ON tracker_conversions (source_id, transaction_id);
CREATE INDEX tracker_conversions_brand_idx ON tracker_conversions (workspace_id, brand_id, converted_at DESC);
CREATE INDEX tracker_conversions_campaign_idx ON tracker_conversions (campaign_id, converted_at) WHERE campaign_id IS NOT NULL;
CREATE INDEX tracker_conversions_record_idx ON tracker_conversions (record_id) WHERE record_id IS NOT NULL;
