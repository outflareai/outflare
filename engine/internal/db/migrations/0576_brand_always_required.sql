-- A mailbox, sending domain, ad account, or a row stored for one of them or
-- for an analytics site, is refused without a brand. 0575 gave every
-- existing row one. NOT VALID: no table scan, so the locks last moments; a
-- later release validates and sets NOT NULL.
-- migration-safety: since 0566 and 0567 every code path writes these tables with a brand (connection, domain, site or first brand), and 0575 filled the rest; the previous version runs the same code

ALTER TABLE ad_accounts ADD CONSTRAINT ad_accounts_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE ad_change_events ADD CONSTRAINT ad_change_events_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE ad_clicks ADD CONSTRAINT ad_clicks_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE ad_conversion_rules ADD CONSTRAINT ad_conversion_rules_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE analytics_daily ADD CONSTRAINT analytics_daily_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE analytics_syncs ADD CONSTRAINT analytics_syncs_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE mailbox_assignments ADD CONSTRAINT mailbox_assignments_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE mailbox_platforms ADD CONSTRAINT mailbox_platforms_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE mailboxes ADD CONSTRAINT mailboxes_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE organic_conversion_syncs ADD CONSTRAINT organic_conversion_syncs_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE page_conversion_syncs ADD CONSTRAINT page_conversion_syncs_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE page_conversions ADD CONSTRAINT page_conversions_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE search_completeness ADD CONSTRAINT search_completeness_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE search_daily ADD CONSTRAINT search_daily_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE search_sitemaps ADD CONSTRAINT search_sitemaps_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE search_syncs ADD CONSTRAINT search_syncs_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE sending_domains ADD CONSTRAINT sending_domains_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
ALTER TABLE url_inspections ADD CONSTRAINT url_inspections_brand_required CHECK (brand_id IS NOT NULL) NOT VALID;
