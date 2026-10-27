-- Website visitors: who is on a workspace's site. One row per browser (the
-- site snippet's anonymous id) with its counts, so the Visitors page,
-- scoring and alerts never scan events. The page views themselves stay in
-- customer_events; the person and company are customer records.
CREATE TABLE site_visitors (
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    anonymous_id  text NOT NULL,
    -- The person, once known.
    record_id     uuid REFERENCES records (id) ON DELETE SET NULL,
    -- The company the visit came from, when it was found for the visitor
    -- rather than through the person (a reverse IP lookup).
    account_id    uuid REFERENCES records (id) ON DELETE SET NULL,
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- How the visitor became known: product (the workspace's own sign-in),
    -- email_click (a tracked link in an email Outflare sent), company_ip,
    -- provider. Empty: anonymous.
    identified_by text NOT NULL DEFAULT ''
                      CHECK (identified_by IN ('', 'product', 'email_click', 'company_ip', 'provider')),
    first_seen_at timestamptz NOT NULL,
    last_seen_at  timestamptz NOT NULL,
    pageviews     integer NOT NULL DEFAULT 0,
    -- Views of the workspace's key pages (pricing, demo, ...).
    key_pageviews integer NOT NULL DEFAULT 0,
    sessions      integer NOT NULL DEFAULT 1,
    last_path     text NOT NULL DEFAULT '',
    PRIMARY KEY (workspace_id, anonymous_id)
);

CREATE INDEX site_visitors_recent_idx ON site_visitors (workspace_id, last_seen_at DESC);
CREATE INDEX site_visitors_record_idx ON site_visitors (record_id) WHERE record_id IS NOT NULL;
CREATE INDEX site_visitors_account_idx ON site_visitors (account_id) WHERE account_id IS NOT NULL;

-- A workspace's choices for the Visitors page. No row: the defaults.
CREATE TABLE visitor_settings (
    workspace_id      uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    key_pages         text[] NOT NULL,
    hot_score         integer NOT NULL DEFAULT 60 CHECK (hot_score BETWEEN 1 AND 100),
    -- Know visitors from their clicks on tracked links in Outflare email.
    email_clicks      boolean NOT NULL DEFAULT true,
    -- Companies left out: the workspace's own team, competitors.
    exclude_domains   text[] NOT NULL DEFAULT '{}',
    exclude_customers boolean NOT NULL DEFAULT false,
    updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Who became hot when, so each alert (audit event, webhook) is sent once a
-- week per person or company.
CREATE TABLE visitor_alerts (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    kind         text NOT NULL CHECK (kind IN ('hot')),
    score        integer NOT NULL,
    alerted_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (record_id, kind)
);

CREATE INDEX visitor_alerts_recent_idx ON visitor_alerts (workspace_id, alerted_at DESC);

-- The browser a click token was handed to: a token names one browser only.
ALTER TABLE link_clicks ADD COLUMN anonymous_id text;
