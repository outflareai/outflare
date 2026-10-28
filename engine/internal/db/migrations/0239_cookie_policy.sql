-- The cookie policy that keeps itself up to date: every cookie, storage
-- key and third-party script found on a site, from a scan of its pages
-- (engine/internal/cookies, through the site crawler) and from what the
-- website script sees in visitors' browsers (names only, never values).
-- Each is matched to a vendor in the catalogue; ones nobody knows wait for
-- a person to say what they are.
CREATE TABLE site_cookies (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_key_id    uuid NOT NULL REFERENCES site_keys (id) ON DELETE CASCADE,
    kind           text NOT NULL CHECK (kind IN ('cookie', 'local_storage', 'session_storage', 'script', 'iframe', 'pixel')),
    -- A cookie's or storage key's name; for a script, iframe or pixel its
    -- host and first path segment (www.googletagmanager.com/gtm.js).
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 300),
    -- The cookie's domain, or the script's host.
    host           text NOT NULL DEFAULT '',
    -- The catalogue's vendor id, or '' when unknown.
    vendor         text NOT NULL DEFAULT '',
    category       text NOT NULL DEFAULT 'unknown' CHECK (category IN ('necessary', 'analytics', 'marketing', 'preferences', 'unknown')),
    purpose        text NOT NULL DEFAULT '' CHECK (length(purpose) <= 1000),
    retention      text NOT NULL DEFAULT '' CHECK (length(retention) <= 100),
    -- new: found, not looked at by a person (in the policy when its vendor
    -- is known); listed: a person confirmed it; ignored: never listed.
    status         text NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'listed', 'ignored')),
    -- Who saw it: crawl, browser.
    sources        text[] NOT NULL DEFAULT '{}',
    -- A browser had it before the visitor allowed its category.
    before_consent boolean NOT NULL DEFAULT false,
    -- A few pages it was on.
    pages          text[] NOT NULL DEFAULT '{}',
    reviewed_by    uuid REFERENCES actors (id) ON DELETE SET NULL,
    reviewed_at    timestamptz,
    first_seen_at  timestamptz NOT NULL DEFAULT now(),
    last_seen_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX site_cookies_name_idx ON site_cookies (site_key_id, kind, name);
CREATE INDEX site_cookies_workspace_idx ON site_cookies (workspace_id, site_key_id);

-- A scan of a site's pages for cookies and scripts.
CREATE TABLE cookie_scans (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_key_id  uuid NOT NULL REFERENCES site_keys (id) ON DELETE CASCADE,
    status       text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued', 'running', 'done', 'failed')),
    -- schedule, person, api
    trigger      text NOT NULL DEFAULT 'person',
    pages        integer NOT NULL DEFAULT 0,
    found        integer NOT NULL DEFAULT 0,
    added        integer NOT NULL DEFAULT 0,
    error        text NOT NULL DEFAULT '',
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    started_at   timestamptz,
    finished_at  timestamptz
);

CREATE INDEX cookie_scans_key_idx ON cookie_scans (site_key_id, created_at DESC);

-- Each version of a site's cookie policy, as published: what it listed
-- and what changed from the one before. A version that adds a vendor
-- needing consent asks visitors again (consent_banners.consent_version).
CREATE TABLE cookie_policies (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    site_key_id  uuid NOT NULL REFERENCES site_keys (id) ON DELETE CASCADE,
    version      integer NOT NULL,
    content      jsonb NOT NULL,
    added        text[] NOT NULL DEFAULT '{}',
    removed      text[] NOT NULL DEFAULT '{}',
    reasked      boolean NOT NULL DEFAULT false,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX cookie_policies_version_idx ON cookie_policies (site_key_id, version);
CREATE INDEX cookie_policies_workspace_idx ON cookie_policies (workspace_id);
