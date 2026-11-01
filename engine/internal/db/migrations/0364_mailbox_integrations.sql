-- Google and Microsoft mailbox sign-ins become connections like any other
-- (the google_mail and outlook_mail connectors), so a mailbox that signs in
-- with OAuth points at its connection in integrations. The connection is
-- either one person's sign-in (oauth2: its refresh token is the
-- connection's credentials) or an admin connection (api_key: a Google
-- service account or a Microsoft Entra app) that signs in many mailboxes.
--
-- Expand only: oauth_connections and mailboxes.oauth_connection_id stay
-- for the mailboxes connected before this, until their rows are moved
-- and a later release removes them.

ALTER TABLE mailboxes ADD COLUMN integration_id uuid REFERENCES integrations (id) ON DELETE RESTRICT;
CREATE INDEX mailboxes_integration_idx ON mailboxes (integration_id) WHERE integration_id IS NOT NULL;
