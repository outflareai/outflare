-- Sales alerts: Outflare posts each new Sales conversation to one Slack
-- channel the brand picks, and keeps the message current as the conversation
-- moves (seen, assigned, talking, archived), so the team's Slack and the
-- Sales queue say the same thing.
CREATE TABLE sales_alert_settings (
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 integration_id uuid REFERENCES integrations(id) ON DELETE SET NULL,
 channel_id text NOT NULL DEFAULT '' CHECK(length(channel_id) <= 40),
 channel_name text NOT NULL DEFAULT '',
 enabled boolean NOT NULL DEFAULT false,
 -- Whether conversations that started from an email reply are posted.
 replies boolean NOT NULL DEFAULT true,
 -- Only conversations that arrive after alerts were last turned on are
 -- posted, so turning them on never floods the channel with old ones.
 enabled_at timestamptz,
 error_code text NOT NULL DEFAULT '',
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(workspace_id,brand_id)
);
CREATE INDEX sales_alert_settings_brand ON sales_alert_settings(brand_id);
CREATE INDEX sales_alert_settings_integration ON sales_alert_settings(integration_id);

-- Whether conversations a Sales channel starts are posted as alerts.
ALTER TABLE sales_feed_channels ADD COLUMN alert boolean NOT NULL DEFAULT true;

-- The Slack message that stands for a conversation: where it is, a hash of
-- its text and the emojis on it, so a change posts only what differs.
CREATE TABLE sales_lead_alerts (
 lead_id uuid PRIMARY KEY REFERENCES sales_leads(id) ON DELETE CASCADE,
 workspace_id uuid NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
 brand_id uuid NOT NULL REFERENCES brands(id) ON DELETE CASCADE,
 channel_id text NOT NULL,
 slack_ts text NOT NULL,
 text_hash text NOT NULL,
 reactions text[] NOT NULL DEFAULT '{}',
 posted_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sales_lead_alerts_workspace ON sales_lead_alerts(workspace_id);
CREATE INDEX sales_lead_alerts_brand ON sales_lead_alerts(brand_id);
