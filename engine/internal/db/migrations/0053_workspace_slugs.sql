-- Workspace slugs name workspaces in web app URLs (/{slug}/connectors) and
-- stand in for their IDs in the API. Give every existing workspace one that
-- the new rules accept: 3 to 40 lowercase letters, digits and dashes, not
-- starting or ending with a dash, and not a word the app uses for its own
-- pages (the list is reservedSlugs in engine/internal/api/workspace_slug.go).

UPDATE workspaces
SET slug = coalesce(nullif(trim(both '-' from left(regexp_replace(lower(slug), '[^a-z0-9]+', '-', 'g'), 30)), ''), 'workspace') || '-' || left(replace(id::text, '-', ''), 6)
WHERE slug !~ '^[a-z0-9][a-z0-9-]{1,38}[a-z0-9]$'
   OR slug IN ('about', 'account', 'accounts', 'admin', 'agency', 'agencies', 'api', 'app', 'assets', 'auth', 'billing', 'blog', 'callback', 'cloud', 'connect', 'dashboard', 'docs', 'download', 'emailbison', 'favicon', 'help', 'home', 'healthz', 'ingest', 'invite', 'invites', 'login', 'logout', 'new', 'oauth', 'outflare', 'pricing', 'privacy', 'readyz', 'register', 'reset-password', 'security', 'settings', 'setup', 'sign-in', 'sign-up', 'signin', 'signout', 'signup', 'static', 'status', 'support', 'terms', 'user', 'users', 'verify-email', 'workspaces', 'www');

-- A workspace's earlier slugs keep working, like a renamed GitHub
-- organisation, until another workspace takes one.
CREATE TABLE workspace_slug_history (
    slug         text PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    retired_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX workspace_slug_history_workspace ON workspace_slug_history (workspace_id);

-- Readable names for items in web app URLs: /acme/sequences/q3-founders
-- instead of /acme/sequences/{id}. A slug is made from the item's name the
-- first time a link to it is needed, and a new one when the name changes;
-- earlier slugs stay, so old links still open the item. kind is sequence,
-- project, list, mission, server or plan.
CREATE TABLE item_slugs (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    kind         text NOT NULL,
    slug         text NOT NULL,
    item_id      uuid NOT NULL,
    name         text NOT NULL, -- the name the slug was made from
    current      boolean NOT NULL DEFAULT true,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, kind, slug)
);
CREATE UNIQUE INDEX item_slugs_current ON item_slugs (workspace_id, kind, item_id) WHERE current;
