-- Brands own their data, step 5 (plans/brands/README.md): outreach.
-- Leads and lead lists, their builds and sources, sequences with their
-- enrollments and sends, replies, meetings and deals each belong to one
-- brand, set on the row itself, so a brand's outreach data is exactly
-- WHERE brand_id = $1. Suppressions get a brand too, where null keeps
-- meaning every brand: hard bounces, complaints and what an admin applies
-- to every brand (decision 5).
--
-- Production holds only test and staff workspaces (decision 7), so every
-- row that was "for every brand" goes to its workspace's first brand.
-- Expand only (docs/migrations.md): columns, indexes, triggers, backfills
-- and views that only gain columns.
--
-- lead_lists and sequences already had a nullable brand_id that the
-- version still serving reads, where null means every brand. They are
-- backfilled, and the new code sets the brand of every row it writes; no
-- trigger fills them, so the older version keeps writing rows for every
-- brand as its tests expect. leads is new to brand_id (nothing older
-- reads it), so a trigger gives the older version's leads the first
-- brand. What hangs off a parent takes the parent's brand from a trigger.

-- The brand of an outreach row: its parent's, else the workspace's first.
CREATE FUNCTION outflare_outreach_brand() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    b uuid;
BEGIN
    -- A row whose parent changed takes the new parent's brand, unless the
    -- writer set one itself.
    IF TG_OP = 'UPDATE' THEN
        IF NEW.brand_id IS DISTINCT FROM OLD.brand_id THEN
            RETURN NEW;
        END IF;
        NEW.brand_id := NULL;
    END IF;
    IF NEW.brand_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    -- One branch per table: plpgsql only resolves the one it runs.
    IF TG_TABLE_NAME = 'lead_sources' THEN
        SELECT brand_id INTO b FROM leads WHERE id = NEW.lead_id;
    ELSIF TG_TABLE_NAME = 'list_runs' THEN
        SELECT brand_id INTO b FROM lead_lists WHERE id = NEW.list_id;
    ELSIF TG_TABLE_NAME = 'enrollments' THEN
        SELECT brand_id INTO b FROM sequences WHERE id = NEW.sequence_id;
    ELSIF TG_TABLE_NAME = 'messages' THEN
        SELECT COALESCE(
            (SELECT brand_id FROM sequences WHERE id = NEW.sequence_id),
            (SELECT brand_id FROM inbound_messages WHERE id = NEW.reply_to_inbound_id),
            (SELECT brand_id FROM leads WHERE id = NEW.lead_id)) INTO b;
    ELSIF TG_TABLE_NAME = 'inbound_messages' THEN
        -- A reply to a sequence is the sequence's; else the lead's; else
        -- the mailbox's when it belongs to one brand.
        SELECT COALESCE(
            (SELECT brand_id FROM sequences WHERE id = NEW.sequence_id),
            (SELECT brand_id FROM leads WHERE id = NEW.lead_id),
            (SELECT brand_id FROM mailboxes WHERE id = NEW.mailbox_id)) INTO b;
    ELSIF TG_TABLE_NAME IN ('deals', 'meetings') THEN
        SELECT COALESCE(
            (SELECT brand_id FROM records WHERE id = NEW.record_id),
            (SELECT brand_id FROM sequences WHERE id = NEW.sequence_id),
            (SELECT brand_id FROM leads WHERE id = NEW.lead_id),
            (SELECT brand_id FROM integrations WHERE id = NEW.integration_id)) INTO b;
    END IF;
    NEW.brand_id := COALESCE(b, outflare_first_brand(NEW.workspace_id));
    RETURN NEW;
END
$$;

-- ---- Lists and sequences: what was for every brand goes to the first ----

UPDATE lead_lists SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE sequences SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

CREATE INDEX lead_lists_brand_created_idx ON lead_lists (brand_id, created_at DESC);
CREATE INDEX sequences_brand_created_idx ON sequences (brand_id, created_at DESC);

-- A list's name is unique in its brand. The version still serving creates
-- lists with ON CONFLICT (workspace_id, lower(name)), which needs exactly
-- lead_lists_name_idx, so until the contract step a list name is used in
-- one brand of a workspace at a time; this is the index the contract step
-- keeps.
CREATE UNIQUE INDEX lead_lists_brand_name_idx ON lead_lists (brand_id, lower(name));

-- ---- Leads: unique per brand ----

ALTER TABLE leads ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;

-- A lead goes with its person, else a list it is on, else a sequence it
-- is in, else the workspace's first brand.
UPDATE leads l SET brand_id = COALESCE(
    (SELECT r.brand_id FROM records r WHERE r.id = l.record_id),
    (SELECT x.brand_id FROM lead_list_members m JOIN lead_lists x ON x.id = m.list_id
        WHERE m.lead_id = l.id ORDER BY m.added_at, x.id LIMIT 1),
    (SELECT q.brand_id FROM enrollments e JOIN sequences q ON q.id = e.sequence_id
        WHERE e.lead_id = l.id ORDER BY e.created_at, e.id LIMIT 1),
    outflare_first_brand(l.workspace_id));

-- The version still serving inserts leads without a brand: they go to the
-- workspace's first brand, as its leads always did.
CREATE TRIGGER leads_brand BEFORE INSERT ON leads
    FOR EACH ROW EXECUTE FUNCTION outflare_brand_fill('default');

-- The same address can be a lead of two brands (decision 1): they are two
-- people. The version still serving upserts leads with ON CONFLICT
-- (workspace_id, lower(email)), which needs exactly leads_email_idx, so it
-- stays until the contract step and an address is a lead in one brand of a
-- workspace at a time until then (the API says lead_in_other_brand). New
-- code upserts on this one.
CREATE UNIQUE INDEX leads_brand_email_idx ON leads (brand_id, lower(email));
CREATE INDEX leads_brand_created_idx ON leads (brand_id, created_at DESC, id);

-- ---- What hangs off a parent: it takes the parent's brand ----

ALTER TABLE lead_sources ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE list_runs ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE enrollments ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE messages ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE inbound_messages ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE deals ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
ALTER TABLE meetings ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;

UPDATE lead_sources x SET brand_id = l.brand_id FROM leads l WHERE l.id = x.lead_id;
UPDATE list_runs x SET brand_id = COALESCE((SELECT l.brand_id FROM lead_lists l WHERE l.id = x.list_id), outflare_first_brand(x.workspace_id));
UPDATE enrollments x SET brand_id = q.brand_id FROM sequences q WHERE q.id = x.sequence_id;
UPDATE inbound_messages x SET brand_id = COALESCE(
    (SELECT brand_id FROM sequences WHERE id = x.sequence_id),
    (SELECT brand_id FROM leads WHERE id = x.lead_id),
    (SELECT brand_id FROM mailboxes WHERE id = x.mailbox_id),
    outflare_first_brand(x.workspace_id));
UPDATE messages x SET brand_id = COALESCE(
    (SELECT brand_id FROM sequences WHERE id = x.sequence_id),
    (SELECT brand_id FROM inbound_messages WHERE id = x.reply_to_inbound_id),
    (SELECT brand_id FROM leads WHERE id = x.lead_id),
    outflare_first_brand(x.workspace_id));
UPDATE deals x SET brand_id = COALESCE(
    (SELECT brand_id FROM records WHERE id = x.record_id),
    (SELECT brand_id FROM sequences WHERE id = x.sequence_id),
    (SELECT brand_id FROM leads WHERE id = x.lead_id),
    (SELECT brand_id FROM integrations WHERE id = x.integration_id),
    outflare_first_brand(x.workspace_id));
UPDATE meetings x SET brand_id = COALESCE(
    (SELECT brand_id FROM records WHERE id = x.record_id),
    (SELECT brand_id FROM sequences WHERE id = x.sequence_id),
    (SELECT brand_id FROM leads WHERE id = x.lead_id),
    (SELECT brand_id FROM integrations WHERE id = x.integration_id),
    outflare_first_brand(x.workspace_id));

CREATE TRIGGER lead_sources_brand BEFORE INSERT ON lead_sources
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();
CREATE TRIGGER list_runs_brand BEFORE INSERT ON list_runs
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();
CREATE TRIGGER enrollments_brand BEFORE INSERT ON enrollments
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();
CREATE TRIGGER messages_brand BEFORE INSERT ON messages
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();
-- A reply is matched to its lead and sequence after it arrives, and a
-- meeting or deal to its person: each takes the brand of what it is
-- matched to.
CREATE TRIGGER inbound_messages_brand BEFORE INSERT OR UPDATE OF sequence_id, lead_id ON inbound_messages
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();
CREATE TRIGGER deals_brand BEFORE INSERT OR UPDATE OF record_id, sequence_id, lead_id ON deals
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();
CREATE TRIGGER meetings_brand BEFORE INSERT OR UPDATE OF record_id, sequence_id, lead_id ON meetings
    FOR EACH ROW EXECUTE FUNCTION outflare_outreach_brand();

-- A parent moved to another brand takes what hangs off it along.
CREATE FUNCTION outflare_outreach_brand_moved() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME = 'leads' THEN
        UPDATE lead_sources SET brand_id = NEW.brand_id WHERE lead_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    ELSIF TG_TABLE_NAME = 'lead_lists' THEN
        UPDATE list_runs SET brand_id = NEW.brand_id WHERE list_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    ELSIF TG_TABLE_NAME = 'sequences' THEN
        UPDATE enrollments SET brand_id = NEW.brand_id WHERE sequence_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
        UPDATE messages SET brand_id = NEW.brand_id WHERE sequence_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
        UPDATE inbound_messages SET brand_id = NEW.brand_id WHERE sequence_id = NEW.id AND brand_id IS DISTINCT FROM NEW.brand_id;
    END IF;
    RETURN NULL;
END
$$;

CREATE TRIGGER leads_brand_moved AFTER UPDATE OF brand_id ON leads
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_outreach_brand_moved();
CREATE TRIGGER lead_lists_brand_moved AFTER UPDATE OF brand_id ON lead_lists
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_outreach_brand_moved();
CREATE TRIGGER sequences_brand_moved AFTER UPDATE OF brand_id ON sequences
    FOR EACH ROW WHEN (NEW.brand_id IS NOT NULL AND OLD.brand_id IS DISTINCT FROM NEW.brand_id)
    EXECUTE FUNCTION outflare_outreach_brand_moved();

CREATE INDEX lead_sources_brand_idx ON lead_sources (brand_id);
CREATE INDEX list_runs_brand_idx ON list_runs (brand_id, created_at DESC);
CREATE INDEX enrollments_brand_idx ON enrollments (brand_id, status);
CREATE INDEX messages_brand_idx ON messages (brand_id, created_at DESC);
CREATE INDEX inbound_messages_brand_idx ON inbound_messages (brand_id, received_at DESC);
CREATE INDEX deals_brand_idx ON deals (brand_id, opened_at DESC);
CREATE INDEX meetings_brand_idx ON meetings (brand_id, starts_at DESC);

-- ---- Suppressions: per brand, or every brand ----

-- An unsubscribe, a do-not-contact or a manual entry applies to the brand
-- it came from; null is every brand: hard bounces, complaints, blocklist
-- syncs and what an admin applies to every brand (decision 5). Every
-- entry so far was for every brand, so none is backfilled, and the
-- version still serving reads and writes them as before. Its unique key
-- (workspace_id, kind, value) is the one its ON CONFLICT names, so until
-- the contract step one address has one entry in a workspace: a second
-- brand's opt-out of the same address widens it to every brand.
ALTER TABLE suppressions ADD COLUMN brand_id uuid REFERENCES brands (id) ON DELETE SET NULL;
CREATE INDEX suppressions_brand_idx ON suppressions (brand_id) WHERE brand_id IS NOT NULL;

-- ---- Query: outreach per brand ----

-- Leads become readable in a brand's query (they had no brand before).
-- Every outreach view keeps to the query's brand (query_scopes.brand,
-- 0342) on each row's own brand_id, and lists it last (a replaced view
-- only gains columns).

CREATE OR REPLACE VIEW query.leads WITH (security_barrier) AS
SELECT l.id, l.record_id, l.email, l.domain, l.first_name, l.last_name, l.company, l.title, l.source, l.custom, l.created_at,
    l.updated_at, l.brand_id
FROM leads l JOIN query_scopes s ON s.pid = pg_backend_pid() AND l.workspace_id = s.workspace_id
WHERE 'leads.read' = ANY (s.scopes) AND (s.brands IS NULL OR l.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR l.brand_id = s.brand);

CREATE OR REPLACE VIEW query.sequences WITH (security_barrier) AS
SELECT q.id, q.brand_id, q.mission_id, q.name, q.status, q.daily_new_limit, q.stop_on_reply, q.created_at, q.updated_at
FROM sequences q JOIN query_scopes s ON s.pid = pg_backend_pid() AND q.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR q.brand_id = s.brand);

CREATE OR REPLACE VIEW query.enrollments WITH (security_barrier) AS
SELECT e.id, e.sequence_id, e.lead_id, e.mailbox_id, e.status, e.status_reason, e.next_step, e.next_send_at, e.created_at, e.updated_at,
    e.brand_id
FROM enrollments e JOIN sequences q ON q.id = e.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND e.workspace_id = s.workspace_id AND q.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR (e.brand_id = s.brand AND q.brand_id = s.brand));

CREATE OR REPLACE VIEW query.messages WITH (security_barrier) AS
SELECT m.id, m.record_id, m.sequence_id, m.enrollment_id, m.step_position, m.lead_id, m.mailbox_id, m.from_email, m.to_email, m.subject,
    m.status, m.attempts, m.created_at, m.sent_at, m.brand_id
FROM messages m LEFT JOIN sequences q ON q.id = m.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'sequences.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR m.brand_id = s.brand);

CREATE OR REPLACE VIEW query.replies WITH (security_barrier) AS
SELECT i.id, i.record_id, i.lead_id, i.sequence_id, i.enrollment_id, i.mailbox_id, i.from_email, i.subject, i.kind, i.category, i.opt_out,
    i.labels, i.done, i.received_at, i.tags, i.brand_id
FROM inbound_messages i LEFT JOIN mailboxes x ON x.id = i.mailbox_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND i.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes) AND (s.brands IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR i.brand_id = s.brand);

CREATE OR REPLACE VIEW query.deals WITH (security_barrier) AS
SELECT d.id, d.record_id, d.mission_id, d.sequence_id, d.lead_id, d.meeting_id, d.source, d.name, d.stage, d.state, d.amount_cents,
    d.currency, d.attribution, d.opened_at, d.closed_at, d.created_at, d.updated_at, d.brand_id
FROM deals d LEFT JOIN sequences q ON q.id = d.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND d.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR d.brand_id = s.brand);

CREATE OR REPLACE VIEW query.meetings WITH (security_barrier) AS
SELECT m.id, m.record_id, m.mission_id, m.sequence_id, m.lead_id, m.source, m.title, m.status, m.qualified, m.attribution, m.starts_at,
    m.ends_at, m.booked_at, m.created_at, m.brand_id
FROM meetings m LEFT JOIN sequences q ON q.id = m.sequence_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND m.workspace_id = s.workspace_id
WHERE 'outcomes.read' = ANY (s.scopes) AND (s.brands IS NULL OR q.brand_id IS NULL OR q.brand_id = ANY (s.brands))
    AND (s.brand IS NULL OR m.brand_id = s.brand);
