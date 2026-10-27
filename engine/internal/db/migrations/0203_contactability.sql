-- Who may be emailed, and why: the contact basis on every person, and one
-- opt-out list that says who added each entry, from which tool and how.

-- The tool or feature a record came from, beside its origin: csv, apollo,
-- emailbison_import, site, stripe, attio and so on. Empty for records made
-- before this was kept.
ALTER TABLE records ADD COLUMN acquired_via text NOT NULL DEFAULT '' CHECK (length(acquired_via) <= 64);

-- Whether people who never said yes or no to marketing email may get it.
-- inferred (the default): everyone but third-party leads does, unless
-- they said no. opt_in: only people who agreed do. Third-party leads
-- never get marketing email without agreeing.
ALTER TABLE workspaces ADD COLUMN customer_marketing text NOT NULL DEFAULT 'inferred'
    CHECK (customer_marketing IN ('opt_in', 'inferred'));

-- More kinds of people to keep out: competitors, customers and partners
-- (never cold email them), and legal (a request or a law says so).
ALTER TABLE suppressions DROP CONSTRAINT IF EXISTS suppressions_reason_check;
ALTER TABLE suppressions ADD CONSTRAINT suppressions_reason_check CHECK (reason IN (
    'unsubscribed', 'bounced', 'complained', 'manual', 'do_not_contact',
    'competitor', 'customer', 'partner', 'legal'));

ALTER TABLE suppressions
    -- How it was added: by a person, automatically (a bounce, an unsubscribe
    -- link, a sync), or because the AI read a reply as an opt-out.
    ADD COLUMN method text NOT NULL DEFAULT 'automated' CHECK (method IN ('manual', 'automated', 'ai_suggested')),
    -- Who added it: the person or API token, or nobody for the system.
    ADD COLUMN added_by uuid REFERENCES actors (id) ON DELETE SET NULL,
    ADD COLUMN added_by_type text NOT NULL DEFAULT 'system' CHECK (length(added_by_type) <= 20),
    -- The connection it came from, for entries read from a sequencer.
    ADD COLUMN integration_id uuid REFERENCES integrations (id) ON DELETE SET NULL,
    -- How sure the AI was, 0 to 1, when it suggested the entry.
    ADD COLUMN confidence real CHECK (confidence BETWEEN 0 AND 1),
    -- Ids that show why: the reply, the message, the import.
    ADD COLUMN evidence jsonb NOT NULL DEFAULT '{}';

UPDATE suppressions SET method = 'manual' WHERE source IN ('manual', 'lead_deleted');

CREATE INDEX suppressions_reason_idx ON suppressions (workspace_id, reason);
