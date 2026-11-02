-- Journalists become a fact on their person record: the catalog's built-in
-- journalist field (true), with the topics they cover in beats. The news
-- watch sets both for the authors it finds from now on, and people can add
-- journalists by hand. This marks the authors it found before, and
-- suggests their beats from the names of the topic searches their articles
-- came up in, as the news watch does now (source enrichment, so a
-- person's own edit always wins). It touches only authors' records, a few
-- per article read, and skips any a person already set.

WITH beats AS (
    SELECT aa.record_id, jsonb_agg(DISTINCT left(n.name, 100)) AS names
    FROM news_article_authors aa
    JOIN news_hits h ON h.article_id = aa.article_id
    JOIN news_searches n ON n.id = h.search_id AND n.purpose = 'topic'
    GROUP BY aa.record_id
)
UPDATE records r SET
    attributes = r.attributes || '{"journalist": true}'::jsonb
        || CASE WHEN r.attributes ? 'beats' OR b.names IS NULL THEN '{}'::jsonb ELSE jsonb_build_object('beats', b.names) END,
    attribute_sources = r.attribute_sources || '{"journalist": "enrichment"}'::jsonb
        || CASE WHEN r.attributes ? 'beats' OR b.names IS NULL THEN '{}'::jsonb ELSE '{"beats": "enrichment"}'::jsonb END,
    version = r.version + 1,
    updated_at = now()
FROM (SELECT DISTINCT record_id FROM news_article_authors) a
LEFT JOIN beats b ON b.record_id = a.record_id
WHERE r.id = a.record_id AND r.record_type = 'person' AND r.merged_into IS NULL AND NOT r.attributes ? 'journalist'
  AND NOT EXISTS (SELECT 1 FROM field_definitions f WHERE f.workspace_id = r.workspace_id AND f.record_type = 'person'
      AND f.key IN ('journalist', 'beats') AND f.brand_id IS NOT DISTINCT FROM r.brand_id);
