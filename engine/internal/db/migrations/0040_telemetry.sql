-- Anonymous usage stats and update checks (docs/telemetry.md). One row for
-- the whole server: a random id that names this install and nothing else,
-- the owner's on/off choice, and what the update check last said.
CREATE TABLE telemetry_state (
    id              boolean PRIMARY KEY DEFAULT true CHECK (id),
    instance_id     uuid NOT NULL DEFAULT gen_random_uuid(),
    enabled         boolean NOT NULL DEFAULT true,
    changed_by      uuid, -- the user who last changed enabled
    changed_at      timestamptz,
    last_attempt_at timestamptz,
    last_sent_at    timestamptz,
    last_error      text,
    -- From the last update check: the newest release and any notices
    -- (security fixes, breaking changes) for this version.
    latest_version  text,
    notices         jsonb NOT NULL DEFAULT '[]',
    created_at      timestamptz NOT NULL DEFAULT now()
);

INSERT INTO telemetry_state DEFAULT VALUES;
