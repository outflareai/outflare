-- Failed sign-ins and account email requests, counted per address and per
-- network, so the limits on them survive restarts and hold across servers.
-- bucket is a hash of the limit and its key (an email or IP address), so
-- no address is stored. Rows older than an hour are cleared as new ones come.
CREATE TABLE auth_attempts (
    bucket bytea NOT NULL,
    at     timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX auth_attempts_bucket_idx ON auth_attempts (bucket, at);
CREATE INDEX auth_attempts_at_idx ON auth_attempts (at);

-- Password sign-ups waiting for their email to be confirmed. The account is
-- made only when the link is opened, so nobody can hold an address they
-- don't own, and the sign-up form answers the same whether or not the
-- address already has an account.
CREATE TABLE pending_signups (
    email          text PRIMARY KEY, -- lowercase
    token_hash     bytea NOT NULL UNIQUE,
    name           text NOT NULL,
    workspace_name text NOT NULL,
    password_hash  text NOT NULL,
    -- Hash of a cookie set in the browser that filled in the form. The link
    -- uses password_hash only when opened there; elsewhere the person
    -- chooses a password, so a stranger's sign-up with your address can't
    -- leave you with their password.
    browser_hash   bytea NOT NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    expires_at     timestamptz NOT NULL,
    -- Confirmation emails sent since first_sent_at, to limit resends.
    first_sent_at  timestamptz NOT NULL DEFAULT now(),
    last_sent_at   timestamptz NOT NULL DEFAULT now(),
    sends          int NOT NULL DEFAULT 1
);
