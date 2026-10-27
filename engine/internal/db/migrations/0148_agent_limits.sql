-- Limits on what connected AI apps do on their own.
--
-- A token that requires a mission makes changes only inside a mission a
-- person approved (the Outflare-Mission header): its writes then follow
-- the mission's mode, dry run or approvals. Reads are unaffected. Apps
-- connected with OAuth get it on; a person can turn it off per app.
ALTER TABLE api_tokens ADD COLUMN require_mission boolean NOT NULL DEFAULT false;

-- When the server's operator checked an app that registered itself. Until
-- then an app Outflare doesn't recognise by its redirect address (Claude,
-- ChatGPT, Cursor, VS Code, an app on the person's own computer) may only
-- read.
ALTER TABLE oauth_clients ADD COLUMN verified_at timestamptz;
