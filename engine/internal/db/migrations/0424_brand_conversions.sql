-- Brands own their data, step 2 of making brands fully separate
-- (plans/brands/README.md), part 2: conversion definitions, attribution
-- models and their pins, and ad conversion rules and sends carry brand_id
-- on the row itself. Expand only (docs/migrations.md). The version before
-- this one already lists conversions and models per brand (reading the
-- column through to_jsonb), lists ad conversion rules and sends by their
-- account's brand, and its brand-leak test seeds these tables, so it still
-- passes on this schema.
--
-- What was for every brand goes to the workspace's first brand. An ad
-- conversion rule takes its ad account's brand, and a send its rule's; an
-- account nobody assigned to a brand has none, so its rules and sends show
-- in no brand (no brand, no data).

CREATE OR REPLACE FUNCTION outflare_tracking_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    b uuid;
BEGIN
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- One branch per table: plpgsql only resolves the one it runs.
    IF TG_TABLE_NAME = 'tracking_link_clicks' THEN
        SELECT brand_id INTO b FROM tracking_links WHERE id = NEW.link_id;
    ELSIF TG_TABLE_NAME = 'visitor_alerts' THEN
        SELECT brand_id INTO b FROM records WHERE id = NEW.record_id;
    ELSIF TG_TABLE_NAME = 'attribution_model_pins' THEN
        SELECT brand_id INTO b FROM attribution_models WHERE id = NEW.model_id;
    ELSIF TG_TABLE_NAME = 'ad_metrics_daily' THEN
        SELECT COALESCE(e.brand_id, a.brand_id) INTO b FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id WHERE e.id = NEW.ad_entity_id;
        NEW.brand_id := b;
        RETURN NEW;
    ELSIF TG_TABLE_NAME = 'ad_conversion_rules' THEN
        SELECT brand_id INTO b FROM ad_accounts WHERE id = NEW.ad_account_id;
        NEW.brand_id := b;
        RETURN NEW;
    ELSIF TG_TABLE_NAME = 'ad_conversion_sends' THEN
        SELECT brand_id INTO b FROM ad_conversion_rules WHERE id = NEW.rule_id;
        NEW.brand_id := b;
        RETURN NEW;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY[
        'conversion_definitions', 'attribution_models', 'attribution_model_pins',
        'ad_conversion_rules', 'ad_conversion_sends']
    LOOP
        EXECUTE format('ALTER TABLE %I ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_tracking_brand()', t || '_brand', t);
    END LOOP;
END
$$;

UPDATE conversion_definitions SET brand_id = outflare_first_brand(workspace_id);
UPDATE attribution_models SET brand_id = outflare_first_brand(workspace_id);
UPDATE attribution_model_pins p SET brand_id = m.brand_id FROM attribution_models m WHERE m.id = p.model_id;
UPDATE ad_conversion_rules r SET brand_id = a.brand_id FROM ad_accounts a WHERE a.id = r.ad_account_id;
UPDATE ad_conversion_sends s SET brand_id = r.brand_id FROM ad_conversion_rules r WHERE r.id = s.rule_id;

-- The per-brand twins of the per-workspace unique rules, which the
-- contract migration drops.
CREATE UNIQUE INDEX conversion_definitions_brand_key_idx ON conversion_definitions (brand_id, key);
CREATE UNIQUE INDEX attribution_models_brand_key_idx ON attribution_models (brand_id, key, version);
CREATE INDEX ad_conversion_sends_brand_idx ON ad_conversion_sends (brand_id, created_at DESC);
