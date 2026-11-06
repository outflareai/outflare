-- Brand knowledge: what a brand's own website says it offers, read through
-- the workspace's own page reader (Geonode) and summarised by the
-- workspace's own AI model into bullets a person can edit: products,
-- services, features, integrations, capabilities, who it serves, use cases
-- and the industry, each with the page it came from. AI visibility drafts
-- its questions from it; agents read it with the brand's context.

ALTER TABLE brand_context
    ADD COLUMN knowledge         jsonb NOT NULL DEFAULT '{}',
    -- When the website was last read, how many pages, and why the last
    -- read failed ('' when it worked).
    ADD COLUMN knowledge_read_at timestamptz,
    ADD COLUMN knowledge_pages   integer NOT NULL DEFAULT 0,
    ADD COLUMN knowledge_error   text NOT NULL DEFAULT '';
