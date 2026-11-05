-- Brands own their data, step 2 of making brands fully separate
-- (plans/brands/README.md), part 1: flag groups, tracking link clicks,
-- visitor alerts, ad settings and ad metrics carry brand_id on the row
-- itself, so a brand's tracking data is
-- exactly WHERE brand_id = $1. Expand only (docs/migrations.md): columns,
-- triggers, backfills and per-brand unique indexes. The per-workspace
-- unique rules these replace go in the contract migration, once no
-- serving version upserts against them.
--
-- Conversions, attribution models and their pins, and ad conversion rules
-- and sends follow in the next migration: the version before this one
-- lists conversions and models across the workspace, and its brand-leak
-- test can't seed ad conversion rules, so a brand column on them now
-- would fail that version's tests on this schema.
--
-- What was for every brand goes to the workspace's first brand. Ad
-- metrics take the brand of their ad (entity), else of its account; an ad
-- account nobody assigned to a brand has none, so its rows show in no
-- brand (no brand, no data).

CREATE FUNCTION outflare_tracking_brand() RETURNS trigger
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
    ELSIF TG_TABLE_NAME = 'ad_metrics_daily' THEN
        SELECT COALESCE(e.brand_id, a.brand_id) INTO b FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id WHERE e.id = NEW.ad_entity_id;
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
        'flag_groups',
        'tracking_link_clicks', 'visitor_alerts', 'ad_settings', 'ad_metrics_daily']
    LOOP
        EXECUTE format('ALTER TABLE %I ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_tracking_brand()', t || '_brand', t);
    END LOOP;
END
$$;

-- Backfills, parents first.
UPDATE flag_groups SET brand_id = outflare_first_brand(workspace_id);
UPDATE tracking_link_clicks c SET brand_id = COALESCE(l.brand_id, outflare_first_brand(c.workspace_id)) FROM tracking_links l WHERE l.id = c.link_id;
UPDATE visitor_alerts v SET brand_id = COALESCE(r.brand_id, outflare_first_brand(v.workspace_id)) FROM records r WHERE r.id = v.record_id;
UPDATE ad_settings SET brand_id = outflare_first_brand(workspace_id);
UPDATE ad_metrics_daily m SET brand_id = COALESCE(e.brand_id, a.brand_id)
FROM ad_entities e JOIN ad_accounts a ON a.id = e.ad_account_id WHERE e.id = m.ad_entity_id;

-- The settings that were one per workspace are one per brand.
UPDATE visitor_settings SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE attribution_settings SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

-- The per-brand twins of the per-workspace unique rules. The contract
-- migration drops the per-workspace ones, so two brands can each use the
-- same key, name or slug.
CREATE UNIQUE INDEX flag_groups_brand_name_idx ON flag_groups (brand_id, name);
CREATE UNIQUE INDEX flags_brand_key_idx ON flags (brand_id, key) WHERE status <> 'archived';
CREATE UNIQUE INDEX tracking_campaigns_brand_slug_idx ON tracking_campaigns (brand_id, slug);
CREATE UNIQUE INDEX visitor_settings_brand_idx ON visitor_settings (brand_id);
CREATE UNIQUE INDEX attribution_settings_brand_idx ON attribution_settings (brand_id);
CREATE UNIQUE INDEX ad_settings_brand_idx ON ad_settings (brand_id);

-- What a brand's pages read.
CREATE INDEX ad_metrics_daily_brand_idx ON ad_metrics_daily (brand_id, day);
CREATE INDEX tracking_link_clicks_brand_idx ON tracking_link_clicks (brand_id, clicked_at DESC);
