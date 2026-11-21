-- OutflareBot's search of a donor's own site for who to write to about a
-- placement: when it last ran, how it ended, and the addresses it found,
-- best first (the advertising desk before the editors before info@). The
-- addresses are also person records on the site's company, so they show
-- with the donor's other contacts; this keeps their order and the outcome
-- for the catalog and for sending the best one to a sequence.
ALTER TABLE coverage_donor_profiles
    ADD COLUMN email_search_at timestamptz,
    ADD COLUMN email_search_status text NOT NULL DEFAULT ''
        CHECK (email_search_status IN ('', 'found', 'none', 'blocked', 'unreachable', 'opted_out', 'failed')),
    ADD COLUMN email_search_emails text[] NOT NULL DEFAULT '{}';
