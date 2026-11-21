-- Sales mail through the company's Google Workspace connection (ADR 15):
-- a Sales connection can be read and sent through the brand's Workspace
-- connection instead of the person's own Google sign-in. Deleting the
-- Workspace connection removes the Sales connections that use it.
ALTER TABLE sales_mail_connections
    ADD COLUMN workspace_integration_id uuid REFERENCES integrations (id) ON DELETE CASCADE;
CREATE INDEX sales_mail_connections_workspace_idx ON sales_mail_connections (workspace_integration_id) WHERE workspace_integration_id IS NOT NULL;
