-- More on-page facts for the site audit: the Open Graph title and image
-- that link previews use, and how many scripts, styles, images and frames
-- a secure page loads over plain http (mixed content).
ALTER TABLE crawl_pages
    ADD COLUMN og_title text NOT NULL DEFAULT '',
    ADD COLUMN og_image text NOT NULL DEFAULT '',
    ADD COLUMN insecure_resources integer NOT NULL DEFAULT 0;
