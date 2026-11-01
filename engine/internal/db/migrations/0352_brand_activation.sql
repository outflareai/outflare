-- Activation records belong to a brand.
-- Rules and their firings, journeys with their versions and runs,
-- lifecycle email (templates, topics, broadcasts and the emails they
-- make), alerts and webhook endpoints each belong to one brand, set on the
-- row itself, so a brand's activation data is exactly WHERE brand_id = $1.
--
-- Rows previously shared across brands map to the workspace's first brand.
-- Expand only (docs/migrations.md): columns, indexes, triggers and
-- backfills.
--
-- The parent tables (rules, journeys, email_templates, email_broadcasts,
-- emails, alerts, webhook_endpoints) already had a nullable brand_id that
-- the version still serving reads, where null means every brand. They are
-- backfilled, and the new code sets the brand of every row it writes
-- itself; no trigger fills them, so the older version keeps writing and
-- reading rows for every brand as its tests expect. The child tables are
-- new to brand_id, so nothing older reads it there, and a trigger fills it
-- from the parent.

-- The brand of an activation row: its parent's, else the workspace's first.
CREATE FUNCTION outflare_activation_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    b uuid;
BEGIN
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- One branch per table: plpgsql only resolves the one it runs.
    IF TG_TABLE_NAME IN ('rule_firings', 'rule_states') THEN
        SELECT brand_id INTO b FROM rules WHERE id = NEW.rule_id;
    ELSIF TG_TABLE_NAME = 'journey_runs' THEN
        SELECT brand_id INTO b FROM journeys WHERE id = NEW.journey_id;
    ELSIF TG_TABLE_NAME = 'journey_run_steps' THEN
        SELECT brand_id INTO b FROM journey_runs WHERE id = NEW.run_id;
    ELSIF TG_TABLE_NAME = 'journey_versions' THEN
        SELECT brand_id INTO b FROM journeys WHERE id = NEW.journey_id;
    ELSIF TG_TABLE_NAME = 'journey_step_secrets' THEN
        SELECT brand_id INTO b FROM journeys WHERE id = NEW.journey_id;
    ELSIF TG_TABLE_NAME = 'email_template_versions' THEN
        SELECT brand_id INTO b FROM email_templates WHERE id = NEW.template_id;
    ELSIF TG_TABLE_NAME = 'email_broadcast_recipients' THEN
        SELECT brand_id INTO b FROM email_broadcasts WHERE id = NEW.broadcast_id;
    ELSIF TG_TABLE_NAME = 'email_events' THEN
        SELECT brand_id INTO b FROM emails WHERE id = NEW.email_id;
    ELSIF TG_TABLE_NAME = 'alert_deliveries' THEN
        SELECT brand_id INTO b FROM alerts WHERE id = NEW.alert_id;
    ELSIF TG_TABLE_NAME = 'webhook_deliveries' THEN
        -- An endpoint of one brand gets only its events; one from before
        -- brands (no brand) takes the event's brand.
        SELECT COALESCE(e.brand_id, (NULLIF(NEW.payload ->> 'brand_id', ''))::uuid) INTO b
        FROM webhook_endpoints e WHERE e.id = NEW.endpoint_id;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

-- ---- The parents: what was for every brand goes to the first brand ----

UPDATE rules SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE journeys SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE email_templates SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
-- A broadcast goes with its template.
UPDATE email_broadcasts b SET brand_id = COALESCE((SELECT t.brand_id FROM email_templates t WHERE t.id = b.template_id),
    outflare_first_brand(b.workspace_id)) WHERE b.brand_id IS NULL;
-- An email goes with its broadcast, else its template.
UPDATE emails e SET brand_id = COALESCE((SELECT b.brand_id FROM email_broadcasts b WHERE b.id = e.broadcast_id),
    (SELECT t.brand_id FROM email_templates t WHERE t.id = e.template_id), outflare_first_brand(e.workspace_id))
WHERE e.brand_id IS NULL;
UPDATE alerts SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
-- Decision 4: a webhook endpoint carries a brand's data, so it belongs to
-- one brand. It still gets the workspace's own events (members, tokens),
-- which belong to no brand, but never another brand's.
UPDATE webhook_endpoints SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

CREATE INDEX rules_brand_idx ON rules (brand_id, created_at DESC);
CREATE INDEX journeys_brand_idx ON journeys (brand_id, created_at DESC);
CREATE INDEX email_templates_brand_idx ON email_templates (brand_id, name);
CREATE INDEX email_broadcasts_brand_idx ON email_broadcasts (brand_id, created_at DESC);
CREATE INDEX emails_brand_created_idx ON emails (brand_id, created_at DESC, id DESC);
CREATE INDEX alerts_brand_idx ON alerts (brand_id, created_at DESC);
CREATE INDEX webhook_endpoints_brand_idx ON webhook_endpoints (brand_id);

-- A rule's name is unique in its brand, so two brands can each have
-- "Trial stalled". No statement names rules_name_idx as an ON CONFLICT
-- arbiter (a duplicate name is a 409 from the unique violation), so it can
-- go now. Rules the version still serving makes have no brand; the
-- zero uuid keeps those unique among themselves, as before.
CREATE UNIQUE INDEX rules_brand_name_idx ON rules (workspace_id, COALESCE(brand_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name))
    WHERE status <> 'archived';
DROP INDEX rules_name_idx;

-- Templates and topics keep their per-workspace unique constraints: the
-- version still serving inserts them with ON CONFLICT (workspace_id,
-- handle) and (workspace_id, key), which need exactly those constraints.
-- Until the contract step a handle or topic key is used in one brand of a
-- workspace at a time; the per-brand ones are what the contract step keeps.
CREATE UNIQUE INDEX email_templates_brand_handle_idx ON email_templates (brand_id, handle);

-- ---- Topics stay the workspace's for now ----

-- email_topics gets no brand_id yet: the version still serving lists every
-- topic of the workspace, so a brand column here would show one brand's
-- topics under another's (its own TestNoBrandLeaks says so). This version
-- reads a topic's brand as to_jsonb(t) ->> 'brand_id', null meaning every
-- brand's, so a later migration can add the column once this one serves.

-- ---- What hangs off a parent: it takes the parent's brand ----

ALTER TABLE rule_firings ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE rule_states ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE journey_runs ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE journey_run_steps ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE journey_versions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE journey_step_secrets ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE email_template_versions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE email_broadcast_recipients ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE email_events ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE alert_deliveries ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE webhook_deliveries ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;

UPDATE rule_firings x SET brand_id = r.brand_id FROM rules r WHERE r.id = x.rule_id;
UPDATE rule_states x SET brand_id = r.brand_id FROM rules r WHERE r.id = x.rule_id;
UPDATE journey_runs x SET brand_id = j.brand_id FROM journeys j WHERE j.id = x.journey_id;
UPDATE journey_run_steps x SET brand_id = j.brand_id FROM journeys j WHERE j.id = x.journey_id;
UPDATE journey_versions x SET brand_id = j.brand_id FROM journeys j WHERE j.id = x.journey_id;
UPDATE journey_step_secrets x SET brand_id = j.brand_id FROM journeys j WHERE j.id = x.journey_id;
UPDATE email_template_versions x SET brand_id = t.brand_id FROM email_templates t WHERE t.id = x.template_id;
UPDATE email_broadcast_recipients x SET brand_id = b.brand_id FROM email_broadcasts b WHERE b.id = x.broadcast_id;
UPDATE email_events x SET brand_id = e.brand_id FROM emails e WHERE e.id = x.email_id;
UPDATE alert_deliveries x SET brand_id = a.brand_id FROM alerts a WHERE a.id = x.alert_id;
UPDATE webhook_deliveries x SET brand_id = e.brand_id FROM webhook_endpoints e WHERE e.id = x.endpoint_id;

CREATE TRIGGER rule_firings_brand BEFORE INSERT ON rule_firings
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER rule_states_brand BEFORE INSERT ON rule_states
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER journey_runs_brand BEFORE INSERT ON journey_runs
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER journey_run_steps_brand BEFORE INSERT ON journey_run_steps
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER journey_versions_brand BEFORE INSERT ON journey_versions
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER journey_step_secrets_brand BEFORE INSERT ON journey_step_secrets
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER email_template_versions_brand BEFORE INSERT ON email_template_versions
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER email_broadcast_recipients_brand BEFORE INSERT ON email_broadcast_recipients
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER email_events_brand BEFORE INSERT ON email_events
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER alert_deliveries_brand BEFORE INSERT ON alert_deliveries
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();
CREATE TRIGGER webhook_deliveries_brand BEFORE INSERT ON webhook_deliveries
    FOR EACH ROW EXECUTE FUNCTION outflare_activation_brand();

-- A parent moved to another brand takes what hangs off it along.
CREATE FUNCTION outflare_activation_brand_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'rules' THEN
        UPDATE rule_firings SET brand_id = NEW.brand_id WHERE rule_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
        UPDATE rule_states SET brand_id = NEW.brand_id WHERE rule_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    ELSIF TG_TABLE_NAME = 'journeys' THEN
        UPDATE journey_runs SET brand_id = NEW.brand_id WHERE journey_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
        UPDATE journey_run_steps SET brand_id = NEW.brand_id WHERE journey_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
        UPDATE journey_versions SET brand_id = NEW.brand_id WHERE journey_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
        UPDATE journey_step_secrets SET brand_id = NEW.brand_id WHERE journey_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    ELSIF TG_TABLE_NAME = 'email_templates' THEN
        UPDATE email_template_versions SET brand_id = NEW.brand_id WHERE template_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    ELSIF TG_TABLE_NAME = 'email_broadcasts' THEN
        UPDATE email_broadcast_recipients SET brand_id = NEW.brand_id WHERE broadcast_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER rules_brand_moved AFTER UPDATE OF brand_id ON rules
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_activation_brand_moved();
CREATE TRIGGER journeys_brand_moved AFTER UPDATE OF brand_id ON journeys
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_activation_brand_moved();
CREATE TRIGGER email_templates_brand_moved AFTER UPDATE OF brand_id ON email_templates
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_activation_brand_moved();
CREATE TRIGGER email_broadcasts_brand_moved AFTER UPDATE OF brand_id ON email_broadcasts
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_activation_brand_moved();

CREATE INDEX rule_firings_brand_idx ON rule_firings (brand_id, fired_at DESC);
CREATE INDEX rule_states_brand_idx ON rule_states (brand_id);
CREATE INDEX journey_runs_brand_idx ON journey_runs (brand_id, entered_at DESC);
CREATE INDEX journey_run_steps_brand_idx ON journey_run_steps (brand_id);
CREATE INDEX journey_versions_brand_idx ON journey_versions (brand_id);
CREATE INDEX journey_step_secrets_brand_idx ON journey_step_secrets (brand_id);
CREATE INDEX email_template_versions_brand_idx ON email_template_versions (brand_id);
CREATE INDEX email_broadcast_recipients_brand_idx ON email_broadcast_recipients (brand_id);
CREATE INDEX email_events_brand_idx ON email_events (brand_id, occurred_at DESC);
CREATE INDEX alert_deliveries_brand_idx ON alert_deliveries (brand_id);
CREATE INDEX webhook_deliveries_brand_idx ON webhook_deliveries (brand_id, created_at DESC);

-- ---- The journey signing secret ----

-- One per workspace today (its primary key is workspace_id), so it is held
-- by the workspace's first brand until the contract step makes it one per
-- brand: changing the key while the version still serving upserts on it
-- isn't expand-only.
ALTER TABLE journey_signing_secrets ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
UPDATE journey_signing_secrets SET brand_id = outflare_first_brand(workspace_id);
CREATE TRIGGER journey_signing_secrets_brand BEFORE INSERT ON journey_signing_secrets
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default');

-- ---- Query: activation per brand ----

-- Rules, their firings, journeys and their runs become queryable
-- (customers.read, like the records they act on). Emails, their events
-- and broadcasts keep to the query's brand (query_scopes.brand, 0342) on
-- each row's own brand_id; null keeps them as before, so the version
-- still serving reads what it read.

CREATE VIEW query.rules WITH (security_barrier) AS
SELECT r.id, r.brand_id, r.name, r.description, r.status, r.record_type, r.trigger_kind, r.trigger, r.conditions, r.match, r.emit_event,
    r.fire, r.cooldown_seconds, r.version, r.activated_at, r.created_at, r.updated_at
FROM rules r JOIN query_scopes s ON s.pid = pg_backend_pid() AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR r.brand_id = s.brand);

CREATE VIEW query.rule_firings WITH (security_barrier) AS
SELECT f.id, f.rule_id, f.brand_id, f.rule_version, f.record_id, f.episode_key, f.event_id, f.cause_event_id, f.depth, f.fired_at
FROM rule_firings f JOIN rules r ON r.id = f.rule_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND f.workspace_id = s.workspace_id AND r.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR r.brand_id IS NULL OR r.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (f.brand_id = s.brand AND r.brand_id = s.brand));

CREATE VIEW query.journeys WITH (security_barrier) AS
SELECT j.id, j.brand_id, j.name, j.description, j.status, j.trigger_kind, j.trigger_event, j.entry, j.goal_event, j.version,
    jsonb_array_length(j.steps) AS steps, j.activated_at, j.created_at, j.updated_at
FROM journeys j JOIN query_scopes s ON s.pid = pg_backend_pid() AND j.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR j.brand_id IS NULL OR j.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR j.brand_id = s.brand);

CREATE VIEW query.journey_runs WITH (security_barrier) AS
SELECT x.id, x.journey_id, x.brand_id, x.journey_version, x.record_id, x.status, x.step, x.exit_reason, x.steps_taken, x.entered_at,
    x.finished_at, x.updated_at
FROM journey_runs x JOIN journeys j ON j.id = x.journey_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND x.workspace_id = s.workspace_id AND j.workspace_id = s.workspace_id
WHERE 'customers.read' = ANY (s.scopes) AND (s.brands IS NULL OR j.brand_id IS NULL OR j.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (x.brand_id = s.brand AND j.brand_id = s.brand));

CREATE OR REPLACE VIEW query.emails WITH (security_barrier) AS
SELECT e.id, e.record_id, e.brand_id, e.broadcast_id, e.template_id, e.channel, e.purpose, e.subject, e.tags, e.status, e.bounce_type,
    e.test, e.opens, e.clicks, e.send_at, e.sent_at, e.delivered_at, e.opened_at, e.clicked_at, e.created_at
FROM emails e JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR e.brand_id = s.brand);

CREATE OR REPLACE VIEW query.email_events WITH (security_barrier) AS
SELECT v.id, v.email_id, v.type, v.occurred_at
FROM email_events v JOIN emails e ON e.id = v.email_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND v.workspace_id = s.workspace_id AND e.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes) AND (s.brands IS NULL OR e.brand_id IS NULL OR e.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (v.brand_id = s.brand AND e.brand_id = s.brand));

CREATE OR REPLACE VIEW query.broadcasts WITH (security_barrier) AS
SELECT b.id, b.brand_id, b.name, b.template_id, b.topic_id, b.status, b.recipients, b.skipped, b.send_at, b.started_at, b.finished_at,
    b.created_at
FROM email_broadcasts b JOIN query_scopes s ON s.pid = pg_backend_pid() AND b.workspace_id = s.workspace_id
WHERE 'emails.read' = ANY (s.scopes) AND (s.brands IS NULL OR b.brand_id IS NULL OR b.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR b.brand_id = s.brand);
