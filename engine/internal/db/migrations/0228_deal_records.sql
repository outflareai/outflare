-- Every deal is one CRM record. A deal logged by hand, through the API or
-- synced from HubSpot is a record of the workspace's Deal type (key deal)
-- with the same id, linked to its person and company, so the CRM, the
-- timeline, search and outreach outcomes all see one deal. The deals row
-- keeps what only outreach knows: the sequence, project, mission, lead and
-- meeting it came from, and where it was synced from.
--
-- Two triggers keep the pair in step, whichever side is written: a deal
-- written through the outcomes API or a sync updates its record, and a
-- deal record made or changed in the CRM (by hand, a reply rule, an
-- import) updates its deals row, or adds one. Archiving a deal record takes
-- it out of outcomes; deleting a deal archives its record.
--
-- Deal records use Attio's field keys, so a deal type brought over from
-- Attio is already the right shape: stage (a status field), value
-- (currency), close_date, associated_people and associated_company.

ALTER TABLE deals DROP CONSTRAINT IF EXISTS deals_source_check;
ALTER TABLE deals ADD CONSTRAINT deals_source_check CHECK (source IN ('hubspot', 'manual', 'api', 'crm'));

-- The workspace's Deal type, with its fields and stages, added where
-- missing. A workspace's own deal type and fields are kept as they are;
-- its stages get won and lost if they lack them, since those close a deal.
CREATE FUNCTION outflare_deal_type(ws uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    stage_set text;
BEGIN
    INSERT INTO record_types (workspace_id, key, name, plural, description)
    VALUES (ws, 'deal', 'Deal', 'Deals',
        'A sales opportunity: who it is with, its stage and its value. Outreach outcomes and reports count deals from here.')
    ON CONFLICT (workspace_id, key) DO NOTHING;
    INSERT INTO option_sets (workspace_id, key, name, options)
    VALUES (ws, 'deal_stage', 'Deal stage', '[{"key": "lead", "label": "Lead"}, {"key": "qualified", "label": "Qualified"},
        {"key": "meeting", "label": "Meeting booked"}, {"key": "proposal", "label": "Proposal"},
        {"key": "negotiation", "label": "Negotiation"}, {"key": "won", "label": "Won"}, {"key": "lost", "label": "Lost"}]')
    ON CONFLICT (workspace_id, key) DO NOTHING;
    INSERT INTO field_definitions (workspace_id, record_type, key, label, description, value_type, option_set, multiple, target_type, filterable)
    VALUES
        (ws, 'deal', 'stage', 'Stage', 'Won and lost close the deal.', 'status', 'deal_stage', false, NULL, true),
        (ws, 'deal', 'value', 'Value', '', 'currency', NULL, false, NULL, true),
        (ws, 'deal', 'close_date', 'Close date', '', 'date', NULL, false, NULL, true),
        (ws, 'deal', 'associated_people', 'People', 'The people the deal is with.', 'record_reference', NULL, true, 'person', true),
        (ws, 'deal', 'associated_company', 'Company', 'The company the deal is with.', 'record_reference', NULL, false, 'account', true)
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

-- A deal field's definition, when it has the value type deals write.
CREATE FUNCTION outflare_deal_field(ws uuid, field text, kind text) RETURNS field_definitions
LANGUAGE sql STABLE AS $$
    SELECT * FROM field_definitions
    WHERE workspace_id = ws AND record_type = 'deal' AND key = field AND value_type = kind AND archived_at IS NULL
$$;

-- A deals row written by the outcomes API, a sync or an older version:
-- its record is made or brought up to date.
CREATE FUNCTION outflare_deal_to_record() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    src text := CASE WHEN NEW.source = 'hubspot' THEN 'crm_sync' ELSE 'manual' END;
    rec records%ROWTYPE;
    f field_definitions%ROWTYPE;
    opts jsonb;
    stage_key text;
    want jsonb := '{}';
    k text;
    v jsonb;
    company uuid;
    people jsonb;
BEGIN
    -- A change the other trigger made: the record is already up to date.
    IF pg_trigger_depth() > 1 THEN
        RETURN NULL;
    END IF;
    IF TG_OP = 'DELETE' THEN
        UPDATE records SET archived_at = now(), version = version + 1, updated_at = now()
        WHERE id = OLD.id AND record_type = 'deal' AND archived_at IS NULL;
        RETURN NULL;
    END IF;
    PERFORM outflare_deal_type(NEW.workspace_id);
    SELECT * INTO rec FROM records WHERE id = NEW.id;
    IF NOT FOUND THEN
        INSERT INTO records (id, workspace_id, record_type, brand_id, name, origin, acquired_via, created_by, created_at, updated_at)
        SELECT NEW.id, NEW.workspace_id, 'deal', COALESCE(s.brand_id, p.brand_id), NEW.name,
            CASE WHEN src = 'crm_sync' THEN 'crm_sync' ELSE 'manual' END, NEW.source, NEW.created_by, NEW.created_at, now()
        FROM (SELECT 1) one
        LEFT JOIN sequences s ON s.id = NEW.sequence_id
        LEFT JOIN projects p ON p.id = NEW.project_id
        RETURNING * INTO rec;
        INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
        VALUES (NEW.workspace_id, NEW.id, 'name', NULL, to_jsonb(NEW.name), src);
    ELSIF rec.record_type <> 'deal' OR rec.merged_into IS NOT NULL THEN
        RETURN NULL;
    END IF;

    -- Stage: won and lost follow the deal's state; an open deal takes the
    -- stage whose key or label matches, keeps the open stage it has, or
    -- else starts at the first one.
    SELECT * INTO f FROM outflare_deal_field(NEW.workspace_id, 'stage', 'status');
    IF f.id IS NOT NULL THEN
        SELECT options INTO opts FROM option_sets WHERE workspace_id = NEW.workspace_id AND key = f.option_set;
        opts := (SELECT COALESCE(jsonb_agg(o), '[]') FROM jsonb_array_elements(COALESCE(opts, '[]')) o
            WHERE NOT COALESCE((o ->> 'archived')::boolean, false));
        IF NEW.state <> 'open' THEN
            stage_key := NEW.state;
        ELSE
            SELECT o ->> 'key' INTO stage_key FROM jsonb_array_elements(opts) o
            WHERE o ->> 'key' NOT IN ('won', 'lost')
              AND (lower(o ->> 'label') = lower(trim(NEW.stage))
                OR o ->> 'key' = trim(both '_' from regexp_replace(lower(NEW.stage), '[^a-z0-9]+', '_', 'g')))
            LIMIT 1;
            IF stage_key IS NULL AND rec.attributes ->> 'stage' NOT IN ('won', 'lost') THEN
                stage_key := rec.attributes ->> 'stage';
            END IF;
            IF stage_key IS NULL THEN
                SELECT o ->> 'key' INTO stage_key FROM jsonb_array_elements(opts) o WHERE o ->> 'key' NOT IN ('won', 'lost') LIMIT 1;
            END IF;
        END IF;
        IF stage_key IS NOT NULL AND opts @> jsonb_build_array(jsonb_build_object('key', stage_key)) THEN
            want := want || jsonb_build_object('stage', stage_key);
        END IF;
    END IF;
    IF NEW.amount_cents IS NOT NULL AND (outflare_deal_field(NEW.workspace_id, 'value', 'currency')).id IS NOT NULL THEN
        want := want || jsonb_build_object('value', jsonb_build_object('amount_minor', NEW.amount_cents, 'currency', NEW.currency));
    END IF;
    IF NEW.closed_at IS NOT NULL AND (outflare_deal_field(NEW.workspace_id, 'close_date', 'date')).id IS NOT NULL THEN
        want := want || jsonb_build_object('close_date', to_char(NEW.closed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD'));
    END IF;
    -- The deal's person joins its people, and their company becomes its
    -- company when it has none.
    IF NEW.record_id IS NOT NULL THEN
        SELECT * INTO f FROM outflare_deal_field(NEW.workspace_id, 'associated_people', 'record_reference');
        IF f.id IS NOT NULL AND f.target_type = 'person' THEN
            people := rec.attributes -> 'associated_people';
            IF f.multiple THEN
                IF jsonb_typeof(people) IS DISTINCT FROM 'array' THEN
                    people := '[]';
                END IF;
                IF NOT people @> jsonb_build_array(NEW.record_id::text) THEN
                    want := want || jsonb_build_object('associated_people', people || jsonb_build_array(NEW.record_id::text));
                END IF;
            ELSIF people IS NULL THEN
                want := want || jsonb_build_object('associated_people', NEW.record_id::text);
            END IF;
        END IF;
        SELECT * INTO f FROM outflare_deal_field(NEW.workspace_id, 'associated_company', 'record_reference');
        IF f.id IS NOT NULL AND f.target_type = 'account' AND NOT f.multiple AND rec.attributes -> 'associated_company' IS NULL THEN
            SELECT c.id INTO company FROM records pr
            JOIN records c ON c.id::text = pr.attributes -> 'companies' ->> 0 AND c.workspace_id = pr.workspace_id
            WHERE pr.id = NEW.record_id AND c.record_type = 'account' AND c.merged_into IS NULL;
            IF company IS NOT NULL THEN
                want := want || jsonb_build_object('associated_company', company::text);
            END IF;
        END IF;
    END IF;

    -- Write what changed. A synced deal's CRM is its source of truth, as
    -- the outcomes API says: a change made here lasts until the next sync
    -- of that deal.
    FOR k, v IN SELECT * FROM jsonb_each(want) LOOP
        IF rec.attributes -> k = v THEN
            CONTINUE;
        END IF;
        INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
        VALUES (NEW.workspace_id, NEW.id, k, rec.attributes -> k, v, src);
        IF k IN ('associated_people', 'associated_company') THEN
            INSERT INTO record_relationships (workspace_id, from_id, to_id, kind, field_key)
            SELECT NEW.workspace_id, NEW.id, x::uuid, k, k
            FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(v) = 'array' THEN v ELSE jsonb_build_array(v) END) x
            ON CONFLICT DO NOTHING;
        END IF;
        rec.attributes := rec.attributes || jsonb_build_object(k, v);
        rec.attribute_sources := rec.attribute_sources || jsonb_build_object(k, src);
        rec.version := 0; -- marks a change
    END LOOP;
    IF rec.name IS DISTINCT FROM NEW.name AND NEW.name <> '' THEN
        INSERT INTO attribute_changes (workspace_id, record_id, field_key, old_value, new_value, source)
        VALUES (NEW.workspace_id, NEW.id, 'name', to_jsonb(rec.name), to_jsonb(NEW.name), src);
        rec.name := NEW.name;
        rec.version := 0;
    END IF;
    IF rec.version = 0 THEN
        UPDATE records SET name = rec.name, attributes = rec.attributes, attribute_sources = rec.attribute_sources,
            version = version + 1, updated_at = now()
        WHERE id = NEW.id;
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER deals_record AFTER INSERT OR UPDATE OR DELETE ON deals
    FOR EACH ROW EXECUTE FUNCTION outflare_deal_to_record();

-- A deal record made or changed in the CRM: its deals row follows, so
-- outcomes count it. A deal synced from HubSpot keeps HubSpot's name,
-- stage and value there. A new deal is attributed to outreach by the
-- deals.attribute job.
CREATE FUNCTION outflare_record_to_deal() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    d deals%ROWTYPE;
    stage_key text := NEW.attributes ->> 'stage';
    stage_label text;
    deal_state text;
    amount bigint;
    cur text := 'USD';
    closed timestamptz;
    person uuid;
BEGIN
    IF pg_trigger_depth() > 1 THEN
        RETURN NULL;
    END IF;
    SELECT * INTO d FROM deals WHERE id = NEW.id;
    IF NEW.archived_at IS NOT NULL OR NEW.merged_into IS NOT NULL THEN
        IF FOUND AND d.source <> 'hubspot' THEN
            DELETE FROM deals WHERE id = NEW.id;
        END IF;
        RETURN NULL;
    END IF;
    deal_state := CASE WHEN stage_key IN ('won', 'closed_won') THEN 'won' WHEN stage_key IN ('lost', 'closed_lost') THEN 'lost' ELSE 'open' END;
    IF stage_key IS NOT NULL THEN
        SELECT o ->> 'label' INTO stage_label
        FROM field_definitions fd JOIN option_sets s ON s.workspace_id = fd.workspace_id AND s.key = fd.option_set,
            jsonb_array_elements(s.options) o
        WHERE fd.workspace_id = NEW.workspace_id AND fd.record_type = 'deal' AND fd.key = 'stage' AND o ->> 'key' = stage_key;
    END IF;
    IF jsonb_typeof(NEW.attributes -> 'value') = 'object' THEN
        amount := GREATEST(0, (NEW.attributes -> 'value' ->> 'amount_minor')::numeric)::bigint;
        IF NEW.attributes -> 'value' ->> 'currency' ~ '^[A-Z]{3}$' THEN
            cur := NEW.attributes -> 'value' ->> 'currency';
        END IF;
    END IF;
    IF deal_state <> 'open' THEN
        closed := COALESCE(CASE WHEN NEW.attributes ->> 'close_date' ~ '^\d{4}-\d{2}-\d{2}$'
            THEN (NEW.attributes ->> 'close_date')::date::timestamptz END, d.closed_at, now());
    END IF;
    SELECT r.id INTO person FROM records r
    WHERE r.workspace_id = NEW.workspace_id AND r.record_type = 'person' AND r.id::text = COALESCE(
        CASE WHEN jsonb_typeof(NEW.attributes -> 'associated_people') = 'array'
            THEN NEW.attributes -> 'associated_people' ->> 0 ELSE NEW.attributes ->> 'associated_people' END, '');
    IF d.id IS NULL THEN
        INSERT INTO deals (id, workspace_id, source, name, stage, state, amount_cents, currency, opened_at, closed_at, record_id,
            created_by, created_at)
        VALUES (NEW.id, NEW.workspace_id, 'crm', left(NEW.name, 300), COALESCE(stage_label, stage_key, ''), deal_state, amount, cur,
            NEW.created_at, closed, person, NEW.created_by, NEW.created_at);
        IF person IS NOT NULL THEN
            INSERT INTO jobs (workspace_id, kind, payload, max_attempts, idempotency_key)
            VALUES (NEW.workspace_id, 'deals.attribute', jsonb_build_object('deal_id', NEW.id), 5, 'deals.attribute:' || NEW.id || ':' || person)
            ON CONFLICT DO NOTHING;
        END IF;
    ELSIF d.source <> 'hubspot' THEN
        UPDATE deals SET name = left(NEW.name, 300), stage = COALESCE(stage_label, stage_key, stage), state = deal_state,
            amount_cents = amount, currency = cur, closed_at = closed, record_id = COALESCE(person, record_id), updated_at = now()
        WHERE id = NEW.id AND (name, stage, state, amount_cents, currency, closed_at, record_id) IS DISTINCT FROM
            (left(NEW.name, 300), COALESCE(stage_label, stage_key, stage), deal_state, amount, cur, closed, COALESCE(person, record_id));
        IF person IS NOT NULL AND d.record_id IS NULL THEN
            INSERT INTO jobs (workspace_id, kind, payload, max_attempts, idempotency_key)
            VALUES (NEW.workspace_id, 'deals.attribute', jsonb_build_object('deal_id', NEW.id), 5, 'deals.attribute:' || NEW.id || ':' || person)
            ON CONFLICT DO NOTHING;
        END IF;
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER records_deal AFTER INSERT OR UPDATE ON records
    FOR EACH ROW WHEN (NEW.record_type = 'deal') EXECUTE FUNCTION outflare_record_to_deal();

-- New workspaces get the Deal type when they are created (createWorkspace
-- calls outflare_deal_type), and any workspace gets it with its first deal.
