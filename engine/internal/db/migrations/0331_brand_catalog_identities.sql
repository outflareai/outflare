-- Brands own their data, step 2b part 1 (plans/brands/README.md): identity
-- matching and the catalog work per brand.
--
-- Decision 1: the same email in two brands is two people. 0322 added the
-- per-brand twin of record_identities_owner_idx; the per-workspace one
-- still refuses a second brand's verified copy of an address, so it goes
-- now rather than in the contract step. That is safe for the version still
-- serving (and one rolled back to): no statement names it as an ON
-- CONFLICT arbiter (they use (record_id, namespace, value), or no target),
-- and every identity row gets a brand from its record (0322's trigger), so
-- within one brand the per-brand index refuses the same duplicates it did.
-- The older code checks for another owner across the workspace before it
-- writes, so it still never adds a second owner itself.
DROP INDEX record_identities_owner_idx;

-- Decision 3: the catalog is per brand, and every brand starts with the
-- default set. The per-workspace unique constraints of record types, option
-- lists and fields would refuse a second brand's Deal type, so they go
-- too; the per-brand ones from 0322 stay. The version still serving never
-- uses them as an ON CONFLICT arbiter (only outflare_deal_type did, and it
-- is replaced below); it writes its rows to the first brand (0322's
-- triggers), where the per-brand constraints refuse the same duplicates.
--
-- event_definitions keeps its per-workspace constraint: the version still
-- serving upserts event names with ON CONFLICT (workspace_id, name), which
-- needs exactly that constraint. Until the contract step an event name is
-- defined in one brand of a workspace at a time, and the code says so.
ALTER TABLE record_types DROP CONSTRAINT record_types_workspace_id_key_key;
ALTER TABLE option_sets DROP CONSTRAINT option_sets_workspace_id_key_key;
ALTER TABLE field_definitions DROP CONSTRAINT field_definitions_workspace_id_record_type_key_key;
CREATE INDEX record_types_workspace_idx ON record_types (workspace_id, key);
CREATE INDEX option_sets_workspace_idx ON option_sets (workspace_id, key);
CREATE INDEX field_definitions_workspace_idx ON field_definitions (workspace_id, record_type, key);

-- The default catalog a brand starts with, on top of the built-in person
-- and company types and option lists that live in code: the Deal type, its
-- stages and its fields. It only adds what the brand lacks.
CREATE FUNCTION outflare_brand_catalog(brand uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    ws uuid;
    stage_set text;
BEGIN
    SELECT workspace_id INTO ws FROM brands WHERE id = brand;
    IF ws IS NULL THEN
        RETURN;
    END IF;
    INSERT INTO record_types (workspace_id, brand_id, key, name, plural, description)
    SELECT ws, brand, 'deal', 'Deal', 'Deals',
        'A sales opportunity: who it is with, its stage and its value. Outreach outcomes and reports count deals from here.'
    WHERE NOT EXISTS (SELECT 1 FROM record_types WHERE brand_id = brand AND key = 'deal');
    INSERT INTO option_sets (workspace_id, brand_id, key, name, options)
    SELECT ws, brand, 'deal_stage', 'Deal stage', '[{"key": "lead", "label": "Lead"}, {"key": "qualified", "label": "Qualified"},
        {"key": "meeting", "label": "Meeting booked"}, {"key": "proposal", "label": "Proposal"},
        {"key": "negotiation", "label": "Negotiation"}, {"key": "won", "label": "Won"}, {"key": "lost", "label": "Lost"}]'
    WHERE NOT EXISTS (SELECT 1 FROM option_sets WHERE brand_id = brand AND key = 'deal_stage');
    INSERT INTO field_definitions (workspace_id, brand_id, record_type, key, label, description, value_type, option_set, multiple, target_type, filterable)
    SELECT ws, brand, 'deal', f.key, f.label, f.description, f.value_type, f.option_set, f.multiple, f.target_type, true
    FROM (VALUES
        ('stage', 'Stage', 'Won and lost close the deal.', 'status', 'deal_stage', false, NULL),
        ('value', 'Value', '', 'currency', NULL, false, NULL),
        ('close_date', 'Close date', '', 'date', NULL, false, NULL),
        ('associated_people', 'People', 'The people the deal is with.', 'record_reference', NULL, true, 'person'),
        ('associated_company', 'Company', 'The company the deal is with.', 'record_reference', NULL, false, 'account')
    ) AS f (key, label, description, value_type, option_set, multiple, target_type)
    WHERE NOT EXISTS (SELECT 1 FROM field_definitions d WHERE d.brand_id = brand AND d.record_type = 'deal' AND d.key = f.key);
    -- A stage list the brand made itself still gets won and lost, which
    -- close a deal.
    SELECT option_set INTO stage_set FROM field_definitions
    WHERE brand_id = brand AND record_type = 'deal' AND key = 'stage' AND value_type = 'status';
    IF stage_set IS NOT NULL THEN
        UPDATE option_sets SET options = options || '[{"key": "won", "label": "Won"}]', updated_at = now()
        WHERE brand_id = brand AND key = stage_set AND NOT options @> '[{"key": "won"}]';
        UPDATE option_sets SET options = options || '[{"key": "lost", "label": "Lost"}]', updated_at = now()
        WHERE brand_id = brand AND key = stage_set AND NOT options @> '[{"key": "lost"}]';
    END IF;
END
$$;

-- New workspaces (the version still serving calls this) get the default
-- catalog in their first brand.
CREATE OR REPLACE FUNCTION outflare_deal_type(ws uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    brand uuid := outflare_first_brand(ws);
BEGIN
    IF brand IS NOT NULL THEN
        PERFORM outflare_brand_catalog(brand);
    END IF;
END
$$;

-- Every brand made before now gets the default catalog it lacks. Brands
-- are few (one or a handful per workspace), so this is quick.
SELECT outflare_brand_catalog(id) FROM brands;

-- The person an email address belongs to within one brand: its verified
-- owner there, or else the oldest person there who has it. The
-- per-workspace outflare_person_with_email stays for outreach until its
-- step moves to brands.
CREATE FUNCTION outflare_brand_person_with_email(brand uuid, address text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT i.record_id FROM record_identities i JOIN records r ON r.id = i.record_id
    WHERE i.brand_id = brand AND i.namespace = 'email' AND i.value = lower(trim(address)) AND i.valid_to IS NULL
      AND r.merged_into IS NULL AND r.record_type = 'person'
    ORDER BY i.verified DESC, i.valid_from
    LIMIT 1
$$;
