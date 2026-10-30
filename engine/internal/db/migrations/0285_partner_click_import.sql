-- Clicks brought in from another partner platform keep its id, so a second
-- import run doesn't count them twice. Clicks Outflare records have none.
ALTER TABLE partner_clicks ADD COLUMN external_id text CHECK (length(external_id) BETWEEN 1 AND 200);
CREATE UNIQUE INDEX partner_clicks_external_idx ON partner_clicks (workspace_id, external_id) WHERE external_id IS NOT NULL;
