-- Site keys: the public key a website's tracking snippet sends page views
-- and clicks with. A key only writes browser events into its workspace
-- (and brand); it reads nothing and authenticates nowhere else, so it can
-- sit in a page's HTML. It is stored as is, since it is public.
CREATE TABLE site_keys (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id        uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The brand whose site it is; events get this brand. Null: the workspace.
    brand_id            uuid REFERENCES brands (id) ON DELETE SET NULL,
    name                text NOT NULL,
    key                 text NOT NULL UNIQUE CHECK (key LIKE 'ofk\_%'),
    -- The key before the last rotation, still accepted until it expires so
    -- a site can be updated without losing visits.
    previous_key        text UNIQUE,
    previous_expires_at timestamptz,
    -- Origins (https://example.com) events are taken from. Empty: any. A
    -- filter against stray traffic, not a lock: anyone can send any Origin.
    origins             text[] NOT NULL DEFAULT '{}',
    created_by          uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    last_used_at        timestamptz,
    rotated_at          timestamptz,
    revoked_at          timestamptz
);

CREATE INDEX site_keys_workspace_idx ON site_keys (workspace_id, created_at);

-- Events taken per key per day, for the daily cap.
CREATE TABLE site_key_days (
    key_id       uuid NOT NULL REFERENCES site_keys (id) ON DELETE CASCADE,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    day          date NOT NULL,
    events       integer NOT NULL DEFAULT 0,
    PRIMARY KEY (key_id, day)
);
