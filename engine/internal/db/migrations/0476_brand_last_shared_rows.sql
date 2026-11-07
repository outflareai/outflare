-- Brands are fully separate, so the last rows saved with no brand stop
-- counting for every brand. Content and records go to the workspace's
-- first brand (keeping their ids); a website goes to the brand whose
-- domain it is. Settings that guide work (news topics, fix guidelines)
-- are copied into every other brand too, so each brand keeps them.
--
-- Rows with no brand that mean "the address, everywhere" stay as they
-- are: a hard bounce, complaint or blocklist opt-out (leads.EveryBrand),
-- a connection shared by every brand, and an API token for every brand.
--
-- Expand only: the previous version shows a brand its own rows and those
-- with no brand, so during a deploy each brand still sees each item.

-- People and companies. Their identities, history and list members follow
-- the record (records_brand_moved).
UPDATE records SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

-- Leads and lead lists. A list name is unique per brand, so a list with no
-- brand whose name the first brand already has keeps its own row and
-- takes the first brand only when it's free.
UPDATE lead_lists l SET brand_id = outflare_first_brand(l.workspace_id)
WHERE l.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM lead_lists o WHERE o.brand_id = outflare_first_brand(l.workspace_id) AND lower(o.name) = lower(l.name));
UPDATE leads l SET brand_id = outflare_first_brand(l.workspace_id)
WHERE l.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM leads o WHERE o.brand_id = outflare_first_brand(l.workspace_id) AND lower(o.email) = lower(l.email));

-- Email topics, the facts a brand states to AI, sequences and support
-- conversations.
UPDATE email_topics t SET brand_id = outflare_first_brand(t.workspace_id)
WHERE t.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM email_topics o WHERE o.brand_id = outflare_first_brand(t.workspace_id) AND o.key = t.key);
UPDATE ai_brand_facts f SET brand_id = outflare_first_brand(f.workspace_id)
WHERE f.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM ai_brand_facts o WHERE o.workspace_id = f.workspace_id
    AND o.brand_id = outflare_first_brand(f.workspace_id) AND lower(o.statement) = lower(f.statement));
UPDATE sequences SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE support_conversations SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

-- A website is the brand's whose domain it is, else the first brand's.
UPDATE seo_sites SET brand_id = COALESCE(outflare_host_brand(workspace_id, host), outflare_first_brand(workspace_id))
WHERE brand_id IS NULL;

-- News topics every brand followed: a copy for every other brand, which
-- finds its own articles from the next check.
INSERT INTO news_searches (workspace_id, brand_id, name, query, purpose, locale, time_range, check_every_hours, status,
    created_by, created_at, updated_at)
SELECT n.workspace_id, b.id, n.name, n.query, n.purpose, n.locale, n.time_range, n.check_every_hours, n.status,
    n.created_by, n.created_at, n.updated_at
FROM news_searches n
JOIN brands b ON b.workspace_id = n.workspace_id AND b.id <> outflare_first_brand(n.workspace_id)
WHERE n.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM news_searches o WHERE o.workspace_id = n.workspace_id AND o.brand_id = b.id
    AND o.purpose = n.purpose AND lower(o.query) = lower(n.query));
UPDATE news_searches SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;

-- Fix guidelines for the whole workspace: a copy for every other brand.
-- One for a single page or site (subject_id) already says what it's for.
INSERT INTO fix_guidelines (workspace_id, brand_id, subject_id, rule, text, ticket_id, created_by, created_at)
SELECT g.workspace_id, b.id, NULL, g.rule, g.text, g.ticket_id, g.created_by, g.created_at
FROM fix_guidelines g
JOIN brands b ON b.workspace_id = g.workspace_id AND b.id <> outflare_first_brand(g.workspace_id)
WHERE g.brand_id IS NULL AND g.subject_id IS NULL;
UPDATE fix_guidelines SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL AND subject_id IS NULL;
