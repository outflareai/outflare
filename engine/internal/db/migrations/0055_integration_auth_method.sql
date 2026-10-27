-- Connectors can offer more than one way to connect (OAuth or an API key),
-- so each connection records the one it used. Token refresh only applies
-- to oauth2 connections.
ALTER TABLE integrations ADD COLUMN auth_method text NOT NULL DEFAULT 'api_key'
    CHECK (auth_method IN ('api_key', 'oauth2'));

UPDATE integrations SET auth_method = 'oauth2'
WHERE connector IN ('google_analytics', 'google_search_console', 'google_calendar', 'outlook_calendar', 'hubspot', 'posthog_cloud');

-- PostHog and HubSpot were two connectors each, one per way to connect.
-- They are one connector now that a connector can offer both.
UPDATE integrations SET connector = 'posthog' WHERE connector = 'posthog_cloud';
UPDATE integrations SET connector = 'hubspot' WHERE connector = 'hubspot_private_app';
UPDATE connect_requests SET connector = 'posthog' WHERE connector = 'posthog_cloud';
UPDATE connect_requests SET connector = 'hubspot' WHERE connector = 'hubspot_private_app';
UPDATE analytics_daily SET connector = 'posthog' WHERE connector = 'posthog_cloud';
UPDATE import_runs SET connector = 'hubspot' WHERE connector = 'hubspot_private_app';
