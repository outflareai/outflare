-- Which tool posted the Slack post that started a Sales conversation: the
-- posting app's own name as Slack gives it ("Fillout"), so the queue can show
-- and filter by it. NULL when a person posted it, and for a conversation that
-- did not start from a Sales channel. An app's name, never a person's.
ALTER TABLE sales_leads ADD COLUMN feed_source text;
CREATE INDEX sales_leads_feed_source ON sales_leads(workspace_id,brand_id,feed_source) WHERE feed_source IS NOT NULL;
