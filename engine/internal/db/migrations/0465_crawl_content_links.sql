-- Links from a page's content, apart from its menu, sidebar and the site's
-- header and footer, which most pages repeat. content_links_in counts the
-- pages that link to a page from their content: how much the site's own
-- writing points at it, which a menu link on every page hides.
ALTER TABLE crawl_links
    ADD COLUMN boilerplate boolean NOT NULL DEFAULT false;

ALTER TABLE crawl_pages
    ADD COLUMN content_links_in integer NOT NULL DEFAULT 0;
