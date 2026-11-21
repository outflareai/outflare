-- One People list: every person carries a Source, where they came from
-- (the catalog's built-in source field: cold, inbound, event, signup,
-- invited, customer, other). It is set once, when the person is added, and
-- nothing that happens later changes it: a cold contact who becomes a
-- customer keeps Source Cold, and progress lives on deals. A person can
-- still correct it by hand (another value, with its history), but nobody
-- can empty it.
--
-- When whoever adds a person doesn't say, Outflare works it out from how
-- they arrived: a third-party prospect is Cold, someone who came through
-- the brand's own site or product (first_party) is Sign-up. Imports and
-- people added by hand ask instead. A brand that already made its own
-- field called source keeps it, and nothing here touches it.

CREATE OR REPLACE FUNCTION outflare_person_source() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    guess text;
BEGIN
    IF NEW.record_type <> 'person' OR EXISTS (SELECT 1 FROM field_definitions f WHERE f.workspace_id = NEW.workspace_id
            AND f.record_type = 'person' AND f.key = 'source' AND f.brand_id IS NOT DISTINCT FROM NEW.brand_id) THEN
        RETURN NEW;
    END IF;
    NEW.attributes := COALESCE(NEW.attributes, '{}'::jsonb);
    NEW.attribute_sources := COALESCE(NEW.attribute_sources, '{}'::jsonb);
    IF NEW.attributes ? 'source' THEN
        RETURN NEW;
    END IF;
    -- Set once: a write that leaves it out (most writes rebuild the whole
    -- attributes object) keeps the value it had.
    IF TG_OP = 'UPDATE' AND OLD.attributes ? 'source' THEN
        NEW.attributes := NEW.attributes || jsonb_build_object('source', OLD.attributes -> 'source');
        NEW.attribute_sources := NEW.attribute_sources || jsonb_build_object('source', COALESCE(OLD.attribute_sources -> 'source', '"first_party"'::jsonb));
        RETURN NEW;
    END IF;
    IF TG_OP = 'INSERT' OR OLD.origin IS DISTINCT FROM NEW.origin THEN
        guess := CASE NEW.origin WHEN 'prospect' THEN 'cold' WHEN 'first_party' THEN 'signup' END;
    END IF;
    IF guess IS NOT NULL THEN
        NEW.attributes := NEW.attributes || jsonb_build_object('source', guess);
        NEW.attribute_sources := NEW.attribute_sources || '{"source": "first_party"}'::jsonb;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER records_person_source BEFORE INSERT OR UPDATE OF attributes, origin ON records
    FOR EACH ROW EXECUTE FUNCTION outflare_person_source();

-- The guess goes in the person's history like any other change.
CREATE OR REPLACE FUNCTION outflare_person_source_history() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.record_type = 'person' AND NEW.attribute_sources ->> 'source' = 'first_party'
        AND NEW.attributes ? 'source'
        AND (TG_OP = 'INSERT' OR NOT (OLD.attributes ? 'source')) THEN
        INSERT INTO attribute_changes (workspace_id, brand_id, record_id, field_key, old_value, new_value, source)
        VALUES (NEW.workspace_id, NEW.brand_id, NEW.id, 'source', NULL, NEW.attributes -> 'source', 'first_party');
    END IF;
    RETURN NULL;
END $$;

CREATE TRIGGER records_person_source_history AFTER INSERT OR UPDATE OF attributes, origin ON records
    FOR EACH ROW EXECUTE FUNCTION outflare_person_source_history();

-- People already here: prospects and anyone in cold outreach (a lead) are
-- Cold, people from the brand's own site or product are Sign-up. Everyone
-- else (imports, CRM syncs, people added by hand) is left empty for a
-- person to set, because nothing says where they came from. Each guess is
-- written to the person's history.
UPDATE records r SET
    attributes = r.attributes || jsonb_build_object('source', g.source),
    attribute_sources = r.attribute_sources || '{"source": "first_party"}'::jsonb,
    version = r.version + 1
FROM (
    SELECT p.id,
        CASE WHEN p.origin = 'prospect' OR EXISTS (SELECT 1 FROM leads l WHERE l.record_id = p.id) THEN 'cold'
             WHEN p.origin = 'first_party' THEN 'signup' END AS source
    FROM records p
    WHERE p.record_type = 'person' AND p.merged_into IS NULL AND NOT p.attributes ? 'source'
      AND NOT EXISTS (SELECT 1 FROM field_definitions f WHERE f.workspace_id = p.workspace_id AND f.record_type = 'person'
          AND f.key = 'source' AND f.brand_id IS NOT DISTINCT FROM p.brand_id)
) g
WHERE r.id = g.id AND g.source IS NOT NULL;

-- A cold contact is a prospect for who may be emailed, however they got
-- into the workspace (an import that answered "Cold", say): Marketing
-- email starts at no until they opt in. Every check of a person's contact
-- basis reads their origin through this.
CREATE OR REPLACE FUNCTION outflare_contact_origin(origin text, attributes jsonb) RETURNS text
    LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
    SELECT CASE WHEN attributes ->> 'source' = 'cold' THEN 'prospect' ELSE origin END
$$;
