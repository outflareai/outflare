-- Preview and QA for A/B tests: a person's check that a version renders
-- right (ok) or is broken. payload_hash is the version's content when it
-- was checked, so a check stops counting once the content changes.
CREATE TABLE ab_test_checks (
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    test_id      uuid NOT NULL REFERENCES ab_tests (id) ON DELETE CASCADE,
    variant      text NOT NULL,
    status       text NOT NULL CHECK (status IN ('ok', 'broken')),
    note         text NOT NULL DEFAULT '',
    payload_hash text NOT NULL,
    checked_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    checked_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (test_id, variant)
);
CREATE INDEX ab_test_checks_workspace ON ab_test_checks (workspace_id);
