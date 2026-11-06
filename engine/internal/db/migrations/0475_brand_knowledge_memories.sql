-- Brands are fully separate, so knowledge and memories saved with no
-- brand stop counting for every brand. In a workspace with brands, each
-- one goes to the workspace's first brand (keeping its id, history and
-- passages), and every other brand gets its own copy. The previous
-- version shows a brand its own rows and those with no brand, so during a
-- deploy each brand still sees each item once.

-- Folders, with their place in the tree, one copy per other brand.
CREATE TEMP TABLE knowledge_folder_copies ON COMMIT DROP AS
SELECT c.id AS old_id, b.id AS brand_id, gen_random_uuid() AS new_id
FROM knowledge_collections c
JOIN brands b ON b.workspace_id = c.workspace_id AND b.id <> outflare_first_brand(c.workspace_id)
WHERE c.brand_id IS NULL;

INSERT INTO knowledge_collections (id, workspace_id, brand_id, parent_id, name, description, position, created_by,
    created_at, updated_at, icon)
SELECT f.new_id, c.workspace_id, f.brand_id, NULL, c.name, c.description, c.position, c.created_by,
    c.created_at, c.updated_at, c.icon
FROM knowledge_folder_copies f JOIN knowledge_collections c ON c.id = f.old_id;

-- A copied folder sits inside its brand's copy of the parent, or the
-- brand's own parent when the parent already had that brand.
UPDATE knowledge_collections n SET parent_id = COALESCE(p.new_id, o.parent_id)
FROM knowledge_folder_copies f
JOIN knowledge_collections o ON o.id = f.old_id
LEFT JOIN knowledge_folder_copies p ON p.old_id = o.parent_id AND p.brand_id = f.brand_id
WHERE n.id = f.new_id AND o.parent_id IS NOT NULL
  AND (p.new_id IS NOT NULL OR EXISTS (SELECT 1 FROM knowledge_collections x WHERE x.id = o.parent_id AND x.brand_id = f.brand_id));

-- Items: a copy per other brand, in that brand's copy of its folder, with
-- its current version. Passages are made for the copies on first read.
CREATE TEMP TABLE knowledge_item_copies ON COMMIT DROP AS
SELECT i.id AS old_id, b.id AS brand_id, gen_random_uuid() AS new_id
FROM knowledge_items i
JOIN brands b ON b.workspace_id = i.workspace_id AND b.id <> outflare_first_brand(i.workspace_id)
WHERE i.brand_id IS NULL AND i.deleted_at IS NULL
  AND (i.source_url IS NULL OR NOT EXISTS (SELECT 1 FROM knowledge_items o WHERE o.workspace_id = i.workspace_id
    AND o.brand_id = b.id AND o.source_url = i.source_url));

INSERT INTO knowledge_items (id, workspace_id, brand_id, collection_id, kind, title, body, visibility, status, version,
    owner_id, review_by, created_by, updated_by, created_at, updated_at, source_url, source_missing_at)
SELECT f.new_id, i.workspace_id, f.brand_id,
    CASE WHEN i.collection_id IS NULL THEN NULL
         ELSE COALESCE(fc.new_id, (SELECT x.id FROM knowledge_collections x WHERE x.id = i.collection_id AND x.brand_id = f.brand_id)) END,
    i.kind, i.title, i.body, i.visibility, i.status, i.version,
    i.owner_id, i.review_by, i.created_by, i.updated_by, i.created_at, i.updated_at, i.source_url, i.source_missing_at
FROM knowledge_item_copies f
JOIN knowledge_items i ON i.id = f.old_id
LEFT JOIN knowledge_folder_copies fc ON fc.old_id = i.collection_id AND fc.brand_id = f.brand_id;

INSERT INTO knowledge_versions (workspace_id, item_id, version, title, body, created_by, created_at, brand_id)
SELECT i.workspace_id, f.new_id, i.version, i.title, i.body, i.updated_by, i.updated_at, f.brand_id
FROM knowledge_item_copies f JOIN knowledge_items i ON i.id = f.old_id
ON CONFLICT (item_id, version) DO NOTHING;

-- The originals become the first brand's.
UPDATE knowledge_collections SET brand_id = outflare_first_brand(workspace_id)
WHERE brand_id IS NULL;
UPDATE knowledge_items SET brand_id = outflare_first_brand(workspace_id)
WHERE brand_id IS NULL;
UPDATE knowledge_versions v SET brand_id = i.brand_id
FROM knowledge_items i WHERE v.item_id = i.id AND v.brand_id IS NULL AND i.brand_id IS NOT NULL;
UPDATE knowledge_chunks c SET brand_id = i.brand_id
FROM knowledge_items i WHERE c.item_id = i.id AND c.brand_id IS NULL AND i.brand_id IS NOT NULL;

-- Memories: the same, one copy per other brand.
INSERT INTO memories (workspace_id, kind, text, pinned, created_by, mission_id, created_at, updated_at, brand_id)
SELECT m.workspace_id, m.kind, m.text, m.pinned, m.created_by, m.mission_id, m.created_at, m.updated_at, b.id
FROM memories m
JOIN brands b ON b.workspace_id = m.workspace_id AND b.id <> outflare_first_brand(m.workspace_id)
WHERE m.brand_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM memories o WHERE o.workspace_id = m.workspace_id AND o.brand_id = b.id
    AND lower(o.text) = lower(m.text));
UPDATE memories SET brand_id = outflare_first_brand(workspace_id)
WHERE brand_id IS NULL;

-- Knowledge's suggestions, website imports and reply imports with no
-- brand go with the first brand too.
UPDATE knowledge_suggestions SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE knowledge_website_imports SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
UPDATE knowledge_reply_imports SET brand_id = outflare_first_brand(workspace_id) WHERE brand_id IS NULL;
