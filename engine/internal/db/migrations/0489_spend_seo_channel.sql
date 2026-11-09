-- Outcomes counts only outreach costs (outcomes.OutreachChannels). The
-- engine books searches, rank checks, SEO reports, site crawl reads and
-- news searches on the SEO channel now, with the brand they ran for. This
-- moves the charges booked before onto that channel, so they stop counting
-- toward cost per meeting, and gives each the brand of the tracked site its
-- domain names, where there is one. Data only: the old version reads and
-- writes the same columns.
UPDATE spend_entries
SET channel = 'seo'
WHERE channel = '' AND source = 'auto' AND category = 'data' AND sequence_id IS NULL
  AND (note LIKE 'Search on %' OR note LIKE 'Rank check on %' OR note LIKE 'SEO %'
       OR note LIKE 'Page read for a site crawl%' OR note LIKE 'News search%');

UPDATE spend_entries e
SET brand_id = s.brand_id
FROM seo_sites s
WHERE e.channel = 'seo' AND e.brand_id IS NULL AND e.domain <> ''
  AND s.workspace_id = e.workspace_id AND s.brand_id IS NOT NULL
  AND (lower(s.host) = e.domain OR lower(s.host) = 'www.' || e.domain);
