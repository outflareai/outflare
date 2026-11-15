-- Every mailbox, sending domain and ad account belongs to a
-- brand (the owner's choice, 14 Nov): no more "Not in a brand". Where
-- nothing else says which brand, the workspace's first brand gets it; a
-- person can move it after. Analytics sites follow in a later step.

-- The brand a mailbox joins: its connection's, else its domain's, else the
-- workspace's first.
CREATE OR REPLACE FUNCTION outflare_mailbox_brand(ws uuid, email text, integration uuid) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT brand_id FROM integrations WHERE id = integration AND workspace_id = ws),
        (SELECT brand_id FROM sending_domains WHERE workspace_id = ws AND domain = lower(split_part(email, '@', 2))),
        outflare_first_brand(ws))
$$;

-- What was in no brand moves to the first brand. Row locks only.
UPDATE sending_domains SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE mailboxes m SET brand_id = COALESCE(
        (SELECT d.brand_id FROM sending_domains d WHERE d.workspace_id = m.workspace_id AND d.domain = lower(split_part(m.email, '@', 2))),
        outflare_first_brand(m.workspace_id))
    WHERE m.brand_id IS NULL;
UPDATE ad_accounts SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

-- What hangs off them follows.
UPDATE mailbox_assignments c SET brand_id = p.brand_id FROM mailboxes p WHERE c.mailbox_id = p.id AND c.brand_id IS NULL;
UPDATE mailbox_platforms c SET brand_id = p.brand_id FROM mailboxes p WHERE c.mailbox_id = p.id AND c.brand_id IS NULL;
UPDATE mailbox_locks c SET brand_id = p.brand_id FROM mailboxes p WHERE c.mailbox_id = p.id AND c.brand_id IS NULL;
UPDATE ad_entities c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_keywords c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_search_terms_daily c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_change_events c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_clicks c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_conversion_rules c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_account_changes c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_acquisitions c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;
UPDATE ad_drafts c SET brand_id = p.brand_id FROM ad_accounts p WHERE c.ad_account_id = p.id AND c.brand_id IS NULL;

