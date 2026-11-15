-- Brands own their data: 15 more tables where only tests still wrote a row
-- without a brand. Old rows with none get the workspace's first brand, as in
-- 0555. Row locks only.

UPDATE ad_search_terms_daily SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE backlinks SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE email_templates SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE fix_tickets SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE fixes SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE journeys SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE link_reads SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE mission_actions SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE mission_updates SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE missions SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE seo_sites SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE social_accounts SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE social_actions SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE social_publications SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE tracking_campaigns SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
