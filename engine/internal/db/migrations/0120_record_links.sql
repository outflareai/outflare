-- Everything Outflare does with a person points at their customer record
-- (0082), so a profile shows their cold email, replies, meetings, deals and
-- lifecycle email beside their product events, and nothing about a person
-- is kept apart from them.
--
-- Leads get a person record of their own (origin prospect) or join the one
-- that already has their email; the customers.link job does that for
-- existing leads and new ones get it as they are added. Activity rows take
-- their lead's record, or the record that owns their email address, when
-- they are written; the job fills in older rows.

ALTER TABLE leads ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;
ALTER TABLE messages ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;
ALTER TABLE inbound_messages ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;
ALTER TABLE meetings ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;
ALTER TABLE deals ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;
ALTER TABLE emails ADD COLUMN record_id uuid REFERENCES records (id) ON DELETE SET NULL;

CREATE INDEX leads_record_idx ON leads (record_id) WHERE record_id IS NOT NULL;
CREATE INDEX leads_unlinked_idx ON leads (workspace_id, id) WHERE record_id IS NULL;
CREATE INDEX messages_record_idx ON messages (record_id, created_at DESC) WHERE record_id IS NOT NULL;
CREATE INDEX inbound_messages_record_idx ON inbound_messages (record_id, received_at DESC) WHERE record_id IS NOT NULL;
CREATE INDEX meetings_record_idx ON meetings (record_id, booked_at DESC) WHERE record_id IS NOT NULL;
CREATE INDEX deals_record_idx ON deals (record_id, opened_at DESC) WHERE record_id IS NOT NULL;
CREATE INDEX emails_record_idx ON emails (record_id, created_at DESC) WHERE record_id IS NOT NULL;

-- Broadcast recipients already name a record (0094); now the database
-- holds them to it.
ALTER TABLE email_broadcast_recipients
    ADD CONSTRAINT email_broadcast_recipients_record_fk FOREIGN KEY (record_id) REFERENCES records (id) ON DELETE CASCADE NOT VALID;
ALTER TABLE email_broadcast_recipients VALIDATE CONSTRAINT email_broadcast_recipients_record_fk;

-- The person an email address belongs to: its verified owner, or else the
-- oldest person who has it.
CREATE FUNCTION outflare_person_with_email(ws uuid, address text) RETURNS uuid
LANGUAGE sql STABLE AS $$
    SELECT i.record_id FROM record_identities i JOIN records r ON r.id = i.record_id
    WHERE i.workspace_id = ws AND i.namespace = 'email' AND i.value = lower(trim(address)) AND i.valid_to IS NULL
      AND r.merged_into IS NULL AND r.record_type = 'person'
    ORDER BY i.verified DESC, i.valid_from
    LIMIT 1
$$;

-- Fills record_id on an activity row as it is written: from its lead, or
-- from the address in the column named by the trigger's argument.
CREATE FUNCTION outflare_link_activity() RETURNS trigger
LANGUAGE plpgsql AS $$
DECLARE
    address text;
BEGIN
    IF NEW.record_id IS NOT NULL THEN
        RETURN NEW;
    END IF;
    IF NEW.lead_id IS NOT NULL THEN
        SELECT l.record_id INTO NEW.record_id FROM leads l WHERE l.id = NEW.lead_id;
    END IF;
    IF NEW.record_id IS NULL AND TG_NARGS > 0 THEN
        address := to_jsonb(NEW) ->> TG_ARGV[0];
        IF address IS NOT NULL AND address <> '' THEN
            NEW.record_id := outflare_person_with_email(NEW.workspace_id, address);
        END IF;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER messages_link_record BEFORE INSERT OR UPDATE OF lead_id ON messages
    FOR EACH ROW EXECUTE FUNCTION outflare_link_activity('to_email');
CREATE TRIGGER inbound_messages_link_record BEFORE INSERT OR UPDATE OF lead_id ON inbound_messages
    FOR EACH ROW EXECUTE FUNCTION outflare_link_activity('from_email');
CREATE TRIGGER meetings_link_record BEFORE INSERT OR UPDATE OF lead_id ON meetings
    FOR EACH ROW EXECUTE FUNCTION outflare_link_activity('attendee_email');
CREATE TRIGGER deals_link_record BEFORE INSERT OR UPDATE OF lead_id ON deals
    FOR EACH ROW EXECUTE FUNCTION outflare_link_activity();

-- Lifecycle email to one person links to that person, but only to the
-- verified owner of the address: sending to an address never makes it
-- someone's.
CREATE FUNCTION outflare_link_email() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.record_id IS NULL AND NOT NEW.test AND cardinality(NEW.to_addresses) = 1 THEN
        SELECT i.record_id INTO NEW.record_id FROM record_identities i JOIN records r ON r.id = i.record_id
        WHERE i.workspace_id = NEW.workspace_id AND i.namespace = 'email' AND i.value = lower(trim(NEW.to_addresses[1]))
          AND i.valid_to IS NULL AND i.verified AND r.merged_into IS NULL;
    END IF;
    RETURN NEW;
END
$$;

CREATE TRIGGER emails_link_record BEFORE INSERT ON emails
    FOR EACH ROW EXECUTE FUNCTION outflare_link_email();

-- Link the leads and activity every workspace already has.
INSERT INTO jobs (workspace_id, kind, payload, max_attempts, idempotency_key)
SELECT id, 'customers.link', '{}'::jsonb, 10, 'customers.link:0120:' || id
FROM workspaces;
