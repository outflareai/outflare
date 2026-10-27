-- Passkeys people sign in with. credential is go-webauthn's record
-- (public key, signature counter, flags), as JSON.
CREATE TABLE user_passkeys (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    credential_id bytea NOT NULL UNIQUE,
    credential    jsonb NOT NULL,
    name          text NOT NULL,
    created_at    timestamptz NOT NULL DEFAULT now(),
    last_used_at  timestamptz
);

CREATE INDEX user_passkeys_user_idx ON user_passkeys (user_id);
