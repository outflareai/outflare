-- Removing projects, step 3 (ADR 12): drop what is left of them. Step 2
-- (0270) detached every record and stopped the code reading projects.
-- migration-safety: nothing reads projects or any project_id column since step 2 of ADR 12 (#451, released in v0.33.0 and running on app.outflare.ai)

-- Deals take their record's brand from their sequence only.
CREATE OR REPLACE FUNCTION outflare_deal_to_record() RETURNS trigger
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
        SELECT NEW.id, NEW.workspace_id, 'deal', s.brand_id, NEW.name,
            CASE WHEN src = 'crm_sync' THEN 'crm_sync' ELSE 'manual' END, NEW.source, NEW.created_by, NEW.created_at, now()
        FROM (SELECT 1) one
        LEFT JOIN sequences s ON s.id = NEW.sequence_id
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


-- Records no longer take a brand from a project.
DROP TRIGGER sending_domains_brand ON sending_domains;
DROP TRIGGER mailboxes_brand ON mailboxes;
DROP TRIGGER lead_lists_brand ON lead_lists;
DROP TRIGGER sequences_brand ON sequences;
DROP TRIGGER missions_brand ON missions;
DROP TRIGGER memories_brand ON memories;
DROP FUNCTION brand_from_project();

-- Query views keep their project_id column, always null, so saved queries
-- that name it still run.
CREATE OR REPLACE VIEW query.sequences WITH (security_barrier) AS
SELECT q.id, q.brand_id, NULL::uuid AS project_id, q.mission_id, q.name, q.status, q.daily_new_limit, q.stop_on_reply, q.created_at, q.updated_at
FROM sequences q JOIN query_scopes s ON s.pid = pg_backend_pid() AND q.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

CREATE OR REPLACE VIEW query.deals WITH (security_barrier) AS
SELECT d.id, d.record_id, NULL::uuid AS project_id, d.mission_id, d.sequence_id, d.lead_id, d.meeting_id, d.source, d.name, d.stage, d.state,
    d.amount_cents, d.currency, d.attribution, d.opened_at, d.closed_at, d.created_at, d.updated_at
FROM deals d LEFT JOIN sequences q ON q.id = d.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

CREATE OR REPLACE VIEW query.meetings WITH (security_barrier) AS
SELECT m.id, m.record_id, NULL::uuid AS project_id, m.mission_id, m.sequence_id, m.lead_id, m.source, m.title, m.status, m.qualified, m.attribution,
    m.starts_at, m.ends_at, m.booked_at, m.created_at
FROM meetings m LEFT JOIN sequences q ON q.id = m.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands));

CREATE OR REPLACE VIEW query.spend WITH (security_barrier) AS
SELECT e.id, e.brand_id, NULL::uuid AS project_id, e.sequence_id, e.mission_id, e.channel, e.category, e.provider, e.units, e.unit_name,
    e.amount_cents, e.currency, e.source, e.occurred_at
FROM spend_entries e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands));

DROP VIEW query.projects;

-- Reply rule sets are the workspace's or a sequence's.
CREATE UNIQUE INDEX reply_rules_workspace_sequence_idx
    ON reply_rules (workspace_id, COALESCE(sequence_id, '00000000-0000-0000-0000-000000000000'::uuid));
DROP INDEX reply_rules_scope_idx;
ALTER TABLE reply_rules DROP CONSTRAINT reply_rules_check;
ALTER TABLE api_tokens DROP CONSTRAINT api_tokens_brand_or_project;

-- The columns (their indexes and foreign keys go with them), then projects.
ALTER TABLE sending_domains DROP COLUMN project_id;
ALTER TABLE mailboxes DROP COLUMN project_id;
ALTER TABLE lead_lists DROP COLUMN project_id;
ALTER TABLE sequences DROP COLUMN project_id;
ALTER TABLE api_tokens DROP COLUMN project_id;
ALTER TABLE spend_entries DROP COLUMN project_id;
ALTER TABLE spend_recurring DROP COLUMN project_id;
ALTER TABLE meetings DROP COLUMN project_id;
ALTER TABLE deals DROP COLUMN project_id;
ALTER TABLE list_runs DROP COLUMN project_id;
ALTER TABLE infra_plans DROP COLUMN project_id;
ALTER TABLE missions DROP COLUMN project_id;
ALTER TABLE memories DROP COLUMN project_id;
ALTER TABLE ai_calls DROP COLUMN project_id;
ALTER TABLE reply_rules DROP COLUMN project_id;
ALTER TABLE request_logs DROP COLUMN project_id;

DROP TABLE projects;
DROP FUNCTION project_brand_moved();
