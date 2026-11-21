-- Lead source category: every person is Inbound (they came to the brand) or
-- Outbound (the brand went to them), worked out from their Source, so the
-- People list and the Sales queue can be narrowed to either. Cold and
-- Events and conferences are Outbound; Inbound and Sign-up are Inbound;
-- Invited, Customer and Other have none. It follows Source: when a person
-- corrects their Source, the category changes with it, in their history.
-- A brand that made its own field called source or source_category keeps
-- it, and nothing here touches it.

CREATE FUNCTION outflare_source_category(source text) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN source IN ('cold', 'event') THEN 'outbound' WHEN source IN ('inbound', 'signup') THEN 'inbound' END
$$;

-- Runs after records_person_source (triggers fire in name order), so it
-- sees the Source that one worked out.
CREATE FUNCTION outflare_person_source_category() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    want text;
BEGIN
    IF NEW.record_type <> 'person' OR EXISTS (SELECT 1 FROM field_definitions f WHERE f.workspace_id = NEW.workspace_id
            AND f.record_type = 'person' AND f.key IN ('source', 'source_category') AND f.brand_id IS NOT DISTINCT FROM NEW.brand_id) THEN
        RETURN NEW;
    END IF;
    NEW.attributes := COALESCE(NEW.attributes, '{}'::jsonb);
    NEW.attribute_sources := COALESCE(NEW.attribute_sources, '{}'::jsonb);
    want := outflare_source_category(NEW.attributes ->> 'source');
    IF want IS NULL THEN
        NEW.attributes := NEW.attributes - 'source_category';
        NEW.attribute_sources := NEW.attribute_sources - 'source_category';
    ELSE
        NEW.attributes := NEW.attributes || jsonb_build_object('source_category', want);
        NEW.attribute_sources := NEW.attribute_sources || '{"source_category": "first_party"}'::jsonb;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER records_person_source_category BEFORE INSERT OR UPDATE OF attributes, origin ON records
    FOR EACH ROW EXECUTE FUNCTION outflare_person_source_category();

-- Each change of category goes in the person's history.
CREATE FUNCTION outflare_person_source_category_history() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    was jsonb;
BEGIN
    IF NEW.record_type <> 'person' THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'UPDATE' THEN
        was := OLD.attributes -> 'source_category';
    END IF;
    IF was IS DISTINCT FROM (NEW.attributes -> 'source_category') THEN
        INSERT INTO attribute_changes (workspace_id, brand_id, record_id, field_key, old_value, new_value, source)
        VALUES (NEW.workspace_id, NEW.brand_id, NEW.id, 'source_category', was, NEW.attributes -> 'source_category', 'first_party');
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER records_person_source_category_history AFTER INSERT OR UPDATE OF attributes, origin ON records
    FOR EACH ROW EXECUTE FUNCTION outflare_person_source_category_history();

-- People a Sales channel brought in were guessed Sign-up, because they were
-- added as first-party. Someone who filled in a form or was handed over by
-- support came inbound: correct the guess (never a Source a person set),
-- in their history.
WITH fixed AS (
    UPDATE records r SET attributes = r.attributes || '{"source": "inbound"}'::jsonb, version = r.version + 1
    WHERE r.record_type = 'person' AND r.acquired_via = 'slack_feeds' AND r.merged_into IS NULL
      AND r.attributes ->> 'source' = 'signup' AND r.attribute_sources ->> 'source' = 'first_party'
      AND EXISTS (SELECT 1 FROM sales_leads l WHERE l.workspace_id = r.workspace_id AND l.record_id = r.id AND l.feed_kind IS NOT NULL)
      AND NOT EXISTS (SELECT 1 FROM field_definitions f WHERE f.workspace_id = r.workspace_id AND f.record_type = 'person'
          AND f.key = 'source' AND f.brand_id IS NOT DISTINCT FROM r.brand_id)
    RETURNING r.workspace_id, r.brand_id, r.id)
INSERT INTO attribute_changes (workspace_id, brand_id, record_id, field_key, old_value, new_value, source)
SELECT workspace_id, brand_id, id, 'source', '"signup"'::jsonb, '"inbound"'::jsonb, 'first_party' FROM fixed;

-- People already here get their category; the triggers above work it out
-- and write it to each person's history.
UPDATE records r SET attributes = r.attributes
WHERE r.record_type = 'person' AND r.merged_into IS NULL AND NOT r.attributes ? 'source_category'
  AND outflare_source_category(r.attributes ->> 'source') IS NOT NULL;
