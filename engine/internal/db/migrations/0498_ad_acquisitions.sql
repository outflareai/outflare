-- Paid ads intelligence layer (plans/paid-ads/plan.md): the ledger of
-- what the ads brought, on Outflare's own attribution. An acquisition is
-- the moment a person became a sign-up or a first-time payer because of
-- an ad click: the last visit with a paid click within the brand's
-- acquisition window before the event. Every payment and refund the
-- ledger counts is a revenue row, new (a person's first ever payment) or
-- returning (any later one), tied to the campaign that acquired the
-- person (lifetime revenue) and to the campaign whose paid click was last
-- before it (returning by click). The ledger job (engine/internal/paidads)
-- reads customer events a few minutes after they arrive; where it got to
-- for each brand is in ad_acquisition_runs.
--
-- Expand only (docs/migrations.md): three new tables and one index on
-- customer_events, which the ledger reads in order of arrival so a
-- payment synced late (a billing connector's backfill) is never missed.
-- The previous version ignores them.

-- One row per person a brand's ads acquired: the last paid click within the
-- window before their sign-up or first payment.
CREATE TABLE ad_acquisitions (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id             uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id            uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    connector            text NOT NULL,                       -- google_ads, meta_ads, ...
    ad_account_id        uuid REFERENCES ad_accounts (id) ON DELETE SET NULL,
    campaign_entity_id   uuid REFERENCES ad_entities (id) ON DELETE SET NULL,
    ad_group_entity_id   uuid REFERENCES ad_entities (id) ON DELETE SET NULL,
    ad_entity_id         uuid REFERENCES ad_entities (id) ON DELETE SET NULL,
    -- The network's own ids as the click carried them (template or click harvest).
    campaign_external_id text NOT NULL DEFAULT '',
    ad_group_external_id text NOT NULL DEFAULT '',
    ad_external_id       text NOT NULL DEFAULT '',
    keyword              text NOT NULL DEFAULT '',
    click_id_kind        text NOT NULL DEFAULT '',             -- gclid, gbraid, wbraid, msclkid, ...
    click_id             text NOT NULL DEFAULT '',
    session_id           uuid REFERENCES site_sessions (id) ON DELETE SET NULL,
    clicked_at           timestamptz NOT NULL,
    -- How the click was tied to the campaign: click_id (harvest), template, campaign_param (gad_campaignid), utm.
    matched_by           text NOT NULL DEFAULT 'template',
    window_days          integer NOT NULL,
    signed_up_at         timestamptz,
    first_paid_at        timestamptz,
    first_payment_minor  bigint,
    currency             text NOT NULL DEFAULT '' CHECK (currency ~ '^([A-Z]{3})?$'),
    ad_consent           boolean NOT NULL DEFAULT false,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, record_id)
);
CREATE INDEX ad_acquisitions_campaign_idx ON ad_acquisitions (campaign_entity_id, first_paid_at);
CREATE INDEX ad_acquisitions_brand_idx ON ad_acquisitions (brand_id, clicked_at DESC);

-- One row per payment or refund the ledger counts: who paid, whether it was
-- their first (new) or a later one (returning), the campaign that acquired
-- them (lifetime revenue) and the campaign whose paid click was last before
-- this payment within the window (returning-by-click), if any.
CREATE TABLE ad_revenue (
    event_id             uuid PRIMARY KEY REFERENCES customer_events (id) ON DELETE CASCADE,
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id             uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    record_id            uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    acquisition_id       uuid REFERENCES ad_acquisitions (id) ON DELETE SET NULL,
    touch_campaign_entity_id uuid REFERENCES ad_entities (id) ON DELETE SET NULL,
    touch_connector      text NOT NULL DEFAULT '',
    bucket               text NOT NULL CHECK (bucket IN ('new', 'returning')),
    value_minor          bigint NOT NULL,                    -- negative for a refund
    currency             text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    occurred_at          timestamptz NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ad_revenue_acq_idx ON ad_revenue (acquisition_id, occurred_at);
CREATE INDEX ad_revenue_touch_idx ON ad_revenue (touch_campaign_entity_id, occurred_at);
CREATE INDEX ad_revenue_brand_idx ON ad_revenue (brand_id, occurred_at DESC);

-- Where the ledger job got to for each brand: brands own their data, so
-- each brand's events are read on their own cursor.
CREATE TABLE ad_acquisition_runs (
    brand_id     uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    cursor_at    timestamptz NOT NULL DEFAULT '-infinity',
    cursor_id    uuid,
    backfilled_at timestamptz,
    last_run_at  timestamptz,
    last_error   text
);

-- The ledger reads a workspace's tracked events with a person in the order
-- they arrived (received_at), so a payment that reaches Outflare late still
-- counts.
CREATE INDEX customer_events_ledger_idx ON customer_events (workspace_id, received_at, id) WHERE kind = 'track' AND record_id IS NOT NULL;
