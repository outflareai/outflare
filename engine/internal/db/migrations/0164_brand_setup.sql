-- Brand setup: the steps that get one brand's website, tracking and tools
-- in place, most needed first. Whether a step is done is read from the
-- workspace (a site key that has had a visit, a verified site, a
-- connection), so doing it anywhere else ticks it off too. Only what a
-- person chose (skips, closing the checklist) and what a one-click install
-- last did are stored here.
CREATE TABLE brand_setup (
    brand_id     uuid PRIMARY KEY REFERENCES brands (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Steps left for later.
    skipped      text[] NOT NULL DEFAULT '{}',
    dismissed_at timestamptz,
    -- What each one-click install last did, by step:
    -- {"site_script": {"method": "gtm", "status": "done", "at": "...",
    --   "error": "", "link": "https://tagmanager.google.com/..."}}.
    -- Never a credential: the connection holds those.
    installs     jsonb NOT NULL DEFAULT '{}',
    updated_at   timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL
);

CREATE INDEX brand_setup_workspace_idx ON brand_setup (workspace_id);

-- Sites whose ownership proof Outflare is waiting to see, such as a DNS
-- record a one-click install just added: checked every minute for 15
-- minutes, then hourly for three days.
ALTER TABLE seo_sites ADD COLUMN verify_watch_until timestamptz;
