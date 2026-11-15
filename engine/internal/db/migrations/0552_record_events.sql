-- Deals, support conversations and CRM tasks on people's and companies'
-- timelines, as customer events (deal.won, support.conversation_opened,
-- task.completed), so rules, journeys and metrics can act on them like
-- any other event. Each row remembers the last state it recorded; the
-- record events loop (engine/internal/recordevents) records what is new.
--
-- Rows from before this start as 'history': the loop notes their state
-- without recording events, so nothing from the past sets an automation
-- off. New rows start empty and get their first event. The version before
-- ignores the columns.
ALTER TABLE deals ADD COLUMN event_state text DEFAULT 'history';
ALTER TABLE deals ALTER COLUMN event_state DROP DEFAULT;
ALTER TABLE support_conversations ADD COLUMN event_state text DEFAULT 'history';
ALTER TABLE support_conversations ALTER COLUMN event_state DROP DEFAULT;
ALTER TABLE crm_activities ADD COLUMN event_state text DEFAULT 'history';
ALTER TABLE crm_activities ALTER COLUMN event_state DROP DEFAULT;

-- The rows that wait for an event, so each round reads only those.
CREATE INDEX deals_event_pending ON deals (id) WHERE event_state IS DISTINCT FROM (state || ':' || stage);
CREATE INDEX support_conversations_event_pending ON support_conversations (id)
    WHERE record_id IS NOT NULL AND event_state IS DISTINCT FROM (CASE WHEN status = 'closed' THEN 'closed' ELSE 'open' END);
CREATE INDEX crm_activities_event_pending ON crm_activities (id) WHERE kind = 'task' AND event_state IS DISTINCT FROM status;
