-- Email confirmation and password reset.
--
-- email_verified_at is when the person proved they read this address: a
-- confirmation link, a password reset, or Google vouching for it. People
-- who signed up before this existed are counted as confirmed.
ALTER TABLE users ADD COLUMN email_verified_at timestamptz;
UPDATE users SET email_verified_at = created_at;

-- One-time links sent by email. Only a hash of the secret is stored.
CREATE TABLE account_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash bytea NOT NULL UNIQUE,
    user_id    uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    text NOT NULL CHECK (purpose IN ('verify_email', 'reset_password')),
    -- The address the link went to; a change of email voids it.
    email      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at    timestamptz
);

CREATE INDEX account_tokens_user_idx ON account_tokens (user_id, purpose);
