-- Email pattern profiles: per workspace, the address shape each domain
-- uses (first.last, flast, ...), learned from the workspace's own leads and
-- their verification outcomes. Lets the finder propose a likely address for
-- a new name at a known domain, with a confidence score and no provider
-- spend. One row per (workspace, domain); the finder reads it, a background
-- job rebuilds it. This is the per-workspace tier; a de-identified global
-- aggregate is a later, separately-reviewed change.

CREATE TABLE email_patterns (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    domain       text NOT NULL CHECK (domain = lower(domain)),
    -- Per-shape tallies derived from observed addresses:
    -- {"first.last": {"hits": 12, "misses": 1}, "flast": {"hits": 2}}.
    shapes       jsonb NOT NULL DEFAULT '{}',
    -- The winning shape and our confidence in it (0-100), cached from shapes.
    top_shape    text NOT NULL DEFAULT '',
    confidence   smallint NOT NULL DEFAULT 0,
    -- Mail provider detected from MX, for display and catch-all handling:
    -- 'google', 'microsoft', 'other' or ''. Never used to reach a mailbox.
    provider     text NOT NULL DEFAULT '',
    catch_all    boolean NOT NULL DEFAULT false,
    -- How many observed addresses backed this profile.
    sample_size  integer NOT NULL DEFAULT 0,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, domain)
);

-- Every check the finder/verifier ran on an address, and what it found: the
-- pattern guess, first-party history, MX/provider detection, the built-in
-- checks, and a third-party verifier's verdict when the waterfall used one.
-- Append-only, per-workspace. This is the labelled corpus the verification AI
-- learns from over time. It holds signals and verdicts only: never a person's
-- name, a message body or anything but the address itself.
CREATE TABLE email_checks (
    id           bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    email        text NOT NULL CHECK (email = lower(email)),
    domain       text NOT NULL,
    -- The waterfall tier that produced this check: 'pattern', 'history',
    -- 'builtin', 'mx', or 'provider:<connector id>' for a third-party verifier.
    check_type   text NOT NULL,
    -- The verdict in the shared vocabulary where it maps (deliverable,
    -- undeliverable, risky, unknown), or '' for a signal-only check.
    result       text NOT NULL DEFAULT '',
    confidence   smallint,
    -- Everything else the check saw, for training: shape, provider status,
    -- catch_all, score, reason, cost. Never names or message content.
    detail       jsonb NOT NULL DEFAULT '{}',
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX email_checks_address_idx ON email_checks (workspace_id, email, created_at DESC);
