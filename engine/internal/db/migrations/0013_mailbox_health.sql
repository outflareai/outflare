-- Mailbox health: how each workspace wants unhealthy mailboxes handled.
-- No row means the defaults below. Health itself is computed on read from
-- sends, bounces, sign-in and sync errors, warm-up placement and domain
-- checks, so it is never stale.

CREATE TABLE health_settings (
    workspace_id      uuid PRIMARY KEY REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Pause a mailbox when its bounce rate over the last 24 hours reaches
    -- pause_bounce_rate percent, once it has sent at least min_sends.
    auto_pause        boolean NOT NULL DEFAULT true,
    pause_bounce_rate numeric(5, 2) NOT NULL DEFAULT 5 CHECK (pause_bounce_rate > 0 AND pause_bounce_rate <= 100),
    min_sends         integer NOT NULL DEFAULT 20 CHECK (min_sends BETWEEN 1 AND 10000),
    updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX enrollments_bounced_idx ON enrollments (mailbox_id, updated_at) WHERE status = 'bounced';
