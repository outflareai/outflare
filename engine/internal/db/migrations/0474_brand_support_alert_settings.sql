-- Brands are fully separate, so the support and alert settings saved for
-- the whole workspace (brand_id NULL) stop counting for every brand. Each
-- brand without its own starts with a copy of the workspace's. The
-- workspace rows stay as they were: the previous version still falls back
-- to them during a deploy, and nothing reads them after it.

-- The support assistant: one per brand.
INSERT INTO support_assistant_settings (workspace_id, brand_id, chat_mode, email_mode, name, find_on_website,
    slack_integration_id, slack_channels, email_hold_minutes, created_at, updated_at)
SELECT s.workspace_id, b.id, s.chat_mode, s.email_mode, s.name, s.find_on_website,
    s.slack_integration_id, s.slack_channels, s.email_hold_minutes, s.created_at, s.updated_at
FROM support_assistant_settings s
JOIN brands b ON b.workspace_id = s.workspace_id
WHERE s.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM support_assistant_settings o WHERE o.workspace_id = s.workspace_id AND o.brand_id = b.id);

-- Reply time targets: one per brand.
INSERT INTO support_sla_policies (workspace_id, brand_id, enabled, first_reply_minutes, next_reply_minutes,
    resolution_minutes, timezone, hours, created_at, updated_at)
SELECT s.workspace_id, b.id, s.enabled, s.first_reply_minutes, s.next_reply_minutes,
    s.resolution_minutes, s.timezone, s.hours, s.created_at, s.updated_at
FROM support_sla_policies s
JOIN brands b ON b.workspace_id = s.workspace_id
WHERE s.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM support_sla_policies o WHERE o.workspace_id = s.workspace_id AND o.brand_id = b.id);

-- Where each kind of alert goes: one per brand and kind.
INSERT INTO alert_settings (workspace_id, brand_id, kind, email, slack_integration_id, slack_channel,
    slack_channel_name, updated_by, updated_at)
SELECT s.workspace_id, b.id, s.kind, s.email, s.slack_integration_id, s.slack_channel,
    s.slack_channel_name, s.updated_by, s.updated_at
FROM alert_settings s
JOIN brands b ON b.workspace_id = s.workspace_id
WHERE s.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM alert_settings o WHERE o.workspace_id = s.workspace_id AND o.brand_id = b.id AND o.kind = s.kind);

-- A brand's own row fell back to the workspace's for what it left empty
-- (email, Slack); it keeps those as its own now.
UPDATE alert_settings o SET email = COALESCE(o.email, s.email),
    slack_integration_id = COALESCE(o.slack_integration_id, s.slack_integration_id),
    slack_channel = CASE WHEN o.slack_integration_id IS NULL THEN s.slack_channel ELSE o.slack_channel END,
    slack_channel_name = CASE WHEN o.slack_integration_id IS NULL THEN s.slack_channel_name ELSE o.slack_channel_name END
FROM alert_settings s
WHERE s.brand_id IS NULL AND o.brand_id IS NOT NULL AND o.workspace_id = s.workspace_id AND o.kind = s.kind
  AND (o.email IS NULL OR o.slack_integration_id IS NULL);

-- Hand-off topics: each brand gets a copy of the workspace's, unless it
-- already has one of that name.
INSERT INTO support_handoff_topics (workspace_id, brand_id, name, description, assignee_id, team_id,
    always_handoff, created_at, updated_at)
SELECT t.workspace_id, b.id, t.name, t.description, t.assignee_id, t.team_id,
    t.always_handoff, t.created_at, t.updated_at
FROM support_handoff_topics t
JOIN brands b ON b.workspace_id = t.workspace_id
WHERE t.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM support_handoff_topics o WHERE o.workspace_id = t.workspace_id
    AND o.brand_id = b.id AND lower(o.name) = lower(t.name));

-- A brand's conversations point at its own copy of their topic.
UPDATE support_conversations c SET topic_id = n.id
FROM support_handoff_topics t, support_handoff_topics n
WHERE c.topic_id = t.id AND t.brand_id IS NULL AND c.brand_id IS NOT NULL
  AND n.workspace_id = t.workspace_id AND n.brand_id = c.brand_id AND lower(n.name) = lower(t.name);

-- A chat widget or help centre is installed on a site, so it can't be
-- copied: one with no brand belongs to the workspace's first brand.
UPDATE support_chat_widgets SET brand_id = outflare_first_brand(workspace_id)
WHERE brand_id IS NULL;

UPDATE help_centres h SET brand_id = outflare_first_brand(h.workspace_id)
WHERE h.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM help_centres o WHERE o.workspace_id = h.workspace_id
    AND o.brand_id = outflare_first_brand(h.workspace_id));
