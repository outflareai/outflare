-- Workspace overview counts enrollments by status.
CREATE INDEX enrollments_ws_status_idx ON enrollments (workspace_id, status);
