-- Brands own their data, step 5 of making brands fully separate
-- (plans/brands/README.md): AI visibility, link building and digest
-- settings, and the digests themselves, carry a brand. Expand only
-- (docs/migrations.md): each settings row goes to the workspace's first
-- brand and stays the workspace's one row until the contract step drops
-- the per-workspace keys, which the version before this one upserts on.

DO $$
DECLARE
    t text;
BEGIN
    FOREACH t IN ARRAY ARRAY['ai_visibility_settings', 'link_building_settings', 'digest_settings', 'digests']
    LOOP
        EXECUTE format('ALTER TABLE %I ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL', t);
        EXECUTE format('UPDATE %I SET brand_id = outflare_first_brand(workspace_id)', t);
        EXECUTE format('CREATE TRIGGER %I BEFORE INSERT ON %I FOR EACH ROW EXECUTE FUNCTION outflare_tracking_brand()', t || '_brand', t);
    END LOOP;
END
$$;

-- One settings row per brand, next to one per workspace until the
-- contract step.
CREATE UNIQUE INDEX ai_visibility_settings_brand_idx ON ai_visibility_settings (brand_id);
CREATE UNIQUE INDEX link_building_settings_brand_idx ON link_building_settings (brand_id);
CREATE UNIQUE INDEX digest_settings_brand_idx ON digest_settings (brand_id);
-- One scheduled digest per brand and period.
CREATE UNIQUE INDEX digests_brand_period_idx ON digests (brand_id, frequency, period_end) WHERE trigger = 'scheduled';
CREATE INDEX digests_brand_idx ON digests (brand_id, created_at DESC);
