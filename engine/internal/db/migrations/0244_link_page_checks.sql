-- Link building page tools: OutflareBot's read of a link prospect's page
-- (is it up, does it link to us, which of its outbound links are broken and
-- what the Internet Archive kept of them), the AI page reader's verdict,
-- the page of ours to offer, and who to contact there. The contacts are
-- person records; this keeps which prospect found them.

-- A prospect whose page is gone is kept out of outreach too.
ALTER TABLE link_prospects DROP CONSTRAINT IF EXISTS link_prospects_excluded_reason_check;
ALTER TABLE link_prospects ADD CONSTRAINT link_prospects_excluded_reason_check
    CHECK (excluded_reason IN ('links_to_us', 'competitor', 'customer', 'partner', 'opted_out', 'spam', 'page_gone'));

-- The latest check of each prospect's page.
CREATE TABLE link_page_checks (
    prospect_id      uuid PRIMARY KEY REFERENCES link_prospects (id) ON DELETE CASCADE,
    workspace_id     uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    -- up, gone (404 or 410), redirected (to another site), blocked
    -- (robots.txt), opted_out (the site asked not to be crawled),
    -- unreachable, or failed.
    status           text NOT NULL CHECK (status IN ('up', 'gone', 'redirected', 'blocked', 'opted_out', 'unreachable', 'failed')),
    http_status      integer,
    final_url        text NOT NULL DEFAULT '',
    redirect_to      text NOT NULL DEFAULT '',
    title            text NOT NULL DEFAULT '',
    author           text NOT NULL DEFAULT '',
    lang             text NOT NULL DEFAULT '',
    noindex          boolean NOT NULL DEFAULT false,
    word_count       integer NOT NULL DEFAULT 0,
    -- Links to any of the workspace's SEO sites: [{url, anchor, nofollow}].
    links_to_us      boolean NOT NULL DEFAULT false,
    our_links        jsonb NOT NULL DEFAULT '[]',
    -- Outbound links to other sites, and the broken ones:
    -- [{url, status, anchor, archive_url, archived_at}].
    outbound         integer NOT NULL DEFAULT 0,
    broken           jsonb NOT NULL DEFAULT '[]',
    -- The AI page reader: what kind of page it is, its topic, how well it
    -- fits the site (0 to 100) and why; ai_error when it couldn't read.
    page_kind        text NOT NULL DEFAULT '',
    topic            text NOT NULL DEFAULT '',
    fit              integer CHECK (fit BETWEEN 0 AND 100),
    fit_reason       text NOT NULL DEFAULT '',
    ai_error         text,
    -- Our page that fits best, and why.
    suggested_target text NOT NULL DEFAULT '',
    suggested_reason text NOT NULL DEFAULT '',
    error_code       text,
    checked_at       timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX link_page_checks_ws_idx ON link_page_checks (workspace_id, checked_at);

-- Who to contact about a prospect: person records the contact finder found
-- on the page or the site, with where and how sure.
CREATE TABLE link_prospect_contacts (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id uuid NOT NULL REFERENCES workspaces (id) ON DELETE CASCADE,
    prospect_id  uuid NOT NULL REFERENCES link_prospects (id) ON DELETE CASCADE,
    record_id    uuid NOT NULL REFERENCES records (id) ON DELETE CASCADE,
    -- author (the page's author, email found by the email finder),
    -- page (a mailto on the page), contact_page (on the site's contact,
    -- about or write-for-us page).
    source       text NOT NULL CHECK (source IN ('author', 'page', 'contact_page')),
    -- 0 to 100: how sure the address reaches them.
    confidence   integer NOT NULL DEFAULT 0 CHECK (confidence BETWEEN 0 AND 100),
    found_at     timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX link_prospect_contacts_idx ON link_prospect_contacts (workspace_id, prospect_id);
CREATE INDEX link_prospect_contacts_record_idx ON link_prospect_contacts (workspace_id, record_id);
