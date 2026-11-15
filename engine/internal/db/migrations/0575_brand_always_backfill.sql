-- Every mailbox, sending domain, ad account and analytics site belongs to a
-- brand (0566, 0567), and so does everything stored for them. Rows written
-- without one since then get the brand they would get now. The next
-- migration then refuses a row without a brand.

UPDATE sending_domains SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE mailboxes m SET brand_id = COALESCE((SELECT d.brand_id FROM sending_domains d WHERE d.workspace_id = m.workspace_id AND d.domain = split_part(m.email, '@', 2)), outflare_first_brand(m.workspace_id)) WHERE brand_id IS NULL;
UPDATE ad_accounts SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE mailbox_assignments c SET brand_id = p.brand_id FROM mailboxes p WHERE c.mailbox_id = p.id AND c.brand_id IS NULL;
UPDATE mailbox_platforms c SET brand_id = p.brand_id FROM mailboxes p WHERE c.mailbox_id = p.id AND c.brand_id IS NULL;
-- An ad campaign, ad set or ad with no brand of its own follows its ad
-- account (COALESCE(e.brand_id, a.brand_id) everywhere), so moving the
-- account moves them. 0567 wrote the account's brand onto them; give that
-- back, so they follow the account again. Only a brand that differs from
-- the account's is a choice of its own.
UPDATE ad_entities e SET brand_id = NULL FROM ad_accounts a WHERE e.ad_account_id = a.id AND e.brand_id = a.brand_id;
UPDATE ad_change_events c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_clicks c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_conversion_rules c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE search_completeness SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE analytics_daily SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE analytics_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_daily SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE search_sitemaps SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE url_inspections SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE page_conversions SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE page_conversion_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
UPDATE organic_conversion_syncs SET brand_id = outflare_site_brand_or_first(integration_id, site) WHERE brand_id IS NULL;
