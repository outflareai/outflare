-- Human takeover of a brand's external account (plans/brand-agent,
-- "Accounts"): when a managed use meets MFA, a CAPTCHA, a passkey or a
-- consent screen, the tools ask for a person, who takes the account over
-- for a short while, exclusively, and hands it back; the identity signed
-- in is checked again on handback. Offboarding a brand's accounts is a
-- single action on the accounts themselves and needs no table.
CREATE TABLE brand_account_takeovers (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id         uuid NOT NULL REFERENCES brands (id) ON DELETE CASCADE,
    account_id       uuid NOT NULL REFERENCES brand_accounts (id) ON DELETE CASCADE,
    -- The incident the takeover is for, when there is one.
    incident_id      uuid REFERENCES brand_account_incidents (id) ON DELETE SET NULL,
    -- What the person is needed for: mfa, captcha, passkey, consent, other.
    reason           text NOT NULL CHECK (reason IN ('mfa', 'captcha', 'passkey', 'consent', 'other')),
    note             text NOT NULL DEFAULT '' CHECK (length(note) <= 1000),
    -- requested: waiting for a person; active: a person holds the account;
    -- handed_back, cancelled, expired: over.
    status           text NOT NULL DEFAULT 'requested' CHECK (status IN ('requested', 'active', 'handed_back', 'cancelled', 'expired')),
    requested_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    requested_at     timestamptz NOT NULL DEFAULT now(),
    -- The person holding it, from when, until when at the latest.
    holder_id        uuid REFERENCES users (id) ON DELETE SET NULL,
    started_at       timestamptz,
    expires_at       timestamptz,
    ended_at         timestamptz,
    -- What the handback check found: authenticated (the brand's identity
    -- was signed in), wrong_identity, or unresolved (not checked).
    handback_outcome text NOT NULL DEFAULT '' CHECK (handback_outcome IN ('', 'authenticated', 'wrong_identity', 'unresolved')),
    handback_note    text NOT NULL DEFAULT '' CHECK (length(handback_note) <= 1000)
);

-- Exclusive: one takeover waiting or under way per account.
CREATE UNIQUE INDEX brand_account_takeovers_open_idx ON brand_account_takeovers (account_id) WHERE status IN ('requested', 'active');
CREATE INDEX brand_account_takeovers_brand_idx ON brand_account_takeovers (brand_id, status, requested_at DESC);
