-- Paid ads intelligence layer (plans/paid-ads/plan.md): the ad sync runs
-- hourly and re-pulls longer windows on a schedule (a week daily, 35 days
-- weekly, 95 days monthly) to absorb the platforms' restatements; Google's
-- click_view is harvested once a day so a click id on a landing page can
-- be tied to its ad group, ad and keyword; and Google's change_event is
-- read every 15 minutes so what changed on the platform, and who did it,
-- sits beside Outflare's own changes.
--
-- Expand only (docs/migrations.md): new nullable watermarks on ad_syncs
-- (machinery, in brandtables/workspace.txt) and two new tables the
-- previous version ignores.

ALTER TABLE ad_syncs ADD COLUMN last_week_at timestamptz;          -- 35-day re-pull
ALTER TABLE ad_syncs ADD COLUMN last_month_at timestamptz;         -- 95-day re-pull
ALTER TABLE ad_syncs ADD COLUMN last_clicks_day date;              -- click harvest watermark
ALTER TABLE ad_syncs ADD COLUMN last_change_event_at timestamptz;  -- change events read up to here

-- Google's click_view: one row per paid click with the ids the network
-- knows, harvested daily (Google keeps 90 days). The brand is the
-- account's at the time of the harvest.
CREATE TABLE ad_clicks (
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id             uuid REFERENCES brands (id) ON DELETE SET NULL,
    ad_account_id        uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    -- The click id as the landing page sees it (gclid); kind says which.
    click_id             text NOT NULL,
    kind                 text NOT NULL DEFAULT 'gclid',
    day                  date NOT NULL,
    campaign_external_id text NOT NULL DEFAULT '',
    ad_group_external_id text NOT NULL DEFAULT '',
    ad_external_id       text NOT NULL DEFAULT '',
    keyword              text NOT NULL DEFAULT '',
    match_type           text NOT NULL DEFAULT '',
    device               text NOT NULL DEFAULT '',
    PRIMARY KEY (ad_account_id, click_id)
);
CREATE INDEX ad_clicks_day_idx ON ad_clicks (workspace_id, day);

-- What changed on the platform and who did it (Google change_event, which
-- Google keeps for 30 days). actor is the SHA-256 of the user's email
-- (never the address), 'google' for Google's own automation, or 'outflare'
-- when the change was one of Outflare's (an ad_changes row applied around
-- then, or the connection's own sign-in through the API).
CREATE TABLE ad_change_events (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id         uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id             uuid REFERENCES brands (id) ON DELETE SET NULL,
    ad_account_id        uuid NOT NULL REFERENCES ad_accounts (id) ON DELETE CASCADE,
    external_id          text NOT NULL,             -- the network's event id (resource name)
    changed_at           timestamptz NOT NULL,
    actor                text NOT NULL DEFAULT '',  -- user email hashed, or 'google' or 'outflare'
    source               text NOT NULL DEFAULT '',  -- the client type: web client, API, automated rule, recommendation auto-apply, ...
    resource_type        text NOT NULL DEFAULT '',
    operation            text NOT NULL DEFAULT '',  -- create, update, remove
    campaign_external_id text NOT NULL DEFAULT '',
    summary              text NOT NULL DEFAULT '',
    detail               jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object'),
    UNIQUE (ad_account_id, external_id)
);
CREATE INDEX ad_change_events_acct_idx ON ad_change_events (ad_account_id, changed_at DESC);
CREATE INDEX ad_change_events_ws_idx ON ad_change_events (workspace_id, changed_at DESC);
