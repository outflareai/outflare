-- A mailbox the CRM reads through the company's Google Workspace
-- connection (a service account with domain-wide delegation) instead of
-- its owner signing in. Removing the connection removes these.
ALTER TABLE crm_google_connections
    ADD COLUMN workspace_integration_id uuid REFERENCES integrations (id) ON DELETE CASCADE;
CREATE INDEX crm_google_connections_workspace_idx ON crm_google_connections (workspace_integration_id) WHERE workspace_integration_id IS NOT NULL;
