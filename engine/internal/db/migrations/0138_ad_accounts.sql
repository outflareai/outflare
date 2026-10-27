-- Paid media: the ad accounts a workspace connected (Google Ads, Meta,
-- Microsoft Advertising, Reddit, X and LinkedIn), their campaigns, and what
-- each campaign spent and got per day, as the platform reports it. The
-- same spend is also written to spend_entries, one row per campaign and
-- day, so costs and outcomes include ads.

CREATE TABLE ad_accounts (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    integration_id uuid NOT NULL REFERENCES integrations (id) ON DELETE CASCADE,
    connector      text NOT NULL,
    -- The platform's id for the account (a Google Ads customer id, act_ id
    -- without the prefix, a LinkedIn sponsored account id).
    external_id    text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 100),
    name           text NOT NULL DEFAULT '',
    currency       text NOT NULL DEFAULT 'USD' CHECK (currency ~ '^[A-Z]{3}$'),
    time_zone      text NOT NULL DEFAULT '',
    -- What the platform needs to read the account again, such as the
    -- Google Ads manager account it is reached through.
    meta           jsonb NOT NULL DEFAULT '{}',
    brand_id       uuid REFERENCES brands (id) ON DELETE SET NULL,
    sync_on        boolean NOT NULL DEFAULT true,
    -- Gone: the sign-in no longer sees the account. Kept for its history.
    status         text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'gone')),
    first_day      date,
    last_day       date,
    last_synced_at timestamptz,
    last_error     text,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (integration_id, external_id)
);
CREATE INDEX ad_accounts_ws_idx ON ad_accounts (workspace_id);

-- Campaigns now; ad groups and ads later, in the same table.
CREATE TABLE ad_entities (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    ad_account_id       uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    level               text NOT NULL DEFAULT 'campaign' CHECK (level IN ('campaign', 'ad_group', 'ad')),
    parent_id           uuid REFERENCES ad_entities (id) ON DELETE CASCADE,
    external_id         text NOT NULL CHECK (length(external_id) BETWEEN 1 AND 100),
    name                text NOT NULL DEFAULT '',
    -- active, paused, removed or other; provider_status is the platform's word.
    status              text NOT NULL DEFAULT 'other' CHECK (status IN ('active', 'paused', 'removed', 'other')),
    provider_status     text NOT NULL DEFAULT '',
    objective           text NOT NULL DEFAULT '',
    daily_budget_micros bigint CHECK (daily_budget_micros >= 0),
    -- Set when a campaign belongs to another brand than its account.
    brand_id            uuid REFERENCES brands (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (ad_account_id, level, external_id)
);
CREATE INDEX ad_entities_ws_idx ON ad_entities (workspace_id, level);

-- Money is in micros (millionths) of the account's currency.
-- Conversions and their value are what the platform claims, never added
-- across platforms or to attributed revenue.
CREATE TABLE ad_metrics_daily (
    ad_entity_id            uuid NOT NULL REFERENCES ad_entities (id) ON DELETE CASCADE,
    day                     date NOT NULL,
    workspace_id            uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    currency                text NOT NULL CHECK (currency ~ '^[A-Z]{3}$'),
    spend_micros            bigint NOT NULL DEFAULT 0 CHECK (spend_micros >= 0),
    impressions             bigint NOT NULL DEFAULT 0 CHECK (impressions >= 0),
    clicks                  bigint NOT NULL DEFAULT 0 CHECK (clicks >= 0),
    conversions             numeric(18, 4),
    conversion_value_micros bigint,
    updated_at              timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (ad_entity_id, day)
);
CREATE INDEX ad_metrics_daily_ws_idx ON ad_metrics_daily (workspace_id, day);

-- One row per connection: when it last synced and how it went.
CREATE TABLE ad_syncs (
    integration_id uuid PRIMARY KEY REFERENCES integrations (id) ON DELETE CASCADE,
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    last_run_at    timestamptz,
    last_ok_at     timestamptz,
    -- The last run that re-read 30 days; platforms restate recent days.
    last_full_at   timestamptz,
    last_error     text,
    last_rows      integer NOT NULL DEFAULT 0
);
CREATE INDEX ad_syncs_ws_idx ON ad_syncs (workspace_id);

ALTER TABLE spend_entries ADD CONSTRAINT spend_entries_ad_entity_fk FOREIGN KEY (ad_entity_id) REFERENCES ad_entities (id) ON DELETE SET NULL;
