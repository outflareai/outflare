-- Fix tickets: the fixes list read as work a person or a coding agent can
-- pick up. A ticket is one problem (a source's rule) on one subject, such
-- as "pages have no title" on a site; its fixes are the parts, one per URL
-- template, which keep being found and confirmed as before. A ticket adds
-- what people and AI make of the problem: a brief written with the
-- workspace's own AI model, and a person's review. Fix guidelines are what
-- people said once that every later brief follows.

CREATE TABLE fix_tickets (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id    uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id        uuid REFERENCES brands (id) ON DELETE SET NULL,
    source          text NOT NULL,
    subject_type    text NOT NULL,
    subject_id      uuid NOT NULL,
    rule            text NOT NULL,
    -- The brief: none until asked for, then queued, writing, ready or
    -- failed. brief holds title, summary, why, change, done_when and dont.
    brief_status    text NOT NULL DEFAULT 'none' CHECK (brief_status IN ('none', 'queued', 'writing', 'ready', 'failed')),
    brief           jsonb,
    brief_error     text NOT NULL DEFAULT '',
    brief_model     text NOT NULL DEFAULT '',
    -- What the person asked the next brief to change.
    brief_feedback  text NOT NULL DEFAULT '',
    brief_written_at timestamptz,
    -- A person's call: '' (not reviewed), approved or rejected.
    review          text NOT NULL DEFAULT '' CHECK (review IN ('', 'approved', 'rejected')),
    review_note     text NOT NULL DEFAULT '',
    reviewed_by     uuid REFERENCES actors (id) ON DELETE SET NULL,
    reviewed_at     timestamptz,
    -- Search clicks and impressions of the affected pages over the last 28
    -- days, when Search Console has them: what the ticket is worth.
    impact_clicks      integer,
    impact_impressions integer,
    impact_at       timestamptz,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, source, subject_id, rule)
);
CREATE INDEX fix_tickets_subject_idx ON fix_tickets (workspace_id, subject_id);

-- Every fix belongs to a ticket: one is made with the first fix of its
-- rule on its subject, by whatever code adds the fix.
CREATE FUNCTION fix_ticket_for_fix() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO fix_tickets (workspace_id, brand_id, source, subject_type, subject_id, rule)
    VALUES (NEW.workspace_id, NEW.brand_id, NEW.source, NEW.subject_type, NEW.subject_id, NEW.rule)
    ON CONFLICT (workspace_id, source, subject_id, rule) DO UPDATE SET brand_id = EXCLUDED.brand_id
        WHERE fix_tickets.brand_id IS DISTINCT FROM EXCLUDED.brand_id;
    RETURN NEW;
END $$;
CREATE TRIGGER fixes_ticket AFTER INSERT OR UPDATE OF brand_id ON fixes
    FOR EACH ROW EXECUTE FUNCTION fix_ticket_for_fix();

-- A ticket goes with its last fix (the site or fact it was about was
-- deleted).
CREATE FUNCTION fix_ticket_after_fix_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    DELETE FROM fix_tickets t
    WHERE t.workspace_id = OLD.workspace_id AND t.source = OLD.source AND t.subject_id = OLD.subject_id AND t.rule = OLD.rule
      AND NOT EXISTS (SELECT 1 FROM fixes f WHERE f.workspace_id = OLD.workspace_id AND f.source = OLD.source
                      AND f.subject_id = OLD.subject_id AND f.rule = OLD.rule);
    RETURN OLD;
END $$;
CREATE TRIGGER fixes_ticket_delete AFTER DELETE ON fixes
    FOR EACH ROW EXECUTE FUNCTION fix_ticket_after_fix_delete();

INSERT INTO fix_tickets (workspace_id, brand_id, source, subject_type, subject_id, rule)
SELECT DISTINCT ON (workspace_id, source, subject_id, rule) workspace_id, brand_id, source, subject_type, subject_id, rule
FROM fixes ORDER BY workspace_id, source, subject_id, rule, created_at
ON CONFLICT DO NOTHING;

-- What people told the brief writer once, for every later brief: for the
-- whole workspace, a brand, or one subject (a site), and for every rule or
-- one ('').
CREATE TABLE fix_guidelines (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    brand_id     uuid REFERENCES brands (id) ON DELETE CASCADE,
    subject_id   uuid,
    rule         text NOT NULL DEFAULT '',
    text         text NOT NULL CHECK (length(text) BETWEEN 1 AND 1000),
    -- The ticket whose review it came from, if any.
    ticket_id    uuid REFERENCES fix_tickets (id) ON DELETE SET NULL,
    created_by   uuid REFERENCES actors (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX fix_guidelines_ws_idx ON fix_guidelines (workspace_id, created_at);
