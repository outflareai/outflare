-- Sales channels: what a channel's posts show first, and how good a
-- conversation was.
--
-- preview_fields is the ordered labels of the form or ticket fields a
-- channel's posts lead with in the queue and on the conversation page
-- ("Expected monthly traffic", "Use case"); empty means Outflare picks.
ALTER TABLE sales_feed_channels ADD COLUMN preview_fields text[] NOT NULL DEFAULT '{}';

-- quality is a rep's mark on a conversation, counted per source so the team
-- sees which source sends what; null until someone marks it.
ALTER TABLE sales_leads ADD COLUMN quality text CHECK (quality IN ('good','poor_fit','junk'));
CREATE INDEX sales_leads_quality ON sales_leads(workspace_id,brand_id,quality) WHERE quality IS NOT NULL;
