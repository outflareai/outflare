-- Fix tickets get a short number people can say and write, counted per
-- workspace: "fix #128" in a pull request, a commit or a chat, instead of
-- a UUID. The number is given when the ticket is made and never changes.

ALTER TABLE fix_tickets ADD COLUMN number integer;

-- The tickets there already, oldest first. The table holds one row per
-- problem per site, so this is quick.
UPDATE fix_tickets t SET number = n.number
FROM (SELECT id, row_number() OVER (PARTITION BY workspace_id ORDER BY created_at, id) AS number FROM fix_tickets) n
WHERE n.id = t.id;

CREATE UNIQUE INDEX fix_tickets_number_idx ON fix_tickets (workspace_id, number);

-- A new ticket takes the workspace's next number. The lock makes two fixes
-- added at once count one after the other; it is only taken when the
-- ticket is not there yet.
CREATE OR REPLACE FUNCTION fix_ticket_for_fix() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM fix_tickets WHERE workspace_id = NEW.workspace_id AND source = NEW.source
        AND subject_id = NEW.subject_id AND rule = NEW.rule) THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('fix_ticket_number:' || NEW.workspace_id::text, 0));
    END IF;
    INSERT INTO fix_tickets (workspace_id, brand_id, source, subject_type, subject_id, rule, number)
    VALUES (NEW.workspace_id, NEW.brand_id, NEW.source, NEW.subject_type, NEW.subject_id, NEW.rule,
        (SELECT COALESCE(max(number), 0) + 1 FROM fix_tickets WHERE workspace_id = NEW.workspace_id))
    ON CONFLICT (workspace_id, source, subject_id, rule) DO UPDATE SET brand_id = EXCLUDED.brand_id
        WHERE fix_tickets.brand_id IS DISTINCT FROM EXCLUDED.brand_id;
    RETURN NEW;
END $$;
