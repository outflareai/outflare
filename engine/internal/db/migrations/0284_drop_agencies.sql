-- Agency accounts were removed in v0.26.0 (0030 added them). Drop what is
-- left of them: their tables and the workspaces.agency_id link.
-- migration-safety: nothing reads agencies, agency_members, agency_tokens, agency_events or workspaces.agency_id since v0.34.0 (#476 removed the last read)

DROP TABLE IF EXISTS agency_events;
DROP TABLE IF EXISTS agency_tokens;
DROP TABLE IF EXISTS agency_members;
ALTER TABLE workspaces DROP COLUMN IF EXISTS agency_id;
DROP TABLE IF EXISTS agencies;
