-- A brand's AI visibility share link: anyone with the link (and the
-- password, when one is set) sees the brand's AI visibility overview and
-- questions read-only, nothing else of the brand. One link per brand;
-- making a new one replaces it. Only the hash of the token is kept, like a
-- dashboard share's.
CREATE TABLE ai_visibility_shares (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id   uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id       uuid NOT NULL UNIQUE REFERENCES brands (id) ON DELETE CASCADE,
    token_hash     bytea NOT NULL UNIQUE,
    -- An argon2id hash, or null when the link alone opens it.
    password_hash  text,
    expires_at     timestamptz,
    views          integer NOT NULL DEFAULT 0,
    last_viewed_at timestamptz,
    created_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now()
);
