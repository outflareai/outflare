-- Playbook automations: a playbook that starts by itself, on a schedule or
-- when an event happens. A person approves an automation once, with the
-- mission mode, who runs it and the spend limits; each run is then a
-- mission started and approved inside those limits. Every run still only
-- does what its mode allows (dry run, approval of each write, or within
-- the limits), and link pitches still wait for approval.

CREATE TABLE playbook_automations (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- Runs for one brand's records only; null for every brand.
    brand_id        uuid REFERENCES brands (id) ON DELETE CASCADE,
    playbook_id     text NOT NULL CHECK (length(playbook_id) <= 100),
    name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
    inputs          jsonb NOT NULL DEFAULT '{}',
    -- schedule: runs every day, week or month. event: runs when an event
    -- from the webhook catalog happens in the workspace.
    kind            text NOT NULL CHECK (kind IN ('schedule', 'event')),
    -- The default keeps a bare insert valid; the API always sets it.
    cadence         text DEFAULT 'weekly' CHECK (cadence IN ('daily', 'weekly', 'monthly')),
    -- Schedules run at this UTC hour, on this weekday (0 is Sunday) or day
    -- of the month (1 to 28).
    run_hour        int NOT NULL DEFAULT 9 CHECK (run_hour BETWEEN 0 AND 23),
    run_weekday     int NOT NULL DEFAULT 1 CHECK (run_weekday BETWEEN 0 AND 6),
    run_monthday    int NOT NULL DEFAULT 1 CHECK (run_monthday BETWEEN 1 AND 28),
    event_type      text CHECK (length(event_type) <= 100),
    -- each: a run per event, at most daily_cap a day (the rest wait for the
    -- next day). hourly, daily: the events gathered into one run.
    gather          text NOT NULL DEFAULT 'daily' CHECK (gather IN ('each', 'hourly', 'daily')),
    daily_cap       int NOT NULL DEFAULT 10 CHECK (daily_cap BETWEEN 1 AND 100),
    -- The last audit event an event automation has taken, so it never
    -- runs for anything that happened before it was turned on.
    last_event_id   bigint NOT NULL DEFAULT 0,
    -- The runs' missions.
    mode            text NOT NULL DEFAULT 'approve_writes' CHECK (mode IN ('dry_run', 'approve_writes', 'autonomous')),
    runner          text NOT NULL DEFAULT 'builtin' CHECK (runner IN ('builtin', 'external')),
    token_id        uuid REFERENCES api_tokens (id) ON DELETE SET NULL,
    max_spend_cents_per_run   bigint CHECK (max_spend_cents_per_run >= 0),
    max_spend_cents_per_month bigint CHECK (max_spend_cents_per_month >= 0),
    -- draft (waits for a person to approve it), active, paused (by hand),
    -- or held (stopped by itself, held_reason says why: a limit reached,
    -- a connection gone, its approver gone).
    status          text NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'active', 'paused', 'held')),
    held_reason     text NOT NULL DEFAULT '' CHECK (length(held_reason) <= 500),
    next_run_at     timestamptz,
    last_run_at     timestamptz,
    last_mission_id uuid,
    runs            int NOT NULL DEFAULT 0,
    -- The person whose approval the runs carry: they act with that
    -- person's current rights.
    approved_by     uuid REFERENCES users (id) ON DELETE SET NULL,
    approved_at     timestamptz,
    created_by      uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (kind <> 'schedule' OR cadence IS NOT NULL),
    CHECK (kind <> 'event' OR event_type IS NOT NULL)
);
CREATE INDEX playbook_automations_ws_idx ON playbook_automations (workspace_id, created_at DESC);
CREATE INDEX playbook_automations_due_idx ON playbook_automations (next_run_at) WHERE status = 'active';

-- Which automation started a mission, and what started it.
ALTER TABLE missions ADD COLUMN automation_id uuid REFERENCES playbook_automations (id) ON DELETE SET NULL;
CREATE INDEX missions_automation_idx ON missions (automation_id, created_at) WHERE automation_id IS NOT NULL;
