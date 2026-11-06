-- Brands own their data, step 6c of making brands fully separate
-- (plans/brands/README.md): the last tables in brandtables/pending.txt.
-- Expand only (docs/migrations.md).

-- The brand a website (host) belongs to: the active brand with the longest
-- domain it is on, else the workspace's only brand; none for a host no
-- brand claims (a competitor's site). The same rule the API reads hosts by.
CREATE FUNCTION outflare_host_brand(ws uuid, host text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT COALESCE(
        (SELECT b.id FROM brands b, unnest(b.domains) d
         WHERE b.workspace_id = ws AND b.status = 'active' AND (lower(host) = lower(d) OR lower(host) LIKE '%.' || lower(d))
         ORDER BY length(d) DESC, b.created_at LIMIT 1),
        outflare_only_brand(ws))
$$;

CREATE FUNCTION outflare_fill_host_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.brand_id := outflare_host_brand(NEW.workspace_id, NEW.host);
    RETURN NEW;
END
$$;

-- AI crawler hits and SEO metrics of a website belong to the brand whose
-- website it is, worked out again each time a sync writes a row, so a
-- brand's new domain is picked up by the next sync.
ALTER TABLE ai_bot_hits ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE seo_snapshots ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE ai_bot_hits SET brand_id = outflare_host_brand(workspace_id, host);
UPDATE seo_snapshots SET brand_id = outflare_host_brand(workspace_id, host);
CREATE INDEX ai_bot_hits_brand_idx ON ai_bot_hits (brand_id, day) WHERE brand_id IS NOT NULL;
CREATE INDEX seo_snapshots_brand_idx ON seo_snapshots (brand_id, day) WHERE brand_id IS NOT NULL;
CREATE TRIGGER ai_bot_hits_brand BEFORE INSERT OR UPDATE ON ai_bot_hits
    FOR EACH ROW EXECUTE FUNCTION outflare_fill_host_brand();
CREATE TRIGGER seo_snapshots_brand BEFORE INSERT OR UPDATE ON seo_snapshots
    FOR EACH ROW EXECUTE FUNCTION outflare_fill_host_brand();

-- A sequence's reply rules carry its brand and move with it. The rules with
-- no sequence are the workspace's default for every sequence, and have none.
ALTER TABLE reply_rules ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE reply_rules r SET brand_id = s.brand_id FROM sequences s WHERE s.id = r.sequence_id;
CREATE INDEX reply_rules_brand_idx ON reply_rules (brand_id) WHERE brand_id IS NOT NULL;

CREATE FUNCTION outflare_fill_reply_rule_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.brand_id IS NULL AND NEW.sequence_id IS NOT NULL THEN
        SELECT brand_id INTO NEW.brand_id FROM sequences WHERE id = NEW.sequence_id;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER reply_rules_brand BEFORE INSERT ON reply_rules
    FOR EACH ROW EXECUTE FUNCTION outflare_fill_reply_rule_brand();
CREATE TRIGGER sequences_reply_rules_moved AFTER UPDATE OF brand_id ON sequences
    FOR EACH ROW WHEN (OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_children_moved('reply_rules', 'sequence_id');
