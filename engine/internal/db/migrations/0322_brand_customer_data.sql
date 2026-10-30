-- Brands own their data, step 2: every row of
-- customer data belongs to exactly one brand. People, companies and other
-- records, their identities, links, history, merges and consent, payment
-- subscriptions, list members, imports and the catalog each carry brand_id,
-- set on the row itself, so a brand's data is exactly WHERE brand_id = $1.
--
-- Every row that was "shared by every brand" goes to its workspace's first
-- brand.
-- Expand only (docs/migrations.md): columns, indexes, triggers and
-- backfills. Nothing is dropped and nothing becomes NOT NULL yet; the
-- per-workspace unique indexes go in the contract step, once the code
-- reads per brand.

-- The brand a workspace's unbranded rows go to: its oldest active brand, or
-- its oldest brand of any status. Null only for a workspace with no brand,
-- which sign-up (and 0315) no longer leaves.
CREATE FUNCTION outflare_first_brand(ws uuid) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT id FROM brands WHERE workspace_id = ws
    ORDER BY status = 'active' DESC, created_at, id
    LIMIT 1
$$;

-- The brand of the row a column points at: a record, a list or a connection.
CREATE FUNCTION outflare_brand_via(col text, ref uuid) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT CASE
        WHEN ref IS NULL THEN NULL
        WHEN col = 'list_id' THEN (SELECT brand_id FROM smart_lists WHERE id = ref)
        WHEN col = 'integration_id' THEN (SELECT brand_id FROM integrations WHERE id = ref)
        ELSE (SELECT brand_id FROM records WHERE id = ref)
    END
$$;

-- Fills brand_id as a row is written. The trigger's first argument is the
-- mode, the rest are columns to take the brand from, in order:
--   follow:  the row belongs to the record in the first column and always
--            takes its brand, so a record's identities, history and consent
--            can never disagree with it. The other columns, then the first
--            brand, fill it only when that record has none.
--   default: a brand the writer set is kept; otherwise the first column
--            whose row has a brand gives it, else the workspace's first.
CREATE FUNCTION outflare_brand_fill() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    follow boolean := TG_ARGV[0] = 'follow';
    col text;
    ref uuid;
    b uuid;
BEGIN
    FOR i IN 1 .. TG_NARGS - 1 LOOP
        EXIT WHEN NEW.brand_id IS NOT NULL AND NOT (follow AND i = 1);
        col := TG_ARGV[i];
        -- One branch per column: plpgsql only resolves the one it runs.
        IF col = 'record_id' THEN ref := NEW.record_id;
        ELSIF col = 'from_id' THEN ref := NEW.from_id;
        ELSIF col = 'into_id' THEN ref := NEW.into_id;
        ELSIF col = 'record_a' THEN ref := NEW.record_a;
        ELSIF col = 'list_id' THEN ref := NEW.list_id;
        ELSIF col = 'integration_id' THEN ref := NEW.integration_id;
        ELSE RAISE EXCEPTION 'outflare_brand_fill: unknown column %', col;
        END IF;
        b := outflare_brand_via(col, ref);
        IF b IS NOT NULL THEN
            NEW.brand_id := b;
            EXIT;
        END IF;
    END LOOP;
    IF NEW.brand_id IS NULL THEN
        NEW.brand_id := outflare_first_brand(NEW.workspace_id);
    END IF;
    RETURN NEW;
END
$$;

-- ---- Records and product events ----

-- Records already had brand_id, null meaning shared by every brand. Shared
-- records go to the first brand. New ones written without a brand (an email
-- seen in a reply, a CRM import, a payment) land there too, but the code
-- that writes them says so, not a trigger: the version still serving (and
-- one rolled back to) keeps writing shared records, as its tests expect.
UPDATE records SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE customer_events e SET brand_id = COALESCE((SELECT r.brand_id FROM records r WHERE r.id = e.record_id), outflare_first_brand(e.workspace_id))
WHERE e.brand_id IS NULL;

CREATE INDEX records_brand_list_idx ON records (brand_id, record_type, created_at DESC, id DESC);
CREATE INDEX customer_events_brand_recent_idx ON customer_events (brand_id, occurred_at DESC, id DESC);

-- ---- What hangs off a record: it takes the record's brand ----

ALTER TABLE record_identities ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE record_relationships ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE attribute_changes ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE record_merges ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE record_merge_candidates ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE consents ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE payment_subscriptions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;

UPDATE record_identities x SET brand_id = r.brand_id FROM records r WHERE r.id = x.record_id;
UPDATE record_relationships x SET brand_id = r.brand_id FROM records r WHERE r.id = x.from_id;
UPDATE attribute_changes x SET brand_id = r.brand_id FROM records r WHERE r.id = x.record_id;
UPDATE record_merges x SET brand_id = r.brand_id FROM records r WHERE r.id = x.into_id;
UPDATE record_merge_candidates x SET brand_id = r.brand_id FROM records r WHERE r.id = x.record_a;
-- Consent can be about an anonymous browser, with no record.
UPDATE consents x SET brand_id = COALESCE((SELECT r.brand_id FROM records r WHERE r.id = x.record_id), outflare_first_brand(x.workspace_id));
-- A subscription whose customer isn't a record yet belongs to the brand of
-- the payments connection it came from.
UPDATE payment_subscriptions x SET brand_id = COALESCE((SELECT r.brand_id FROM records r WHERE r.id = x.record_id),
    (SELECT i.brand_id FROM integrations i WHERE i.id = x.integration_id), outflare_first_brand(x.workspace_id));

CREATE TRIGGER record_identities_brand BEFORE INSERT OR UPDATE OF record_id ON record_identities
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'record_id');
CREATE TRIGGER record_relationships_brand BEFORE INSERT OR UPDATE OF from_id ON record_relationships
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'from_id');
CREATE TRIGGER attribute_changes_brand BEFORE INSERT OR UPDATE OF record_id ON attribute_changes
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'record_id');
CREATE TRIGGER record_merges_brand BEFORE INSERT OR UPDATE OF into_id ON record_merges
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'into_id');
CREATE TRIGGER record_merge_candidates_brand BEFORE INSERT OR UPDATE OF record_a ON record_merge_candidates
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'record_a');
CREATE TRIGGER consents_brand BEFORE INSERT OR UPDATE OF record_id ON consents
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'record_id');
CREATE TRIGGER payment_subscriptions_brand BEFORE INSERT OR UPDATE OF record_id ON payment_subscriptions
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('follow', 'record_id', 'integration_id');

-- A record moved to another brand takes what hangs off it along.
CREATE FUNCTION outflare_record_brand_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    UPDATE record_identities SET brand_id = NEW.brand_id WHERE record_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    UPDATE record_relationships SET brand_id = NEW.brand_id WHERE from_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    UPDATE attribute_changes SET brand_id = NEW.brand_id WHERE record_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    UPDATE record_merges SET brand_id = NEW.brand_id WHERE into_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    UPDATE record_merge_candidates SET brand_id = NEW.brand_id
    WHERE workspace_id = NEW.workspace_id AND record_a = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    UPDATE consents SET brand_id = NEW.brand_id WHERE record_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    UPDATE payment_subscriptions SET brand_id = NEW.brand_id WHERE record_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    RETURN NULL;
END
$$;

CREATE TRIGGER records_brand_moved AFTER UPDATE OF brand_id ON records
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_record_brand_moved();

-- Decision 1: the same email in two brands is two people. This is the
-- per-brand twin of record_identities_owner_idx; the per-workspace one goes
-- in the contract step.
CREATE UNIQUE INDEX record_identities_brand_owner_idx ON record_identities (brand_id, namespace, value)
    WHERE valid_to IS NULL AND verified;
CREATE INDEX record_identities_brand_lookup_idx ON record_identities (brand_id, namespace, value) WHERE valid_to IS NULL;
CREATE INDEX record_relationships_brand_idx ON record_relationships (brand_id);
CREATE INDEX record_relationships_from_idx ON record_relationships (from_id);
CREATE INDEX attribute_changes_brand_idx ON attribute_changes (brand_id, changed_at DESC);
CREATE INDEX record_merges_brand_idx ON record_merges (brand_id, merged_at DESC);
CREATE INDEX record_merge_candidates_brand_idx ON record_merge_candidates (brand_id, status, created_at DESC, id DESC);
CREATE INDEX consents_brand_idx ON consents (brand_id, occurred_at DESC);
CREATE INDEX payment_subscriptions_brand_idx ON payment_subscriptions (brand_id);

-- ---- Lists: a list belongs to one brand, and so do its members ----

-- A list with no brand held people of every brand; it now holds the first
-- brand's.
-- New lists get their brand from the code that makes them, as records do.
UPDATE smart_lists SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

ALTER TABLE smart_list_members ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE static_list_members ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE smart_list_members x SET brand_id = COALESCE((SELECT l.brand_id FROM smart_lists l WHERE l.id = x.list_id),
    (SELECT r.brand_id FROM records r WHERE r.id = x.record_id), outflare_first_brand(x.workspace_id));
UPDATE static_list_members x SET brand_id = COALESCE((SELECT l.brand_id FROM smart_lists l WHERE l.id = x.list_id),
    (SELECT r.brand_id FROM records r WHERE r.id = x.record_id), outflare_first_brand(x.workspace_id));
CREATE TRIGGER smart_list_members_brand BEFORE INSERT ON smart_list_members
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default', 'list_id', 'record_id');
CREATE TRIGGER static_list_members_brand BEFORE INSERT ON static_list_members
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default', 'list_id', 'record_id');
CREATE INDEX smart_list_members_brand_idx ON smart_list_members (brand_id);
CREATE INDEX static_list_members_brand_idx ON static_list_members (brand_id);

-- ---- Payments syncs and imports: the brand of their connection ----

ALTER TABLE payment_syncs ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE import_runs ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE import_items ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE crm_imports ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE payment_syncs x SET brand_id = COALESCE((SELECT i.brand_id FROM integrations i WHERE i.id = x.integration_id), outflare_first_brand(x.workspace_id));
UPDATE import_runs x SET brand_id = COALESCE((SELECT i.brand_id FROM integrations i WHERE i.id = x.integration_id), outflare_first_brand(x.workspace_id));
UPDATE import_items x SET brand_id = COALESCE((SELECT i.brand_id FROM integrations i WHERE i.id = x.integration_id), outflare_first_brand(x.workspace_id));
UPDATE crm_imports x SET brand_id = COALESCE((SELECT i.brand_id FROM integrations i WHERE i.id = x.integration_id), outflare_first_brand(x.workspace_id));
CREATE TRIGGER payment_syncs_brand BEFORE INSERT ON payment_syncs
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default', 'integration_id');
CREATE TRIGGER import_runs_brand BEFORE INSERT ON import_runs
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default', 'integration_id');
CREATE TRIGGER import_items_brand BEFORE INSERT ON import_items
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default', 'integration_id');
CREATE TRIGGER crm_imports_brand BEFORE INSERT ON crm_imports
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default', 'integration_id');
CREATE INDEX payment_syncs_brand_idx ON payment_syncs (brand_id);
CREATE INDEX import_runs_brand_idx ON import_runs (brand_id, created_at DESC);
CREATE INDEX import_items_brand_idx ON import_items (brand_id);
CREATE INDEX crm_imports_brand_idx ON crm_imports (brand_id, created_at DESC);

-- ---- The catalog is per brand ----

-- What a workspace added to the built-in catalog goes to its first brand.
-- The unique indexes are the per-brand twins of the per-workspace ones,
-- which the contract step drops so two brands can each have a field with
-- the same key.
ALTER TABLE record_types ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE field_definitions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE option_sets ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE event_definitions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE record_types SET brand_id = outflare_first_brand(workspace_id);
UPDATE field_definitions SET brand_id = outflare_first_brand(workspace_id);
UPDATE option_sets SET brand_id = outflare_first_brand(workspace_id);
UPDATE event_definitions SET brand_id = outflare_first_brand(workspace_id);
CREATE TRIGGER record_types_brand BEFORE INSERT ON record_types
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default');
CREATE TRIGGER field_definitions_brand BEFORE INSERT ON field_definitions
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default');
CREATE TRIGGER option_sets_brand BEFORE INSERT ON option_sets
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default');
CREATE TRIGGER event_definitions_brand BEFORE INSERT ON event_definitions
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default');
CREATE UNIQUE INDEX record_types_brand_key_idx ON record_types (brand_id, key);
CREATE UNIQUE INDEX option_sets_brand_key_idx ON option_sets (brand_id, key);
CREATE UNIQUE INDEX field_definitions_brand_key_idx ON field_definitions (brand_id, record_type, key);
CREATE UNIQUE INDEX event_definitions_brand_name_idx ON event_definitions (brand_id, name);

-- New workspaces get the Deal type in their first brand. createWorkspace
-- now makes the brand first; called before a workspace has one, the rows
-- keep a null brand and the triggers above leave it so.
CREATE OR REPLACE FUNCTION outflare_deal_type(ws uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    stage_set text;
    brand uuid := outflare_first_brand(ws);
BEGIN
    INSERT INTO record_types (workspace_id, brand_id, key, name, plural, description)
    VALUES (ws, brand, 'deal', 'Deal', 'Deals',
        'A sales opportunity: who it is with, its stage and its value. Outreach outcomes and reports count deals from here.')
    ON CONFLICT (workspace_id, key) DO NOTHING;
    INSERT INTO option_sets (workspace_id, brand_id, key, name, options)
    VALUES (ws, brand, 'deal_stage', 'Deal stage', '[{"key": "lead", "label": "Lead"}, {"key": "qualified", "label": "Qualified"},
        {"key": "meeting", "label": "Meeting booked"}, {"key": "proposal", "label": "Proposal"},
        {"key": "negotiation", "label": "Negotiation"}, {"key": "won", "label": "Won"}, {"key": "lost", "label": "Lost"}]')
    ON CONFLICT (workspace_id, key) DO NOTHING;
    INSERT INTO field_definitions (workspace_id, brand_id, record_type, key, label, description, value_type, option_set, multiple, target_type, filterable)
    VALUES
        (ws, brand, 'deal', 'stage', 'Stage', 'Won and lost close the deal.', 'status', 'deal_stage', false, NULL, true),
        (ws, brand, 'deal', 'value', 'Value', '', 'currency', NULL, false, NULL, true),
        (ws, brand, 'deal', 'close_date', 'Close date', '', 'date', NULL, false, NULL, true),
        (ws, brand, 'deal', 'associated_people', 'People', 'The people the deal is with.', 'record_reference', NULL, true, 'person', true),
        (ws, brand, 'deal', 'associated_company', 'Company', 'The company the deal is with.', 'record_reference', NULL, false, 'account', true)
    ON CONFLICT (workspace_id, record_type, key) DO NOTHING;
    SELECT option_set INTO stage_set FROM field_definitions
    WHERE workspace_id = ws AND record_type = 'deal' AND key = 'stage' AND value_type = 'status';
    IF stage_set IS NOT NULL THEN
        UPDATE option_sets SET options = options || '[{"key": "won", "label": "Won"}]', updated_at = now()
        WHERE workspace_id = ws AND key = stage_set AND NOT options @> '[{"key": "won"}]';
        UPDATE option_sets SET options = options || '[{"key": "lost", "label": "Lost"}]', updated_at = now()
        WHERE workspace_id = ws AND key = stage_set AND NOT options @> '[{"key": "lost"}]';
    END IF;
END
$$;
