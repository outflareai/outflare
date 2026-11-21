-- Alerts in Telegram (plans/ad-tracker.md, B8): beside email and Slack, a
-- kind of alert can post to a chat of the workspace's own Telegram bot
-- (the telegram_bot connector), chosen per brand like the Slack channel.
ALTER TABLE alert_settings
    ADD COLUMN telegram_integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- The chat as Telegram takes it: its number, or a public channel's
    -- @name; and its name when it was picked, for the settings page.
    ADD COLUMN telegram_chat text,
    ADD COLUMN telegram_chat_name text;

-- Expand only (docs/migrations.md): the CHECK takes one more value and the
-- previous version keeps writing the old ones.
ALTER TABLE alert_deliveries DROP CONSTRAINT alert_deliveries_channel_check;
ALTER TABLE alert_deliveries ADD CONSTRAINT alert_deliveries_channel_check
    CHECK (channel IN ('email', 'slack', 'telegram'));
