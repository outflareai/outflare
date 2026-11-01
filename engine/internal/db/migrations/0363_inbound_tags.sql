-- Reply tags are called tags in the API (#708); the column that holds them
-- is still inbound_messages.labels. This is the expand step of renaming it:
-- add tags, copy what labels holds, and keep the two equal while a version
-- that only knows labels may still run beside this one (a deploy, or a
-- rollback). The code writes both and reads tags. A later release drops
-- labels, its trigger and function, and the view's labels column.

ALTER TABLE inbound_messages ADD COLUMN tags text[] NOT NULL DEFAULT '{}';

UPDATE inbound_messages SET tags = labels WHERE labels <> '{}';

-- Whichever column a write changed is copied to the other, so a version
-- that writes only labels keeps tags right, and the other way round.
CREATE FUNCTION outflare_inbound_tags_sync() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.tags = '{}' THEN
            NEW.tags := NEW.labels;
        ELSIF NEW.labels = '{}' THEN
            NEW.labels := NEW.tags;
        END IF;
    ELSIF NEW.labels IS DISTINCT FROM OLD.labels AND NEW.tags IS NOT DISTINCT FROM OLD.tags THEN
        NEW.tags := NEW.labels;
    ELSIF NEW.tags IS DISTINCT FROM OLD.tags AND NEW.labels IS NOT DISTINCT FROM OLD.labels THEN
        NEW.labels := NEW.tags;
    END IF;
    RETURN NEW;
END $$;

CREATE TRIGGER inbound_tags_sync BEFORE INSERT OR UPDATE OF labels, tags ON inbound_messages
    FOR EACH ROW EXECUTE FUNCTION outflare_inbound_tags_sync();

-- The Query table shows tags too; a view only takes new columns at its end.
CREATE OR REPLACE VIEW query.replies WITH (security_barrier) AS
SELECT i.id, i.record_id, i.lead_id, i.sequence_id, i.enrollment_id, i.mailbox_id, i.from_email, i.subject, i.kind, i.category,
    i.opt_out, i.labels, i.done, i.received_at, i.tags
FROM inbound_messages i LEFT JOIN mailboxes x ON x.id = i.mailbox_id
JOIN query_scopes s ON s.pid = pg_backend_pid() AND i.workspace_id = s.workspace_id
WHERE 'inbox.read' = ANY (s.scopes) AND (s.brands IS NULL OR x.brand_id IS NULL OR x.brand_id = ANY (s.brands));
