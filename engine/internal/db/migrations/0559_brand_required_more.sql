-- A row of these tables must belong to a brand, as in 0556 (0558 gave the
-- old ones theirs). NOT VALID: no table scan, so the locks last moments.
-- migration-safety: no code path writes these tables without brand_id; only test fixtures did, and they set it since the previous release

ALTER TABLE ad_search_terms_daily ADD CONSTRAINT ad_search_terms_daily_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE backlinks ADD CONSTRAINT backlinks_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE email_templates ADD CONSTRAINT email_templates_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE fix_tickets ADD CONSTRAINT fix_tickets_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE fixes ADD CONSTRAINT fixes_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE journeys ADD CONSTRAINT journeys_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE link_reads ADD CONSTRAINT link_reads_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE mission_actions ADD CONSTRAINT mission_actions_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE mission_updates ADD CONSTRAINT mission_updates_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE missions ADD CONSTRAINT missions_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE seo_sites ADD CONSTRAINT seo_sites_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE social_accounts ADD CONSTRAINT social_accounts_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE social_actions ADD CONSTRAINT social_actions_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE social_publications ADD CONSTRAINT social_publications_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE tracking_campaigns ADD CONSTRAINT tracking_campaigns_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
