-- The Outflare desktop app (plans/desktop-app, docs/desktop-app.md) signs
-- in through the system browser: the app starts a sign-in with a PKCE
-- challenge, the person approves it in the browser while signed in, the
-- browser hands the app a one-time code, and the app completes it with its
-- verifier to get an ordinary session. One row per sign-in in progress;
-- rows go when they complete or an hour after they expire. Machinery, with
-- no workspace: a person signs in, not a workspace.
CREATE TABLE app_signins (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    -- Names the pending sign-in in the browser URL and the deep link back.
    state          text NOT NULL UNIQUE CHECK (length(state) BETWEEN 16 AND 64),
    -- SHA-256 of the verifier the app keeps (RFC 7636 S256).
    code_challenge bytea NOT NULL CHECK (length(code_challenge) = 32),
    -- The one-time code's hash, set when the person approves.
    code_hash      bytea UNIQUE,
    name           text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
    platform       text NOT NULL CHECK (platform IN ('macos', 'windows', 'linux')),
    app_version    text NOT NULL CHECK (length(app_version) BETWEEN 1 AND 40),
    user_id        uuid REFERENCES users (id) ON DELETE CASCADE,
    approved_at    timestamptz,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL
);

CREATE INDEX app_signins_expires_idx ON app_signins (expires_at);

-- Which client a session belongs to: NULL for a browser (every session so
-- far), 'app' for the desktop app, so the app's sessions can be told apart
-- and listed or signed out later.
ALTER TABLE sessions ADD COLUMN client text CHECK (client IS NULL OR client IN ('app'));
