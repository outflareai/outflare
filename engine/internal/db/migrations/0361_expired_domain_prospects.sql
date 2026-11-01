-- Link prospects found through an expired domain: a page that still links
-- to a domain that has lapsed, offered a page of ours instead.
ALTER TABLE link_prospects DROP CONSTRAINT IF EXISTS link_prospects_method_check;
ALTER TABLE link_prospects ADD CONSTRAINT link_prospects_method_check
    CHECK (method IN ('competitor_gap', 'broken_link', 'reclaim', 'competitor_dead_page',
        'resource_page', 'asset', 'unlinked_mention', 'partner', 'manual', 'expired_domain'));
