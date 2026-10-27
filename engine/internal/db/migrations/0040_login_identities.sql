-- Ways to sign in other than a password. A person can have a password, a
-- Google account, or both; users.password_hash is '' for an account that
-- has never set a password (which no password matches).
CREATE TABLE user_identities (
    provider     text NOT NULL CHECK (provider IN ('google')),
    subject      text NOT NULL, -- the provider's stable id for the account
    user_id      uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    email        text NOT NULL, -- as the provider last reported it
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (provider, subject)
);

CREATE INDEX user_identities_user_idx ON user_identities (user_id);
