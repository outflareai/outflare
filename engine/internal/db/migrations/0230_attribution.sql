-- Attribution: which channel brought each visit, and which visits get the
-- credit for each conversion (a sign-up, a payment), so every channel and
-- ad can be measured on the same numbers.
--
-- Each website visit (site_sessions) gets its channel, source and campaign
-- once, worked out on the server from what it arrived with: the Outflare
-- campaign code (ofc), a partner's referral code, ad click ids, UTM tags,
-- then the referrer. '' means not worked out yet.
ALTER TABLE site_sessions
    ADD COLUMN channel     text NOT NULL DEFAULT '' CHECK (channel IN ('', 'outbound_email', 'seo', 'ai_answers',
        'organic_social', 'reddit', 'paid_ads', 'affiliates', 'lifecycle_email', 'referral', 'direct')),
    ADD COLUMN source      text NOT NULL DEFAULT '' CHECK (length(source) <= 80),
    ADD COLUMN campaign    text NOT NULL DEFAULT '' CHECK (length(campaign) <= 80),
    ADD COLUMN tracking_campaign_id uuid REFERENCES tracking_campaigns (id) ON DELETE SET NULL,
    -- What decided it: ofc, ref, click_id, utm, referrer or none.
    ADD COLUMN tag_method  text NOT NULL DEFAULT '' CHECK (tag_method IN ('', 'ofc', 'ref', 'click_id', 'utm', 'referrer', 'none'));

CREATE INDEX site_sessions_unclassified_idx ON site_sessions (started_at) WHERE channel = '';
CREATE INDEX site_sessions_channel_idx ON site_sessions (workspace_id, channel, started_at);

-- One row per workspace: the model every report uses by default, how far
-- back a touch can be, and which customer events count as conversions.
CREATE TABLE attribution_settings (
    workspace_id      uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    model             text NOT NULL DEFAULT 'last_non_direct'
                      CHECK (model IN ('last_non_direct', 'first_touch', 'linear', 'position', 'time_decay')),
    lookback_days     integer NOT NULL DEFAULT 90 CHECK (lookback_days BETWEEN 1 AND 365),
    conversion_events text[] NOT NULL DEFAULT '{signed_up,payment.succeeded}'
                      CHECK (cardinality(conversion_events) BETWEEN 1 AND 20),
    -- When every conversion in the lookback was last credited. Null makes
    -- the next run go back over them (new settings, changed events).
    backfilled_at     timestamptz,
    updated_by        uuid REFERENCES actors (id) ON DELETE SET NULL,
    updated_at        timestamptz NOT NULL DEFAULT now()
);
