-- The events feed (GET .../events?after=) reads the audit log forwards
-- from an id, filtered by action.
CREATE INDEX audit_events_workspace_id_idx ON audit_events (workspace_id, id);
