-- What AI answers need from a crawled page, beyond what search needs:
-- whether it lets them quote it (nosnippet, max-snippet, noarchive),
-- whether its question headings have answers right under them, its
-- article's author and dates, a Markdown copy, and structured data that
-- says what the page doesn't. Read from the HTML the crawl already
-- fetched; the audit turns it into ai_visibility rules. A page from
-- before this has {} and breaks none of them.
ALTER TABLE crawl_pages ADD COLUMN ai_signals jsonb NOT NULL DEFAULT '{}';
