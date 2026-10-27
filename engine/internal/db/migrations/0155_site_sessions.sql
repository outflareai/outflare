-- Website visits as sessions: one row per visit to a workspace's sites,
-- with where it came from (the landing page's campaign tags, ad click ids,
-- referral code and referrer) and the visitor's rough setting (country
-- from their time zone, device, browser, system). A visit ends after 30
-- minutes without an event or when the visitor arrives again through a
-- different campaign. Page and track events carry the session's id as
-- context.sessionId. A person's first and last touch are worked out from
-- these rows.
CREATE TABLE site_sessions (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    key_id        uuid REFERENCES site_keys (id) ON DELETE SET NULL,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    anonymous_id  text NOT NULL CHECK (length(anonymous_id) BETWEEN 1 AND 200),
    -- Set once the workspace's product identifies the visitor.
    record_id     uuid REFERENCES records (id) ON DELETE SET NULL,
    started_at    timestamptz NOT NULL DEFAULT now(),
    last_seen_at  timestamptz NOT NULL DEFAULT now(),
    events        integer NOT NULL DEFAULT 0,
    pageviews     integer NOT NULL DEFAULT 0,
    -- The landing page, without any parameter but the tags below.
    landing_url   text NOT NULL DEFAULT '' CHECK (length(landing_url) <= 2000),
    landing_path  text NOT NULL DEFAULT '' CHECK (length(landing_path) <= 2000),
    -- The page they came from: its origin and path, never its query.
    referrer      text NOT NULL DEFAULT '' CHECK (length(referrer) <= 2000),
    referrer_host text NOT NULL DEFAULT '' CHECK (length(referrer_host) <= 253),
    utm_source    text NOT NULL DEFAULT '' CHECK (length(utm_source) <= 500),
    utm_medium    text NOT NULL DEFAULT '' CHECK (length(utm_medium) <= 500),
    utm_campaign  text NOT NULL DEFAULT '' CHECK (length(utm_campaign) <= 500),
    utm_term      text NOT NULL DEFAULT '' CHECK (length(utm_term) <= 500),
    utm_content   text NOT NULL DEFAULT '' CHECK (length(utm_content) <= 500),
    click_ids     jsonb NOT NULL DEFAULT '{}',
    -- A partner's referral code (ref or via) and an Outflare campaign code.
    ref           text NOT NULL DEFAULT '' CHECK (length(ref) <= 200),
    ofc           text NOT NULL DEFAULT '' CHECK (length(ofc) <= 200),
    -- ISO 3166-1 alpha-2 from the browser's time zone; '' when unknown.
    country       text NOT NULL DEFAULT '' CHECK (country ~ '^([A-Z]{2})?$'),
    timezone      text NOT NULL DEFAULT '' CHECK (length(timezone) <= 100),
    device        text NOT NULL DEFAULT '' CHECK (device IN ('', 'desktop', 'mobile', 'tablet')),
    browser       text NOT NULL DEFAULT '' CHECK (length(browser) <= 40),
    os            text NOT NULL DEFAULT '' CHECK (length(os) <= 40)
);

CREATE INDEX site_sessions_visitor_idx ON site_sessions (workspace_id, anonymous_id, last_seen_at DESC);
CREATE INDEX site_sessions_record_idx ON site_sessions (workspace_id, record_id, started_at) WHERE record_id IS NOT NULL;
CREATE INDEX site_sessions_ws_idx ON site_sessions (workspace_id, started_at DESC);

-- Requests from bots and crawlers, counted but not stored.
ALTER TABLE site_key_days ADD COLUMN bots integer NOT NULL DEFAULT 0;
