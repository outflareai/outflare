-- Brands are fully separate: website tracking, events, webhooks and the
-- support address saved with no brand become one brand's.
--
-- Rows with no brand that are the workspace's own stay as they are: the
-- AI receipts address (it serves the workspace's AI key) and webhook
-- events about the team, which every endpoint still gets.
--
-- Expand only: the previous version reads its own brand's rows and those
-- with no brand, so during a deploy each brand still sees each item.

-- Site keys: the brand whose domain the first origin is on, else the
-- workspace's first brand. What a key collected follows it.
UPDATE site_keys k SET brand_id = COALESCE(
    outflare_host_brand(k.workspace_id, lower(substring(k.origins[1] FROM '^[a-z]+://([^/:]+)'))),
    outflare_first_brand(k.workspace_id))
WHERE k.brand_id IS NULL;
UPDATE consent_banners x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id AND x.brand_id IS NULL;
UPDATE cookie_policies x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id AND x.brand_id IS NULL;
UPDATE cookie_scans x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id AND x.brand_id IS NULL;
UPDATE site_cookies x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.site_key_id AND x.brand_id IS NULL;
UPDATE site_event_inbox x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.key_id AND x.brand_id IS NULL;
UPDATE site_key_days x SET brand_id = k.brand_id FROM site_keys k WHERE k.id = x.key_id AND x.brand_id IS NULL;

-- Website visits: the key's brand, else the visitor's record's, else the
-- first brand.
UPDATE site_sessions v SET brand_id = COALESCE(
    (SELECT k.brand_id FROM site_keys k WHERE k.id = v.key_id),
    (SELECT r.brand_id FROM records r WHERE r.id = v.record_id),
    outflare_first_brand(v.workspace_id))
WHERE v.brand_id IS NULL;

-- Customer events: their record's brand, else the first brand.
UPDATE customer_events e SET brand_id = COALESCE(
    (SELECT r.brand_id FROM records r WHERE r.id = e.record_id),
    outflare_first_brand(e.workspace_id))
WHERE e.brand_id IS NULL;

-- Webhook endpoints: the first brand (the secret stays the endpoint's).
-- Their deliveries follow.
UPDATE webhook_endpoints SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE webhook_deliveries x SET brand_id = e.brand_id FROM webhook_endpoints e WHERE e.id = x.endpoint_id AND x.brand_id IS NULL;

-- A support address with no brand is the first brand's, unless that brand
-- already has a live one; its mail follows (inbound_addresses_children_moved).
UPDATE inbound_addresses a SET brand_id = outflare_first_brand(a.workspace_id)
WHERE a.purpose = 'support' AND a.brand_id IS NULL
  AND (a.disabled_at IS NOT NULL OR NOT EXISTS (SELECT 1 FROM inbound_addresses o WHERE o.workspace_id = a.workspace_id
    AND o.purpose = 'support' AND o.brand_id = outflare_first_brand(a.workspace_id) AND o.disabled_at IS NULL));
