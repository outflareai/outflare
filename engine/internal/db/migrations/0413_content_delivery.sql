-- Content delivery: what a brand's own site needs to put an approved
-- post or page in place itself, sent in the content.approved webhook
-- (docs/content.md, "Delivering to your own site"). Each field is
-- optional; an empty one is left out of the webhook.
ALTER TABLE content_items ADD COLUMN language text NOT NULL DEFAULT '' CHECK (length(language) <= 35);
-- Where the page goes on the site: type, collection, path, parent page,
-- a page to follow the layout of, and a menu label.
ALTER TABLE content_items ADD COLUMN placement jsonb NOT NULL DEFAULT '{}' CHECK (pg_column_size(placement) <= 8192);
-- Meta tags, keywords, search intent, robots, Open Graph and structured
-- data (JSON-LD).
ALTER TABLE content_items ADD COLUMN seo jsonb NOT NULL DEFAULT '{}' CHECK (pg_column_size(seo) <= 65536);
-- Internal and external links in the page, and existing pages that
-- should link to it.
ALTER TABLE content_items ADD COLUMN links jsonb NOT NULL DEFAULT '{}' CHECK (pg_column_size(links) <= 65536);
-- Images: address, role, alt text, size, file name, credit.
ALTER TABLE content_items ADD COLUMN media jsonb NOT NULL DEFAULT '[]' CHECK (pg_column_size(media) <= 32768);
-- Why the page exists, what to check before it goes live, overlapping
-- pages and redirects.
ALTER TABLE content_items ADD COLUMN brief jsonb NOT NULL DEFAULT '{}' CHECK (pg_column_size(brief) <= 32768);
-- Each approval counts up, so a receiver replaces the version it has.
ALTER TABLE content_items ADD COLUMN revision integer NOT NULL DEFAULT 0;
ALTER TABLE content_items ADD COLUMN approved_by uuid REFERENCES actors (id) ON DELETE SET NULL;
ALTER TABLE content_items ADD COLUMN approved_at timestamptz;
