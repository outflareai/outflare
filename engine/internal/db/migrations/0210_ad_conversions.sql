-- Paid media, sending conversions back: when a customer event happens (a
-- sign-up, a payment), Outflare tells the ad platform that brought the
-- person, server to server, so its bidding learns from real outcomes.
--
-- A rule maps one event to one conversion on one ad account: a Google Ads
-- conversion action, a Meta pixel event, a Microsoft Advertising offline
-- conversion goal or a Reddit conversion event. Each event a rule matches
-- is one row in the send log, with what was sent or why it was not.

CREATE TABLE ad_conversion_rules (
    id                 uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id       uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    ad_account_id      uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    -- The customer event's catalog name, such as signed_up or order_completed.
    event_name         text NOT NULL CHECK (event_name ~ '^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$' AND length(event_name) <= 100),
    -- The platform's conversion: a Google conversion action id, a Meta
    -- pixel id, a Microsoft goal name, a Reddit account id.
    target_id          text NOT NULL CHECK (length(target_id) BETWEEN 1 AND 200),
    target_name        text NOT NULL DEFAULT '' CHECK (length(target_name) <= 300),
    -- The platform's event type where it has one (Meta Purchase, Reddit
    -- SignUp); '' where the target already is the event.
    event_type         text NOT NULL DEFAULT '' CHECK (length(event_type) <= 60),
    -- Where the value comes from: a property of the event (in major units,
    -- 12.50, or minor units, 1250), a fixed value, or none.
    value_property     text NOT NULL DEFAULT '' CHECK (length(value_property) <= 100),
    value_unit         text NOT NULL DEFAULT 'major' CHECK (value_unit IN ('major', 'minor')),
    fixed_value_micros bigint CHECK (fixed_value_micros >= 0),
    -- '' uses the event's currency property, then the account's.
    currency           text NOT NULL DEFAULT '' CHECK (currency ~ '^([A-Z]{3})?$'),
    enabled            boolean NOT NULL DEFAULT true,
    created_by         uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (ad_account_id, event_name, target_id, event_type)
);
CREATE INDEX ad_conversion_rules_ws_idx ON ad_conversion_rules (workspace_id) WHERE enabled;

-- One row per event a rule matched. The person's identifiers are never
-- copied here: match_keys only names the kinds that were sent (email,
-- phone, gclid, fbc), and the values are read from the record at send time.
CREATE TABLE ad_conversion_sends (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    rule_id      uuid NOT NULL REFERENCES ad_conversion_rules (id) ON DELETE CASCADE,
    event_row_id uuid NOT NULL REFERENCES customer_events (id) ON DELETE CASCADE,
    record_id    uuid REFERENCES records (id) ON DELETE SET NULL,
    -- The id the platform dedupes on: the event's message id, which the
    -- browser pixel sends too.
    event_id     text NOT NULL CHECK (length(event_id) BETWEEN 1 AND 300),
    occurred_at  timestamptz NOT NULL,
    -- queued, sent, skipped (nothing to send or no consent), failed (the
    -- platform refused it or could not be reached after retries).
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'sent', 'skipped', 'failed')),
    reason       text NOT NULL DEFAULT '' CHECK (length(reason) <= 500),
    match_keys   text[] NOT NULL DEFAULT '{}',
    value_micros bigint,
    currency     text NOT NULL DEFAULT '',
    attempts     integer NOT NULL DEFAULT 0,
    sent_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (rule_id, event_row_id)
);
CREATE INDEX ad_conversion_sends_rule_idx ON ad_conversion_sends (workspace_id, rule_id, created_at DESC);
CREATE INDEX ad_conversion_sends_queued_idx ON ad_conversion_sends (rule_id, created_at) WHERE status = 'queued';

-- Workspace settings for paid media. consent_mode: outside the EEA, the UK
-- and Switzerland, opt_out sends unless the person withdrew consent for ad
-- data, opt_in only with a grant. Inside them a grant is always needed.
CREATE TABLE ad_settings (
    workspace_id uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    consent_mode text NOT NULL DEFAULT 'opt_out' CHECK (consent_mode IN ('opt_out', 'opt_in')),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

-- Consent for ad data (Google's and Meta's ad_user_data and
-- ad_personalization purposes) is kept in the same log as email consent.
-- migration-safety: the new check only adds a value; every existing row and the previous version's writes still pass it.
ALTER TABLE consents DROP CONSTRAINT consents_channel_check;
ALTER TABLE consents ADD CONSTRAINT consents_channel_check CHECK (channel IN ('email', 'sms', 'push', 'whatsapp', 'in_app', 'ads'));
