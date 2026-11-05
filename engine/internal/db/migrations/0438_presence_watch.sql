-- Presence step 3: watch each placement over time. What the latest page
-- checks agree on is kept on the placement, so lists can show and filter
-- placements that went offline, lost their mention or lost their link,
-- and since when. The observations stay the history.

ALTER TABLE coverage_placements
    -- present: the last check saw it (one check without it is not enough to
    -- change that). missing: the page is up but it has never been seen there.
    -- removed: it had been there, and two checks in a row saw the page up
    -- without it. unknown: not read yet.
    ADD COLUMN link_status text NOT NULL DEFAULT 'unknown' CHECK (link_status IN ('unknown','present','missing','removed')),
    ADD COLUMN mention_status text NOT NULL DEFAULT 'unknown' CHECK (mention_status IN ('unknown','present','missing','removed')),
    -- When the page went offline (two checks agree), the link or the mention
    -- was removed; cleared when it comes back.
    ADD COLUMN offline_since timestamptz,
    ADD COLUMN link_removed_at timestamptz,
    ADD COLUMN mention_removed_at timestamptz,
    -- When the brand facts on the page were last queued for a check, so a
    -- change to the brand's published Knowledge queues it again.
    ADD COLUMN accuracy_queued_at timestamptz;

-- A removed brand mention is work, like a removed link. Widening a CHECK
-- only expands: the previous version never writes the new value.
ALTER TABLE coverage_tasks DROP CONSTRAINT IF EXISTS coverage_tasks_reason_check;
ALTER TABLE coverage_tasks ADD CONSTRAINT coverage_tasks_reason_check CHECK (reason IN (
    'manual','publication_overdue','first_live','page_gone','link_missing','link_changed','outdated_claim','renewal_due','invoice_due','relationship_followup',
    'mention_missing'));
ALTER TABLE coverage_rules DROP CONSTRAINT IF EXISTS coverage_rules_kind_check;
ALTER TABLE coverage_rules ADD CONSTRAINT coverage_rules_kind_check CHECK (kind IN (
    'first_live','relationship_followup','page_gone','link_missing','link_changed','publication_overdue','outdated_claim',
    'mention_missing'));

CREATE INDEX coverage_placements_watch_idx ON coverage_placements (workspace_id, brand_id, link_status, mention_status);
