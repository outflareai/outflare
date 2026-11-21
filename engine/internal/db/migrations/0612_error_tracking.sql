-- Error tracking for a brand's own website or app. The website script (or
-- a server) sends each error as an ordinary track event, Error Caught, so
-- errors need no table of their own: the Errors page groups those events
-- into issues by their fingerprint.

-- Whether the site's script sends uncaught errors on its own (off by
-- default, like the other autocapture switches).
ALTER TABLE site_keys ADD COLUMN capture_errors boolean NOT NULL DEFAULT false;

-- What a person decided about an issue: resolved or ignored. An issue
-- nobody has touched has no row and is open. A resolved issue that
-- happens again after status_at shows as open again.
CREATE TABLE error_issues (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    fingerprint  text NOT NULL CHECK (fingerprint ~ '^[a-z0-9-]{1,64}$'),
    status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'ignored')),
    status_at    timestamptz NOT NULL DEFAULT now(),
    updated_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (brand_id, fingerprint)
);
CREATE INDEX error_issues_workspace_idx ON error_issues (workspace_id);
