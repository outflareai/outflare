-- Journey flow control (customer data platform, actions step 3):
-- wait_until steps, and limits on who enters a journey.

-- A run on a wait_until step waits for one of these events (wait_events),
-- or for conditions that come true (re-checked when one of the person's
-- fields changes, while wait_fields), until wait_deadline. next_at is
-- when it next looks.
ALTER TABLE journey_runs ADD COLUMN wait_events text[];
ALTER TABLE journey_runs ADD COLUMN wait_fields boolean NOT NULL DEFAULT false;
ALTER TABLE journey_runs ADD COLUMN wait_deadline timestamptz;
CREATE INDEX journey_runs_wait_events ON journey_runs USING gin (wait_events) WHERE status = 'active' AND wait_events IS NOT NULL;
CREATE INDEX journey_runs_wait_fields ON journey_runs (workspace_id, record_id) WHERE status = 'active' AND wait_fields;
CREATE INDEX journey_runs_wait_deadline ON journey_runs (wait_deadline) WHERE status = 'active' AND wait_deadline IS NOT NULL;
-- For monitoring: the earliest run due to move on.
CREATE INDEX journey_runs_next_at ON journey_runs (next_at) WHERE status = 'active' AND next_at IS NOT NULL;

-- Entry limits: at most entry_limit people start it per entry_limit_per
-- (hour or day), so a busy trigger can't flood it. With entry again,
-- reentry_after_seconds is how long after someone's last run ends they
-- can start again. Drafts and kept versions carry them like entry.
ALTER TABLE journeys ADD COLUMN entry_limit integer CHECK (entry_limit > 0);
ALTER TABLE journeys ADD COLUMN entry_limit_per text CHECK (entry_limit_per IN ('hour', 'day'));
ALTER TABLE journeys ADD COLUMN reentry_after_seconds integer CHECK (reentry_after_seconds >= 60);
ALTER TABLE journey_versions ADD COLUMN entry_limit integer CHECK (entry_limit > 0);
ALTER TABLE journey_versions ADD COLUMN entry_limit_per text CHECK (entry_limit_per IN ('hour', 'day'));
ALTER TABLE journey_versions ADD COLUMN reentry_after_seconds integer CHECK (reentry_after_seconds >= 60);
