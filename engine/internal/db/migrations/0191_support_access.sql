-- Support access: staff of the server's operator can view an account
-- read-only to help its person, who can turn it off in Account settings.
-- Each view is a short-lived row the person can see in their settings.
ALTER TABLE users ADD COLUMN support_access boolean NOT NULL DEFAULT true;

CREATE TABLE support_views (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash bytea NOT NULL UNIQUE,
    -- The person whose account is viewed, and the staff member viewing it.
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    viewer_id  uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    ended_at   timestamptz
);

CREATE INDEX support_views_user ON support_views (user_id, created_at DESC);
CREATE INDEX support_views_viewer ON support_views (viewer_id) WHERE ended_at IS NULL;
