-- The fixes list: one list of work for the whole workspace, shared by
-- people and agents. Each feature that finds work adds its fixes here with
-- its own source, so Home and agents read one list rather than one per
-- feature. Site audits come first (source seo_audit): one fix is one rule
-- on one URL template of a site ("pages in /blog/* have no meta
-- description"), so fixing the template clears it. The run that found it
-- keeps finding it, and the next complete run that no longer finds it
-- confirms it done.
CREATE TABLE fixes (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- The subject's brand, kept in step with it, for brand filters and
    -- brand-limited members. NULL is shared.
    brand_id      uuid REFERENCES brands (id) ON DELETE SET NULL,
    -- What found it: seo_audit (crawl issues of an SEO site).
    source        text NOT NULL,
    -- What it is about: for seo_audit, the seo_site.
    subject_type  text NOT NULL,
    subject_id    uuid NOT NULL,
    -- The source's rule, like title_missing, and what it groups by (for
    -- seo_audit, the URL template).
    rule          text NOT NULL,
    severity      text NOT NULL CHECK (severity IN ('error', 'warning', 'notice')),
    group_key     text NOT NULL DEFAULT '',
    -- open, in_progress (someone took it), done (a run confirmed it, or
    -- someone said so until the next run checks), ignored (on purpose).
    status        text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'done', 'ignored')),
    -- Who is on it: a member's actor or an agent's.
    assignee_id   uuid REFERENCES actors (id) ON DELETE SET NULL,
    note          text NOT NULL DEFAULT '',
    -- How much is left in the latest run that found it, in unit (pages).
    count         integer NOT NULL DEFAULT 0,
    unit          text NOT NULL DEFAULT '',
    -- The source's runs (for seo_audit, crawl_runs): the first and latest
    -- that found it, and the one that confirmed it fixed.
    found_by      uuid,
    last_found_by uuid,
    confirmed_by  uuid,
    fixed_at      timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, source, subject_id, rule, group_key)
);
CREATE INDEX fixes_status_idx ON fixes (workspace_id, status);
CREATE INDEX fixes_subject_idx ON fixes (workspace_id, subject_type, subject_id);
