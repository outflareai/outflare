-- The Google Workspace connection that reads the team's mail and calendars
-- into the CRM had the same connector id as the one that inspects a
-- Workspace tenant (google_workspace), so only one of them could be used.
-- It is google_workspace_mail now; connections made with it (a
-- service account key and a test user, whose account records the service
-- account) move over.
UPDATE integrations SET connector = 'google_workspace_mail'
WHERE connector = 'google_workspace' AND account->'details' ? 'service_account';
