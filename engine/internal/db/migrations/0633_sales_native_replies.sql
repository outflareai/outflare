-- Sales conversations Outflare opens itself from replies it reads from a
-- connected sequencer (docs/engineering/sales-workflow.md, "Replies
-- Outflare reads itself").

-- Which sequencer reply opened a conversation, as a hash of the connection
-- and the sequencer's id for the reply, so the same reply arriving from an
-- outside importer and from Outflare's own read finds one conversation.
-- Conversations from before this have none; they are found by the copies
-- of their messages.
ALTER TABLE sales_leads ADD COLUMN reply_hash text;
CREATE INDEX sales_leads_reply ON sales_leads(workspace_id,brand_id,reply_hash) WHERE reply_hash IS NOT NULL;
-- The conversation a person already has on a connection.
CREATE INDEX sales_leads_person ON sales_leads(workspace_id,brand_id,record_id,integration_id);

-- Whether Outflare added a person to a sequencer campaign: only their
-- replies open a conversation.
CREATE INDEX audit_events_lead_pushed ON audit_events(workspace_id,(data->>'record_id')) WHERE action='sequence.lead_pushed';
