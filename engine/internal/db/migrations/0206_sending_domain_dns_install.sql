-- The last one-click run that added a cold email domain's missing SPF or
-- DMARC record through the workspace's own DNS host connection.
ALTER TABLE sending_domains ADD COLUMN IF NOT EXISTS dns_install jsonb;
